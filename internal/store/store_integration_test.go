package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
	"github.com/ReindeerGames/echo/internal/monitor"
)

func TestMariaDBSchemaStatusDetectsDrift(t *testing.T) {
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
	status, err := st.SchemaStatus(ctx)
	if err != nil || !status.Current || status.Missing != 0 || status.Expected != status.Applied {
		t.Fatalf("unexpected current schema status: status=%#v err=%v", status, err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") && entry.Name() > version {
			version = entry.Name()
		}
	}
	if version == "" {
		t.Fatal("no embedded migration found")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM echo_schema_migrations WHERE version=?`, version); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := st.db.ExecContext(context.Background(), `INSERT IGNORE INTO echo_schema_migrations (version) VALUES (?)`, version); err != nil {
			t.Errorf("restore migration record: %v", err)
		}
	}()
	status, err = st.SchemaStatus(ctx)
	if err != nil || status.Current || status.Missing != 1 || status.Applied != status.Expected-1 {
		t.Fatalf("schema drift not detected: status=%#v err=%v", status, err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT IGNORE INTO echo_schema_migrations (version) VALUES (?)`, version); err != nil {
		t.Fatal(err)
	}
	const futureVersion = "999_future_test.sql"
	if _, err := st.db.ExecContext(ctx, `INSERT INTO echo_schema_migrations (version) VALUES (?)`, futureVersion); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := st.db.ExecContext(context.Background(), `DELETE FROM echo_schema_migrations WHERE version=?`, futureVersion); err != nil {
			t.Errorf("remove future migration record: %v", err)
		}
	}()
	status, err = st.SchemaStatus(ctx)
	if err != nil || status.Current || status.Missing != 0 || status.Unexpected != 1 {
		t.Fatalf("unexpected migration not detected: status=%#v err=%v", status, err)
	}
}

func TestMariaDBRuntimeComponentAlertLifecycleIsAtomicAndDeduplicated(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := "owner@s.whatsapp.net"
	if transition, err := st.ReconcileRuntimeComponent(ctx, "monitor-watchdog", "running", true, now, owner); err != nil || transition != nil {
		t.Fatalf("healthy component without alert: transition=%#v err=%v", transition, err)
	}

	opened, err := st.ReconcileRuntimeComponent(ctx, "worker", "error", false, now, owner)
	if err != nil || opened == nil || opened.Kind != "opened" {
		t.Fatalf("open runtime alert: transition=%#v err=%v", opened, err)
	}
	duplicate, err := st.ReconcileRuntimeComponent(ctx, "worker", "error", false, now.Add(time.Second), owner)
	if err != nil || duplicate != nil {
		t.Fatalf("duplicate runtime alert: transition=%#v err=%v", duplicate, err)
	}
	resolved, err := st.ReconcileRuntimeComponent(ctx, "worker", "running", true, now.Add(30*time.Second), owner)
	if err != nil || resolved == nil || resolved.Kind != "resolved" {
		t.Fatalf("resolve runtime alert: transition=%#v err=%v", resolved, err)
	}
	if transition, err := st.ReconcileRuntimeComponent(ctx, "worker", "running", true, now.Add(time.Minute), owner); err != nil || transition != nil {
		t.Fatalf("duplicate runtime recovery: transition=%#v err=%v", transition, err)
	}
	reopened, err := st.ReconcileRuntimeComponent(ctx, "worker", "error", false, now.Add(2*time.Minute), owner)
	if err != nil || reopened == nil || reopened.Kind != "reopened" {
		t.Fatalf("reopen runtime alert: transition=%#v err=%v", reopened, err)
	}
	disabled, err := st.ReconcileRuntimeComponent(ctx, "worker", "disabled", true, now.Add(3*time.Minute), owner)
	if err != nil || disabled == nil || disabled.Kind != "resolved" || disabled.Status != "disabled" {
		t.Fatalf("disable runtime component: transition=%#v err=%v", disabled, err)
	}

	var notifications, audits int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE chat_jid=?`, owner).Scan(&notifications); err != nil || notifications != 4 {
		t.Fatalf("runtime notifications count=%d err=%v", notifications, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_audit_events WHERE resource_type='runtime_component' AND resource_id='worker'`).Scan(&audits); err != nil || audits != 4 {
		t.Fatalf("runtime audits count=%d err=%v", audits, err)
	}
	var open int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_runtime_component_alerts WHERE component_name='worker' AND alert_status='open'`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("runtime alert state count=%d err=%v", open, err)
	}
}

func TestMariaDBOwnerSelectionQueuesConfirmationAtomically(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, _, err := st.UpsertWhatsAppContact(ctx, domain.InboundMessage{MessageID: "owner-contact-1", ChatJID: "27820000000@s.whatsapp.net", SenderJID: "27820000000@s.whatsapp.net", SenderName: "Jason", Body: "hello", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	selected, _, err := st.SetOwnerContact(ctx, first.ID, "admin")
	if err != nil || selected.ID != first.ID {
		t.Fatalf("select owner: contact=%#v err=%v", selected, err)
	}
	outbound, err := st.ClaimOutbound(ctx, time.Minute)
	if err != nil || outbound.ID == "" || outbound.ChatJID != first.JID || !strings.Contains(outbound.Body, "Echo owner contact confirmed") || !strings.Contains(outbound.Body, "Send “brief”") {
		t.Fatalf("owner confirmation: outbound=%#v err=%v", outbound, err)
	}
	second, _, err := st.UpsertWhatsAppContact(ctx, domain.InboundMessage{MessageID: "owner-contact-2", ChatJID: "27821111111@s.whatsapp.net", SenderJID: "27821111111@s.whatsapp.net", SenderName: "Other", Body: "hello", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetOwnerContact(ctx, second.ID, strings.Repeat("x", 256)); err == nil {
		t.Fatal("oversized actor unexpectedly committed owner selection")
	}
	current, _, err := st.GetOwnerContact(ctx)
	if err != nil || current.ID != first.ID {
		t.Fatalf("failed owner selection changed current owner: contact=%#v err=%v", current, err)
	}
	var confirmations int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox`).Scan(&confirmations); err != nil || confirmations != 1 {
		t.Fatalf("failed owner selection changed outbox: count=%d err=%v", confirmations, err)
	}
}

func TestMariaDBDailyBriefingIsQueuedExactlyOncePerLocalDay(t *testing.T) {
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

	settings, err := st.SaveBriefingSettings(ctx, domain.BriefingSettings{Enabled: true, LocalTime: "08:00", Timezone: "Africa/Johannesburg"})
	if err != nil || !settings.Enabled || settings.LocalTime != "08:00" || settings.Timezone != "Africa/Johannesburg" {
		t.Fatalf("save briefing settings: settings=%#v err=%v", settings, err)
	}
	before := time.Date(2026, 10, 30, 5, 59, 0, 0, time.UTC)
	if queued, err := st.EnqueueDailyBriefIfDue(ctx, "owner@s.whatsapp.net", "Echo daily brief", before); err != nil || queued {
		t.Fatalf("brief queued before local schedule: queued=%v err=%v", queued, err)
	}
	due := before.Add(time.Minute)
	if queued, err := st.EnqueueDailyBriefIfDue(ctx, "owner@s.whatsapp.net", "Echo daily brief", due); err != nil || !queued {
		t.Fatalf("due brief not queued: queued=%v err=%v", queued, err)
	}
	if queued, err := st.EnqueueDailyBriefIfDue(ctx, "owner@s.whatsapp.net", "duplicate", due.Add(time.Hour)); err != nil || queued {
		t.Fatalf("duplicate brief queued: queued=%v err=%v", queued, err)
	}

	settings, err = st.GetBriefingSettings(ctx)
	if err != nil || settings.LastSentLocalDate != "2026-10-30" || settings.LastSentAt == nil || !settings.LastSentAt.Equal(due) {
		t.Fatalf("unexpected sent state: settings=%#v err=%v", settings, err)
	}
	var outbox, audits, duplicates int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE chat_jid='owner@s.whatsapp.net' AND body='Echo daily brief'`).Scan(&outbox); err != nil || outbox != 1 {
		t.Fatalf("daily briefing outbox count=%d err=%v", outbox, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE body='duplicate'`).Scan(&duplicates); err != nil || duplicates != 0 {
		t.Fatalf("duplicate briefing outbox count=%d err=%v", duplicates, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_audit_events WHERE action_name='briefing.queued' AND resource_id='daily_brief'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("daily briefing audit count=%d err=%v", audits, err)
	}
}

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
	imageData := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 504)...)
	imageDigest := sha256.Sum256(imageData)
	message := domain.InboundMessage{MessageID: "message-1", ChatJID: binding.GroupJID, SenderJID: "reporter@s.whatsapp.net", SenderName: "Reporter", Body: "forms are blank", IsGroup: true, MentionedJIDs: []string{"echo@s.whatsapp.net"}, Attachments: []domain.IncidentAttachment{{Kind: "image", MIMEType: "image/png", FileName: "screen.png", SizeBytes: int64(len(imageData)), SHA256: hex.EncodeToString(imageDigest[:]), Data: imageData}}, Timestamp: time.Now().UTC()}
	first, created, err := st.CreateIncident(ctx, binding, message)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	attachments, err := st.LoadIncidentAttachments(ctx, first.ID)
	if err != nil || len(attachments) != 1 || attachments[0].MIMEType != "image/png" || attachments[0].FileName != "screen.png" || string(attachments[0].Data) != string(imageData) {
		t.Fatalf("incident attachment not retained: attachments=%#v err=%v", attachments, err)
	}
	metadata, err := st.ListIncidentAttachments(ctx, first.ID)
	if err != nil || len(metadata) != 1 || len(metadata[0].Data) != 0 {
		t.Fatalf("attachment metadata view loaded image bytes: attachments=%#v err=%v", metadata, err)
	}
	tampered := message
	tampered.MessageID = "message-invalid-attachment"
	tampered.Attachments = append([]domain.IncidentAttachment(nil), message.Attachments...)
	tampered.Attachments[0].SHA256 = strings.Repeat("0", 64)
	if _, _, err := st.CreateIncident(ctx, binding, tampered); err == nil {
		t.Fatal("tampered attachment hash was accepted")
	}
	var tamperedIncidents int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_incidents WHERE source_message_id=?`, tampered.MessageID).Scan(&tamperedIncidents); err != nil || tamperedIncidents != 0 {
		t.Fatalf("invalid attachment did not roll back incident: count=%d err=%v", tamperedIncidents, err)
	}
	second, created, err := st.CreateIncident(ctx, binding, message)
	if err != nil || created {
		t.Fatalf("duplicate create: created=%v err=%v", created, err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotency failed: %s != %s", first.ID, second.ID)
	}
	bySource, err := st.FindIncidentBySource(ctx, message.ChatJID, message.MessageID)
	if err != nil || bySource.ID != first.ID {
		t.Fatalf("find incident by source: incident=%#v err=%v", bySource, err)
	}
	followUp := domain.InboundMessage{MessageID: "message-2", ChatJID: binding.GroupJID, SenderJID: "second-reporter@s.whatsapp.net", SenderName: "Second Reporter", Body: "It only fails in Safari", IsGroup: true, ReplyToMessageID: message.MessageID, Attachments: []domain.IncidentAttachment{{Kind: "image", MIMEType: "image/png", FileName: "safari.png", SizeBytes: int64(len(imageData)), SHA256: hex.EncodeToString(imageDigest[:]), Data: imageData}}, Timestamp: time.Now().UTC().Add(time.Second)}
	updated, appended, err := st.AppendIncidentFollowUp(ctx, first.ID, followUp)
	if err != nil || !appended || updated.EvidenceRevision != 2 || updated.Status != "triaging" {
		t.Fatalf("append incident follow-up: incident=%#v appended=%v err=%v", updated, appended, err)
	}
	duplicateFollowUp, appended, err := st.AppendIncidentFollowUp(ctx, first.ID, followUp)
	if err != nil || appended || duplicateFollowUp.ID != first.ID || duplicateFollowUp.EvidenceRevision != 2 {
		t.Fatalf("duplicate follow-up was not idempotent: incident=%#v appended=%v err=%v", duplicateFollowUp, appended, err)
	}
	byReference, err := st.FindIncidentByMessageReference(ctx, binding.GroupJID, followUp.MessageID)
	if err != nil || byReference.ID != first.ID {
		t.Fatalf("find incident by follow-up reference: incident=%#v err=%v", byReference, err)
	}
	messages, err := st.ListIncidentMessages(ctx, first.ID, 20)
	if err != nil || len(messages) != 2 || messages[1].ReplyToMessageID != message.MessageID || messages[1].Body != followUp.Body {
		t.Fatalf("incident thread was not retained: messages=%#v err=%v", messages, err)
	}
	attachments, err = st.LoadIncidentAttachments(ctx, first.ID)
	if err != nil || len(attachments) != 2 {
		t.Fatalf("follow-up attachment was not retained: attachments=%#v err=%v", attachments, err)
	}
	if _, err := st.SaveAnalysis(ctx, domain.Analysis{IncidentID: first.ID, EvidenceRevision: 1, Status: "complete", Confidence: "low", Summary: "stale", Evidence: json.RawMessage(`[]`), Proposal: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	staleResult, err := st.GetIncident(ctx, first.ID)
	if err != nil || staleResult.Status != "triaging" {
		t.Fatalf("stale analysis changed current incident state: incident=%#v err=%v", staleResult, err)
	}
	currentAnalysis, err := st.SaveAnalysis(ctx, domain.Analysis{IncidentID: first.ID, EvidenceRevision: 2, Status: "complete", Confidence: "medium", Summary: "Safari-specific regression", Evidence: json.RawMessage(`[]`), Proposal: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	latestAnalysis, err := st.LatestAnalysis(ctx, first.ID)
	if err != nil || latestAnalysis.ID != currentAnalysis.ID || latestAnalysis.EvidenceRevision != 2 {
		t.Fatalf("latest analysis did not follow evidence revision: analysis=%#v err=%v", latestAnalysis, err)
	}
	pendingPatch, err := st.CreateApproval(ctx, domain.Approval{IncidentID: first.ID, ActionType: "prepare_patch", Action: json.RawMessage(`{"repositoryId":"repo_test","evidenceRevision":2}`), RequestedBy: "integration", ExpiresAt: time.Now().Add(time.Hour)}, strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	thirdFollowUp := domain.InboundMessage{MessageID: "message-3", ChatJID: binding.GroupJID, SenderJID: "reporter@s.whatsapp.net", SenderName: "Reporter", Body: "Chrome is now affected too", IsGroup: true, ReplyToMessageID: followUp.MessageID, Timestamp: time.Now().UTC().Add(2 * time.Second)}
	updated, appended, err = st.AppendIncidentFollowUp(ctx, first.ID, thirdFollowUp)
	if err != nil || !appended || updated.EvidenceRevision != 3 {
		t.Fatalf("append third evidence revision: incident=%#v appended=%v err=%v", updated, appended, err)
	}
	approvals, err := st.ListApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pendingExpired := false
	for _, approval := range approvals {
		if approval.ID == pendingPatch.ID {
			pendingExpired = approval.Status == "expired"
		}
	}
	if !pendingExpired {
		t.Fatal("follow-up did not expire the pending approval")
	}
	stalePatch, err := st.CreateApproval(ctx, domain.Approval{IncidentID: first.ID, ActionType: "prepare_patch", Action: json.RawMessage(`{"repositoryId":"repo_test","evidenceRevision":2}`), RequestedBy: "integration", ExpiresAt: time.Now().Add(time.Hour)}, strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveApproval(ctx, stalePatch.ID, "integration", "approved"); !errors.Is(err, ErrApprovalStale) {
		t.Fatalf("stale patch approval was not rejected: %v", err)
	}
	var staleStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM echo_approvals WHERE id=?`, stalePatch.ID).Scan(&staleStatus); err != nil || staleStatus != "expired" {
		t.Fatalf("stale approval status=%q err=%v", staleStatus, err)
	}
	currentPatch, err := st.CreateApproval(ctx, domain.Approval{IncidentID: first.ID, ActionType: "prepare_patch", Action: json.RawMessage(`{"repositoryId":"repo_test","evidenceRevision":3}`), RequestedBy: "integration", ExpiresAt: time.Now().Add(time.Hour)}, strings.Repeat("6", 64))
	if err != nil {
		t.Fatal(err)
	}
	resolvedPatch, err := st.ResolveApproval(ctx, currentPatch.ID, "integration", "approved")
	if err != nil || resolvedPatch.Status != "approved" {
		t.Fatalf("current evidence approval was not resolved: approval=%#v err=%v", resolvedPatch, err)
	}
	var patchJobs int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_jobs WHERE job_type='prepare_patch' AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.approvalId'))=?`, currentPatch.ID).Scan(&patchJobs); err != nil || patchJobs != 1 {
		t.Fatalf("approved patch job count=%d err=%v", patchJobs, err)
	}
	var analysisJobs, acknowledgements int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_jobs WHERE job_type='analyze_incident'`).Scan(&analysisJobs); err != nil || analysisJobs != 3 {
		t.Fatalf("follow-up analysis job count=%d err=%v", analysisJobs, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox`).Scan(&acknowledgements); err != nil || acknowledgements != 3 {
		t.Fatalf("follow-up acknowledgement count=%d err=%v", acknowledgements, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE echo_incident_attachments SET created_at=? WHERE incident_id=?`, time.Now().UTC().Add(-48*time.Hour), first.ID); err != nil {
		t.Fatal(err)
	}
	pruned, err := st.PruneIncidentAttachments(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil || pruned != 2 {
		t.Fatalf("prune incident attachments: count=%d err=%v", pruned, err)
	}
	attachments, err = st.LoadIncidentAttachments(ctx, first.ID)
	if err != nil || len(attachments) != 0 {
		t.Fatalf("expired incident attachment was retained: attachments=%#v err=%v", attachments, err)
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
	projectIncidents, err := st.ListProjectIncidents(ctx, project.ID, 101)
	if err != nil || len(projectIncidents) != 1 || projectIncidents[0].ID != first.ID {
		t.Fatalf("project incident view: items=%#v err=%v", projectIncidents, err)
	}
	projectDeployments, err := st.ListProjectDeployments(ctx, project.ID, 201)
	if err != nil || len(projectDeployments) != 3 || projectDeployments[0].ID != nativeDeployment.ID {
		t.Fatalf("project deployment view: items=%#v err=%v", projectDeployments, err)
	}
	outsideDeployments, err := st.ListProjectDeployments(ctx, "prj_not_this_project", 201)
	if err != nil || len(outsideDeployments) != 0 {
		t.Fatalf("project deployment scope escaped: items=%#v err=%v", outsideDeployments, err)
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
	var openingBody string
	if err := st.db.QueryRowContext(ctx, `SELECT body FROM echo_whatsapp_outbox ORDER BY created_at DESC LIMIT 1`).Scan(&openingBody); err != nil || !strings.Contains(openingBody, `service Web`) || !strings.Contains(openingBody, `investigate Web`) {
		t.Fatalf("alert notification did not guide investigation: body=%q err=%v", openingBody, err)
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
	scopedAlerts, err := st.ListServiceAlertsForService(ctx, service.ID, "open", 1)
	if err != nil || len(scopedAlerts) != 1 || scopedAlerts[0].ServiceID != service.ID || scopedAlerts[0].Status != "open" {
		t.Fatalf("list bounded service alerts: items=%#v err=%v", scopedAlerts, err)
	}
	outsideAlerts, err := st.ListServiceAlertsForService(ctx, "svc_not_this_service", "open", 1)
	if err != nil || len(outsideAlerts) != 0 {
		t.Fatalf("service alert scope escaped: items=%#v err=%v", outsideAlerts, err)
	}
	dashboard, err := st.Dashboard(ctx)
	if err != nil || dashboard.OpenAlerts != 1 || dashboard.PendingWhatsAppMessages != 3 || dashboard.DeadWhatsAppMessages != 0 {
		t.Fatalf("dashboard alert count: item=%#v err=%v", dashboard, err)
	}
	var notifications int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox`).Scan(&notifications); err != nil || notifications != 3 {
		t.Fatalf("alert transitions must enqueue atomically and once: count=%d err=%v", notifications, err)
	}
	watchdog := domain.MonitorEvent{EventID: "monitor-watchdog-later", ServiceID: service.ID, Source: "echo-watchdog", Status: "unhealthy", Severity: "critical", Summary: "observer heartbeat is stale", Fingerprint: "observer-silent", Details: json.RawMessage(`{}`), ObservedAt: observed.Add(3 * time.Minute)}
	if _, err := st.RecordMonitorEvent(ctx, watchdog); err != nil {
		t.Fatal(err)
	}
	latest, err := st.ListLatestMonitorEvents(ctx, project.ID)
	if err != nil || len(latest) != 1 || latest[0].EventID != "monitor-3" {
		t.Fatalf("latest observer evidence should exclude watchdog events: items=%#v err=%v", latest, err)
	}
	outside, err := st.ListLatestMonitorEvents(ctx, "prj_not_this_project")
	if err != nil || len(outside) != 0 {
		t.Fatalf("latest observer evidence escaped project scope: items=%#v err=%v", outside, err)
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

func TestMariaDBServiceCreateAndEditAreUnambiguous(t *testing.T) {
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
	project, err := st.SaveProject(ctx, domain.Project{Key: "services", Name: "Services", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	service, err := st.SaveService(ctx, domain.Service{ProjectID: project.ID, Key: "web", Name: "Web", Kind: "application", Criticality: "medium", Enabled: true, HeartbeatTimeoutSeconds: 120, Metadata: json.RawMessage(`{"owner":"ops"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveService(ctx, domain.Service{ProjectID: project.ID, Key: "web", Name: "Accidental overwrite", Enabled: true, Metadata: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("duplicate service creation unexpectedly succeeded")
	}
	unchanged, err := st.GetService(ctx, service.ID)
	if err != nil || unchanged.Name != "Web" || unchanged.HeartbeatTimeoutSeconds != 120 {
		t.Fatalf("duplicate creation mutated existing service: service=%#v err=%v", unchanged, err)
	}
	service.Name = "Public Web"
	service.Criticality = "high"
	service.HeartbeatTimeoutSeconds = 180
	updated, err := st.SaveService(ctx, service)
	if err != nil || updated.ID != service.ID || updated.Name != "Public Web" || updated.Criticality != "high" || updated.HeartbeatTimeoutSeconds != 180 {
		t.Fatalf("service edit failed: service=%#v err=%v", updated, err)
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

func TestMariaDBExpiredLeasesAreRecoveredOrFinalized(t *testing.T) {
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

	if err := st.EnqueueOutbound(ctx, "owner@s.whatsapp.net", "lease recovery"); err != nil {
		t.Fatal(err)
	}
	first, err := st.ClaimOutbound(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE echo_whatsapp_outbox SET locked_until=DATE_SUB(NOW(6),INTERVAL 1 SECOND) WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := st.ClaimOutbound(ctx, time.Minute)
	if err != nil || first.Attempts != 1 || reclaimed.ID != first.ID || reclaimed.Status != "sending" || reclaimed.Attempts != 2 {
		t.Fatalf("outbox lease was not reclaimed: first=%#v reclaimed=%#v err=%v", first, reclaimed, err)
	}
	if err := st.CompleteOutbound(ctx, first.ID, first.Attempts, "stale-message-id", nil); err != nil {
		t.Fatal(err)
	}
	var leasedStatus, leasedMessageID string
	if err := st.db.QueryRowContext(ctx, `SELECT status,whatsapp_message_id FROM echo_whatsapp_outbox WHERE id=?`, first.ID).Scan(&leasedStatus, &leasedMessageID); err != nil || leasedStatus != "sending" || leasedMessageID != "" {
		t.Fatalf("stale outbox completion changed newer lease: status=%q message=%q err=%v", leasedStatus, leasedMessageID, err)
	}
	if err := st.CompleteOutbound(ctx, reclaimed.ID, reclaimed.Attempts, "current-message-id", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status,whatsapp_message_id FROM echo_whatsapp_outbox WHERE id=?`, first.ID).Scan(&leasedStatus, &leasedMessageID); err != nil || leasedStatus != "sent" || leasedMessageID != "current-message-id" {
		t.Fatalf("current outbox completion was not recorded: status=%q message=%q err=%v", leasedStatus, leasedMessageID, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE echo_whatsapp_outbox SET status='sending',attempts=5,locked_until=DATE_SUB(NOW(6),INTERVAL 1 SECOND) WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimOutbound(ctx, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("final expired outbox lease was returned: %v", err)
	}
	var outboxStatus, outboxError string
	if err := st.db.QueryRowContext(ctx, `SELECT status,last_error FROM echo_whatsapp_outbox WHERE id=?`, first.ID).Scan(&outboxStatus, &outboxError); err != nil || outboxStatus != "dead" || !strings.Contains(outboxError, "lease expired") {
		t.Fatalf("final outbox lease not finalized: status=%q error=%q err=%v", outboxStatus, outboxError, err)
	}

	jobID := id.New("job")
	if _, err := st.db.ExecContext(ctx, `INSERT INTO echo_jobs(id,job_type,payload_json,max_attempts) VALUES(?,?,?,?)`, jobID, "test", `{"incidentId":"inc_lease"}`, 3); err != nil {
		t.Fatal(err)
	}
	firstJob, err := st.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := st.RenewJobLease(ctx, firstJob.ID, firstJob.Attempts, "worker-a", 2*time.Minute)
	if err != nil || !renewed {
		t.Fatalf("current job lease was not renewed: renewed=%v err=%v", renewed, err)
	}
	renewed, err = st.RenewJobLease(ctx, firstJob.ID, firstJob.Attempts, "worker-other", 2*time.Minute)
	if err != nil || renewed {
		t.Fatalf("wrong worker renewed job lease: renewed=%v err=%v", renewed, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE echo_jobs SET locked_until=DATE_SUB(NOW(6),INTERVAL 1 SECOND) WHERE id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	reclaimedJob, err := st.ClaimJob(ctx, "worker-b", time.Minute)
	if err != nil || firstJob.Attempts != 1 || reclaimedJob.ID != firstJob.ID || reclaimedJob.Status != "running" || reclaimedJob.Attempts != 2 || reclaimedJob.LockedBy != "worker-b" {
		t.Fatalf("job lease was not reclaimed: first=%#v reclaimed=%#v err=%v", firstJob, reclaimedJob, err)
	}
	renewed, err = st.RenewJobLease(ctx, firstJob.ID, firstJob.Attempts, "worker-a", 2*time.Minute)
	if err != nil || renewed {
		t.Fatalf("stale job attempt renewed newer lease: renewed=%v err=%v", renewed, err)
	}
	if err := st.CompleteJob(ctx, firstJob.ID, firstJob.Attempts, "worker-a", nil, nil); err != nil {
		t.Fatal(err)
	}
	var leasedJobStatus, leasedJobWorker string
	if err := st.db.QueryRowContext(ctx, `SELECT status,locked_by FROM echo_jobs WHERE id=?`, jobID).Scan(&leasedJobStatus, &leasedJobWorker); err != nil || leasedJobStatus != "running" || leasedJobWorker != "worker-b" {
		t.Fatalf("stale job completion changed newer lease: status=%q worker=%q err=%v", leasedJobStatus, leasedJobWorker, err)
	}
	if err := st.CompleteJob(ctx, reclaimedJob.ID, reclaimedJob.Attempts, "worker-b", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status,locked_by FROM echo_jobs WHERE id=?`, jobID).Scan(&leasedJobStatus, &leasedJobWorker); err != nil || leasedJobStatus != "complete" || leasedJobWorker != "" {
		t.Fatalf("current job completion was not recorded: status=%q worker=%q err=%v", leasedJobStatus, leasedJobWorker, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE echo_jobs SET status='running',attempts=max_attempts,locked_until=DATE_SUB(NOW(6),INTERVAL 1 SECOND) WHERE id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimJob(ctx, "worker-c", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("final expired job lease was returned: %v", err)
	}
	var jobStatus, jobError string
	if err := st.db.QueryRowContext(ctx, `SELECT status,last_error FROM echo_jobs WHERE id=?`, jobID).Scan(&jobStatus, &jobError); err != nil || jobStatus != "dead" || !strings.Contains(jobError, "lease expired") {
		t.Fatalf("final job lease not finalized: status=%q error=%q err=%v", jobStatus, jobError, err)
	}
	jobs, err := st.ListJobs(ctx, "dead", 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != jobID || jobs[0].IncidentID != "inc_lease" || jobs[0].LastError == "" {
		t.Fatalf("dead job was not observable: jobs=%#v err=%v", jobs, err)
	}
	dashboard, err := st.Dashboard(ctx)
	if err != nil || dashboard.DeadJobs != 1 || dashboard.RetryingJobs != 0 || dashboard.RunningJobs != 0 || dashboard.QueuedJobs != 0 {
		t.Fatalf("dashboard job counts are wrong: dashboard=%#v err=%v", dashboard, err)
	}

	notifiedJobID := id.New("job")
	if _, err := st.db.ExecContext(ctx, `INSERT INTO echo_jobs(id,job_type,payload_json,max_attempts) VALUES(?,?,?,?)`, notifiedJobID, "prepare_patch", `{"incidentId":"inc_notified"}`, 1); err != nil {
		t.Fatal(err)
	}
	notifiedJob, err := st.ClaimJob(ctx, "worker-notifier", time.Minute)
	if err != nil || notifiedJob.ID != notifiedJobID {
		t.Fatalf("terminal notification job was not claimed: job=%#v err=%v", notifiedJob, err)
	}
	notification := &domain.OutboundMessage{ChatJID: "owner@s.whatsapp.net", Body: "Safe terminal failure for inc_notified"}
	jobFailure := errors.New("status 403 bearer must-not-leak")
	if err := st.CompleteJob(ctx, notifiedJob.ID, notifiedJob.Attempts, "worker-notifier", jobFailure, notification); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteJob(ctx, notifiedJob.ID, notifiedJob.Attempts, "worker-notifier", jobFailure, notification); err != nil {
		t.Fatal(err)
	}
	var terminalNotifications int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE body=?`, notification.Body).Scan(&terminalNotifications); err != nil || terminalNotifications != 1 {
		t.Fatalf("terminal notification was not atomic and idempotent: count=%d err=%v", terminalNotifications, err)
	}
}

func TestMariaDBWorkerQueueHealthLifecycleIsAtomic(t *testing.T) {
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

	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	jobID := id.New("job")
	if _, err := st.db.ExecContext(ctx, `INSERT INTO echo_jobs(id,job_type,payload_json,available_at,created_at) VALUES(?,?,?,?,?)`, jobID, "analyze_incident", `{"token":"must-not-leak"}`, now.Add(-10*time.Minute), now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	lockedJobID := id.New("job")
	if _, err := st.db.ExecContext(ctx, `INSERT INTO echo_jobs(id,job_type,payload_json,available_at,locked_until,created_at) VALUES(?,?,?,?,?,?)`, lockedJobID, "analyze_incident", `{}`, now.Add(-10*time.Minute), now.Add(time.Hour), now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordWorkerHeartbeat(ctx, "worker-old", now.Add(-8*time.Minute)); err != nil {
		t.Fatal(err)
	}
	health, transition, err := st.ReconcileWorkerQueueHealth(ctx, now, time.Minute, 5*time.Minute, true, "owner@s.whatsapp.net")
	if err != nil || !health.QueueStalled || transition == nil || transition.Kind != "opened" || health.ReadyJobs != 1 {
		t.Fatalf("worker stall did not open: health=%#v transition=%#v err=%v", health, transition, err)
	}
	_, duplicate, err := st.ReconcileWorkerQueueHealth(ctx, now.Add(30*time.Second), time.Minute, 5*time.Minute, true, "owner@s.whatsapp.net")
	if err != nil || duplicate != nil {
		t.Fatalf("unchanged worker stall emitted another transition: transition=%#v err=%v", duplicate, err)
	}
	var notifications int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE chat_jid='owner@s.whatsapp.net'`).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("worker stall notification was not exactly once: count=%d err=%v", notifications, err)
	}
	dashboard, err := st.Dashboard(ctx)
	if err != nil || !dashboard.WorkerQueueStalled || dashboard.OpenAlerts != 1 || dashboard.WorkerLastSeenAt == nil || !strings.Contains(dashboard.WorkerHealthSummary, "oldest waiting") {
		t.Fatalf("worker stall missing from dashboard: dashboard=%#v err=%v", dashboard, err)
	}

	if err := st.RecordWorkerHeartbeat(ctx, "worker-new", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	health, transition, err = st.ReconcileWorkerQueueHealth(ctx, now.Add(time.Minute), time.Minute, 5*time.Minute, true, "owner@s.whatsapp.net")
	if err != nil || health.QueueStalled || transition == nil || transition.Kind != "resolved" {
		t.Fatalf("worker stall did not resolve: health=%#v transition=%#v err=%v", health, transition, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE chat_jid='owner@s.whatsapp.net'`).Scan(&notifications); err != nil || notifications != 2 {
		t.Fatalf("worker recovery notification was not exactly once: count=%d err=%v", notifications, err)
	}
	var leaked int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE body LIKE '%must-not-leak%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("job payload leaked into worker health notification: count=%d err=%v", leaked, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE echo_worker_heartbeats SET last_seen_at=?`, now); err != nil {
		t.Fatal(err)
	}
	_, transition, err = st.ReconcileWorkerQueueHealth(ctx, now.Add(10*time.Minute), time.Minute, 5*time.Minute, true, "owner@s.whatsapp.net")
	if err != nil || transition == nil || transition.Kind != "opened" {
		t.Fatalf("stale worker queue did not reopen: transition=%#v err=%v", transition, err)
	}
	health, transition, err = st.ReconcileWorkerQueueHealth(ctx, now.Add(10*time.Minute+time.Second), time.Minute, 5*time.Minute, false, "owner@s.whatsapp.net")
	if err != nil || health.Status != "disabled" || health.QueueStalled || transition == nil || transition.Kind != "resolved" {
		t.Fatalf("disabling worker did not clear stall policy: health=%#v transition=%#v err=%v", health, transition, err)
	}
}

func cleanupIntegration(t *testing.T, st *Store) {
	t.Helper()
	tables := []string{"echo_audit_events", "echo_whatsapp_outbox", "echo_owner_settings", "echo_whatsapp_contacts", "echo_runtime_component_alerts", "echo_worker_health_alerts", "echo_worker_heartbeats", "echo_jobs", "echo_deployments", "echo_approvals", "echo_change_sets", "echo_analyses", "echo_incident_attachments", "echo_incident_messages", "echo_incidents", "echo_tool_runs", "echo_conversation_messages", "echo_conversations", "echo_service_alerts", "echo_monitor_events", "echo_services", "echo_environments", "echo_repositories", "echo_group_bindings", "echo_projects", "echo_briefing_settings"}
	for _, table := range tables {
		if _, err := st.db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(`INSERT INTO echo_briefing_settings(singleton_id) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
}
