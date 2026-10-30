package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/monitor"
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

func TestMariaDBMonitorEventLifecycle(t *testing.T) {
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
	project, err := st.SaveProject(ctx, domain.Project{Key: "monitoring", Name: "Monitoring", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	service, err := st.SaveService(ctx, domain.Service{ProjectID: project.ID, Key: "web", Name: "Web", Kind: "application", Criticality: "high", Enabled: true, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Truncate(time.Microsecond)
	unhealthy := domain.MonitorEvent{EventID: "monitor-1", ServiceID: service.ID, Source: "integration", Status: "unhealthy", Severity: "critical", Summary: "Health endpoint failed", Fingerprint: "health-endpoint", Details: json.RawMessage(`{}`), ObservedAt: observed}
	result, err := st.RecordMonitorEvent(ctx, unhealthy, "owner@s.whatsapp.net")
	if err != nil || len(result.Transitions) != 1 || result.Transitions[0].Kind != "opened" {
		t.Fatalf("open alert: result=%#v err=%v", result, err)
	}
	duplicate, err := st.RecordMonitorEvent(ctx, unhealthy, "owner@s.whatsapp.net")
	if err != nil || !duplicate.Duplicate || len(duplicate.Transitions) != 0 {
		t.Fatalf("deduplicate event: result=%#v err=%v", duplicate, err)
	}
	healthy := domain.MonitorEvent{EventID: "monitor-2", ServiceID: service.ID, Source: "integration", Status: "healthy", Severity: "info", Summary: "Health endpoint recovered", Details: json.RawMessage(`{}`), ObservedAt: observed.Add(time.Minute)}
	result, err = st.RecordMonitorEvent(ctx, healthy, "owner@s.whatsapp.net")
	if err != nil || len(result.Transitions) != 1 || result.Transitions[0].Kind != "resolved" {
		t.Fatalf("resolve alert: result=%#v err=%v", result, err)
	}
	unhealthy.EventID = "monitor-3"
	unhealthy.ObservedAt = observed.Add(2 * time.Minute)
	result, err = st.RecordMonitorEvent(ctx, unhealthy, "owner@s.whatsapp.net")
	if err != nil || len(result.Transitions) != 1 || result.Transitions[0].Kind != "reopened" {
		t.Fatalf("reopen alert: result=%#v err=%v", result, err)
	}
	alerts, err := st.ListServiceAlerts(ctx, "open")
	if err != nil || len(alerts) != 1 || alerts[0].ServiceName != service.Name {
		t.Fatalf("list alerts: items=%#v err=%v", alerts, err)
	}
	dashboard, err := st.Dashboard(ctx)
	if err != nil || dashboard.OpenAlerts != 1 {
		t.Fatalf("dashboard alert count: item=%#v err=%v", dashboard, err)
	}
	var notifications int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox`).Scan(&notifications); err != nil || notifications != 3 {
		t.Fatalf("alert transitions must enqueue atomically and once: count=%d err=%v", notifications, err)
	}
	atomicFailure := unhealthy
	atomicFailure.EventID = "monitor-atomic-failure"
	atomicFailure.Fingerprint = "atomic-failure"
	atomicFailure.ObservedAt = observed.Add(3 * time.Minute)
	if _, err := st.RecordMonitorEvent(ctx, atomicFailure, strings.Repeat("x", 256)); err == nil {
		t.Fatal("oversized notification owner unexpectedly committed")
	}
	var failedEvents int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_monitor_events WHERE event_id=?`, atomicFailure.EventID).Scan(&failedEvents); err != nil || failedEvents != 0 {
		t.Fatalf("failed notification did not roll back monitor event: count=%d err=%v", failedEvents, err)
	}
}

func TestMariaDBHeartbeatWatchdogLifecycle(t *testing.T) {
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
	project, err := st.SaveProject(ctx, domain.Project{Key: "watchdog", Name: "Watchdog", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	service, err := st.SaveService(ctx, domain.Service{ProjectID: project.ID, Key: "api", Name: "API", Kind: "application", Criticality: "high", Enabled: true, HeartbeatTimeoutSeconds: 60, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := st.db.ExecContext(ctx, `UPDATE echo_services SET updated_at=? WHERE id=?`, now.Add(-2*time.Minute), service.ID); err != nil {
		t.Fatal(err)
	}
	watchdog := monitor.NewWatchdog(st, nil, 5*time.Second)
	opened, err := watchdog.Sweep(ctx, now)
	if err != nil || len(opened) != 1 || opened[0].Kind != "opened" || opened[0].Alert.Fingerprint != "observer-silent" {
		t.Fatalf("open silence alert: transitions=%#v err=%v", opened, err)
	}
	duplicate, err := watchdog.Sweep(ctx, now.Add(10*time.Second))
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("deduplicate silence alert: transitions=%#v err=%v", duplicate, err)
	}
	healthy := domain.MonitorEvent{EventID: "observer-recovered", ServiceID: service.ID, Source: "integration", Status: "healthy", Severity: "info", Summary: "API is healthy", Details: json.RawMessage(`{}`), ObservedAt: now.Add(20 * time.Second)}
	recovery, err := st.RecordMonitorEvent(ctx, healthy)
	if err != nil || len(recovery.Transitions) != 1 || recovery.Transitions[0].Kind != "resolved" {
		t.Fatalf("resolve silence alert: result=%#v err=%v", recovery, err)
	}
	reopened, err := watchdog.Sweep(ctx, now.Add(2*time.Minute))
	if err != nil || len(reopened) != 1 || reopened[0].Kind != "reopened" {
		t.Fatalf("reopen silence alert: transitions=%#v err=%v", reopened, err)
	}
	service.HeartbeatTimeoutSeconds = 0
	if _, err := st.SaveService(ctx, service); err != nil {
		t.Fatal(err)
	}
	resolved, err := watchdog.Sweep(ctx, now.Add(3*time.Minute))
	if err != nil || len(resolved) != 1 || resolved[0].Kind != "resolved" {
		t.Fatalf("resolve disabled heartbeat policy: transitions=%#v err=%v", resolved, err)
	}
	service.HeartbeatTimeoutSeconds = 60
	if _, err := st.SaveService(ctx, service); err != nil {
		t.Fatal(err)
	}
	reenabled, err := watchdog.Sweep(ctx, now.Add(5*time.Minute))
	if err != nil || len(reenabled) != 1 || reenabled[0].Kind != "reopened" {
		t.Fatalf("reopen re-enabled heartbeat policy: transitions=%#v err=%v", reenabled, err)
	}
}

func TestMariaDBAssistantConversationIsIdempotent(t *testing.T) {
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
	conversation, inbound, created, err := st.BeginConversationTurn(ctx, "whatsapp", "27820000000@s.whatsapp.net", "wa-message-1", "Is anything wrong?")
	if err != nil || !created {
		t.Fatalf("begin turn: created=%v conversation=%#v inbound=%#v err=%v", created, conversation, inbound, err)
	}
	duplicateConversation, duplicateInbound, created, err := st.BeginConversationTurn(ctx, "whatsapp", "27820000000@s.whatsapp.net", "wa-message-1", "Is anything wrong?")
	if err != nil || created || duplicateConversation.ID != conversation.ID || duplicateInbound.ID != inbound.ID {
		t.Fatalf("deduplicate turn: created=%v conversation=%#v inbound=%#v err=%v", created, duplicateConversation, duplicateInbound, err)
	}
	run := domain.ToolRun{CallID: "call-1", Name: "get_open_alerts", Arguments: json.RawMessage(`{}`), Output: `{"alerts":[]}`, Status: "succeeded", DurationMS: 4}
	reply, err := st.CompleteConversationTurn(ctx, conversation.ID, inbound.ID, "No open alerts.", "ai_investigation", []domain.ToolRun{run}, "owner@s.whatsapp.net")
	if err != nil || reply.ReplyToMessageID != inbound.ID {
		t.Fatalf("complete turn: reply=%#v err=%v", reply, err)
	}
	sameReply, err := st.CompleteConversationTurn(ctx, conversation.ID, inbound.ID, "No open alerts.", "ai_investigation", []domain.ToolRun{run}, "owner@s.whatsapp.net")
	if err != nil || sameReply.ID != reply.ID {
		t.Fatalf("idempotent reply: reply=%#v err=%v", sameReply, err)
	}
	var outboxCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox`).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("reply must enqueue exactly once: count=%d err=%v", outboxCount, err)
	}
	outbound, err := st.ClaimOutbound(ctx, time.Minute)
	if err != nil || outbound.ChatJID != "owner@s.whatsapp.net" || outbound.Body != reply.Body {
		t.Fatalf("claim assistant reply: outbound=%#v err=%v", outbound, err)
	}
	messages, err := st.ListConversationMessages(ctx, conversation.ID, 12)
	if err != nil || len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("conversation history: messages=%#v err=%v", messages, err)
	}

	_, rollbackInbound, created, err := st.BeginConversationTurn(ctx, "whatsapp", "27820000000@s.whatsapp.net", "wa-message-2", "Test a failed completion")
	if err != nil || !created {
		t.Fatalf("begin rollback turn: created=%v inbound=%#v err=%v", created, rollbackInbound, err)
	}
	invalidRun := domain.ToolRun{CallID: "call-invalid", Name: "get_status", Arguments: json.RawMessage(`{}`), Status: "running"}
	if _, err := st.CompleteConversationTurn(ctx, conversation.ID, rollbackInbound.ID, "This must roll back.", "ai_investigation", []domain.ToolRun{invalidRun}, "owner@s.whatsapp.net"); err == nil {
		t.Fatal("invalid tool run unexpectedly completed")
	}
	if _, err := st.ConversationReply(ctx, rollbackInbound.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed completion persisted reply: err=%v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox`).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("failed completion changed outbox: count=%d err=%v", outboxCount, err)
	}
}

func cleanupIntegration(t *testing.T, st *Store) {
	t.Helper()
	tables := []string{"echo_audit_events", "echo_whatsapp_outbox", "echo_jobs", "echo_deployments", "echo_approvals", "echo_change_sets", "echo_analyses", "echo_incident_messages", "echo_incidents", "echo_tool_runs", "echo_conversation_messages", "echo_conversations", "echo_service_alerts", "echo_monitor_events", "echo_services", "echo_environments", "echo_repositories", "echo_group_bindings", "echo_projects"}
	for _, table := range tables {
		if _, err := st.db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
}
