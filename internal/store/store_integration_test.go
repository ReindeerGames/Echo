package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestMariaDBIncidentIsIdempotent(t *testing.T) {
	dsn := os.Getenv("ECHO_TEST_CORE_DSN")
	if dsn == "" {
		t.Skip("ECHO_TEST_CORE_DSN not set")
	}
	ctx := context.Background()
	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cleanupIntegration(t, st)
	project, err := st.SaveProject(ctx, domain.Project{Key: "integration", Name: "Integration", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := st.SaveGroupBinding(ctx, domain.GroupBinding{ProjectID: project.ID, GroupJID: "integration@g.us", GroupName: "Integration", MentionRequired: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	message := domain.InboundMessage{MessageID: "message-1", ChatJID: binding.GroupJID, SenderJID: "reporter@s.whatsapp.net", SenderName: "Reporter", Body: "forms are blank", IsGroup: true, MentionedJIDs: []string{"echo@s.whatsapp.net"}, Timestamp: time.Now().UTC()}
	first, created, err := st.CreateIncident(ctx, binding, message)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	second, created, err := st.CreateIncident(ctx, binding, message)
	if err != nil || created {
		t.Fatalf("duplicate create: created=%v err=%v", created, err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotency failed: %s != %s", first.ID, second.ID)
	}
	repository, err := st.SaveRepository(ctx, domain.Repository{ProjectID: project.ID, Owner: "example", Name: "site", Role: "application", DefaultBranch: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	artifact := "0123456789012345678901234567890123456789"
	change, err := st.CreateChangeSet(ctx, domain.ChangeSet{IncidentID: first.ID, RepositoryID: repository.ID, BranchName: "echo/test", CommitSHA: artifact, ArtifactRef: artifact, Status: "testing"})
	if err != nil {
		t.Fatal(err)
	}
	byIncident, err := st.FindChangeSetByArtifact(ctx, first.ID, artifact)
	if err != nil || byIncident.ID != change.ID {
		t.Fatalf("find incident change set: item=%#v err=%v", byIncident, err)
	}
	byProject, err := st.FindProjectChangeSetByArtifact(ctx, project.ID, artifact)
	if err != nil || byProject.ID != change.ID {
		t.Fatalf("find project change set: item=%#v err=%v", byProject, err)
	}
	deployment, err := st.CreateDeployment(ctx, domain.Deployment{IncidentID: first.ID, ProjectID: project.ID, Environment: "staging", ArtifactRef: artifact, Status: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	storedDeployment, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil || storedDeployment.ArtifactRef != artifact {
		t.Fatalf("get deployment: item=%#v err=%v", storedDeployment, err)
	}
	approval, err := st.CreateApproval(ctx, domain.Approval{IncidentID: first.ID, ActionType: "deploy_production", Action: json.RawMessage(`{"artifactRef":"0123456789012345678901234567890123456789"}`), RequestedBy: "integration", ExpiresAt: time.Now().Add(time.Hour)}, "0123456789012345678901234567890123456789012345678901234567890123")
	if err != nil {
		t.Fatal(err)
	}
	dispatching, err := st.CreateDeployment(ctx, domain.Deployment{IncidentID: first.ID, ApprovalID: approval.ID, ProjectID: project.ID, Environment: "production", ArtifactRef: artifact})
	if err != nil || dispatching.Status != "dispatching" {
		t.Fatalf("create dispatching deployment: item=%#v err=%v", dispatching, err)
	}
	byApproval, err := st.FindDeploymentByApproval(ctx, dispatching.ApprovalID)
	if err != nil || byApproval.ID != dispatching.ID {
		t.Fatalf("find deployment by approval: item=%#v err=%v", byApproval, err)
	}
	if err := st.MarkDeploymentQueued(ctx, dispatching.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkChangeSetMerged(ctx, change.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkChangeSetMerged(ctx, change.ID); err != nil {
		t.Fatalf("marking an already merged change must be idempotent: %v", err)
	}
	mergeApproval, err := st.CreateApproval(ctx, domain.Approval{IncidentID: first.ID, ActionType: "merge_production", Action: json.RawMessage(`{"changeSetId":"` + change.ID + `"}`), RequestedBy: "integration", ExpiresAt: time.Now().Add(time.Hour)}, "1123456789012345678901234567890123456789012345678901234567890123")
	if err != nil {
		t.Fatalf("merge production approval migration is unavailable: %v", err)
	}
	nativeDeployment, err := st.CreateDeployment(ctx, domain.Deployment{IncidentID: first.ID, ApprovalID: mergeApproval.ID, ProjectID: project.ID, Environment: "production", ArtifactRef: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", Status: "running", Repository: "example/site", Workflow: "repository-native-main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteRepositoryNativeDeployment(ctx, nativeDeployment.ID, 42, "https://github.test/actions/42", true); err != nil {
		t.Fatal(err)
	}
	completed, err := st.GetDeployment(ctx, nativeDeployment.ID)
	if err != nil || completed.Status != "verified" || completed.ExternalRunID != 42 {
		t.Fatalf("repository-native deployment not verified: item=%#v err=%v", completed, err)
	}
	resolved, err := st.GetIncident(ctx, first.ID)
	if err != nil || resolved.Status != "resolved" || resolved.ResolvedAt == nil {
		t.Fatalf("repository-native success did not resolve incident: item=%#v err=%v", resolved, err)
	}
}

func cleanupIntegration(t *testing.T, st *Store) {
	t.Helper()
	tables := []string{"echo_audit_events", "echo_whatsapp_outbox", "echo_jobs", "echo_deployments", "echo_approvals", "echo_change_sets", "echo_analyses", "echo_incident_messages", "echo_incidents", "echo_environments", "echo_repositories", "echo_group_bindings", "echo_projects"}
	for _, table := range tables {
		if _, err := st.db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
}
