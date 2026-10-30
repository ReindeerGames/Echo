package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/ai"
	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/githubapp"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
	"github.com/ReindeerGames/echo/internal/store"
)

type fakeReader struct {
	dashboard   domain.Dashboard
	projects    []domain.Project
	repos       []domain.Repository
	services    []domain.Service
	alerts      []domain.ServiceAlert
	events      []domain.MonitorEvent
	jobs        []domain.Job
	deployments []domain.Deployment
	changes     []domain.ChangeSet
	incident    domain.Incident
	incidents   []domain.Incident
	analysis    domain.Analysis
}

type fakeToolModel struct{}

type fakeRepositoryInspector struct {
	status githubapp.RepositoryStatus
	repo   domain.Repository
	err    error
}

func (f *fakeRepositoryInspector) InspectRepository(_ context.Context, repo domain.Repository) (githubapp.RepositoryStatus, error) {
	f.repo = repo
	return f.status, f.err
}

func (fakeToolModel) RespondWithTools(ctx context.Context, instructions string, history []ai.ConversationMessage, tools []ai.ToolDefinition, execute ai.ToolExecutor) (string, []ai.ToolExecution, error) {
	if !strings.Contains(instructions, "no shell") || len(history) == 0 {
		return "", nil, errors.New("missing safe investigation context")
	}
	for _, tool := range tools {
		if strings.Contains(tool.Name, "restart") || strings.Contains(tool.Name, "dispatch") || strings.Contains(tool.Name, "shell") || strings.Contains(tool.Name, "merge") || strings.Contains(tool.Name, "approve") || strings.Contains(tool.Name, "write") {
			return "", nil, errors.New("mutation tool exposed")
		}
	}
	arguments := json.RawMessage(`{"service":"nudgie-web"}`)
	output, err := execute(ctx, "get_service", arguments)
	if err != nil {
		return "", nil, err
	}
	if !strings.Contains(output, "Health check failed") {
		return "", nil, errors.New("grounding data missing")
	}
	return "Nudgie web is unhealthy based on its latest Docker observer signal.", []ai.ToolExecution{{CallID: "call-1", Name: "get_service", Arguments: arguments, Output: output}}, nil
}

func (f fakeReader) Dashboard(context.Context) (domain.Dashboard, error)    { return f.dashboard, nil }
func (f fakeReader) ListProjects(context.Context) ([]domain.Project, error) { return f.projects, nil }
func (f fakeReader) ListRepositories(context.Context, string) ([]domain.Repository, error) {
	return f.repos, nil
}
func (f fakeReader) ListServices(context.Context, string) ([]domain.Service, error) {
	return f.services, nil
}
func (f fakeReader) ListServiceAlerts(context.Context, string) ([]domain.ServiceAlert, error) {
	return f.alerts, nil
}
func (f fakeReader) ListMonitorEvents(context.Context, string, int) ([]domain.MonitorEvent, error) {
	return f.events, nil
}
func (f fakeReader) ListLatestMonitorEvents(context.Context, string) ([]domain.MonitorEvent, error) {
	return f.events, nil
}
func (f fakeReader) ListJobs(context.Context, string, int) ([]domain.Job, error) { return f.jobs, nil }
func (f fakeReader) ListDeployments(context.Context, string) ([]domain.Deployment, error) {
	return f.deployments, nil
}
func (f fakeReader) ListProjectDeployments(_ context.Context, projectID string, limit int) ([]domain.Deployment, error) {
	var out []domain.Deployment
	for _, deployment := range f.deployments {
		if deployment.ProjectID == projectID && len(out) < limit {
			out = append(out, deployment)
		}
	}
	return out, nil
}
func (f fakeReader) ListChangeSets(context.Context, string) ([]domain.ChangeSet, error) {
	return f.changes, nil
}
func (f fakeReader) ListIncidents(context.Context, int) ([]domain.Incident, error) {
	return f.incidents, nil
}
func (f fakeReader) ListProjectIncidents(_ context.Context, projectID string, limit int) ([]domain.Incident, error) {
	var out []domain.Incident
	for _, incident := range f.incidents {
		if incident.ProjectID == projectID && len(out) < limit {
			out = append(out, incident)
		}
	}
	return out, nil
}
func (f fakeReader) GetIncident(context.Context, string) (domain.Incident, error) {
	if f.incident.ID == "" {
		return domain.Incident{}, store.ErrNotFound
	}
	return f.incident, nil
}
func (f fakeReader) LatestAnalysis(context.Context, string) (domain.Analysis, error) {
	if f.analysis.ID == "" {
		return domain.Analysis{}, store.ErrNotFound
	}
	return f.analysis, nil
}

func TestClassifyNaturalOperatorQuestions(t *testing.T) {
	tests := map[string]string{
		"anything wrong?":     "alerts",
		"good morning":        "brief",
		"how are things":      "status",
		"service nudgie-web":  "service",
		"what can you do?":    "help",
		"list projects":       "projects",
		"project invoices":    "project",
		"incident inc_123456": "incident",
		"failed jobs":         "jobs",
		"repo invoices":       "repository",
		"what is deployed?":   "deployments",
		"open incidents":      "incidents",
	}
	for input, expected := range tests {
		got, _ := classify(input)
		if got != expected {
			t.Errorf("classify %q = %q, want %q", input, got, expected)
		}
	}
}

func TestStatusAndServiceAnswersUseStoredEvidence(t *testing.T) {
	now := time.Now().UTC()
	reader := fakeReader{
		dashboard: domain.Dashboard{OpenAlerts: 1, OpenIncidents: 2, PendingApprovals: 1, QueuedJobs: 4, RunningJobs: 1, RetryingJobs: 2, DeadJobs: 1, PendingWhatsAppMessages: 3, DeadWhatsAppMessages: 1},
		services:  []domain.Service{{ID: "svc_1", Key: "nudgie-web", Name: "Nudgie web", ProjectName: "Nudgie", Kind: "application", Criticality: "high", Enabled: true}},
		alerts:    []domain.ServiceAlert{{ServiceID: "svc_1", ServiceName: "Nudgie web", Severity: "critical", Summary: "Health check failed"}},
		events:    []domain.MonitorEvent{{ServiceID: "svc_1", ServiceName: "Nudgie web", Status: "unhealthy", Source: "docker", Summary: "Health check failed", ObservedAt: now}},
	}
	responder := New(reader)
	status, intent, err := responder.Answer(context.Background(), "status")
	if err != nil || intent != "status" || !containsAll(status, "1 runtime alert", "Open incidents: 2", "Jobs: 4 queued · 1 running · 2 retrying · 1 dead", "WhatsApp delivery: 3 pending · 1 dead", "needs attention") {
		t.Fatalf("unexpected status answer intent=%s err=%v body=%s", intent, err, status)
	}
	service, intent, err := responder.Answer(context.Background(), "service nudgie-web")
	if err != nil || intent != "service" || !containsAll(service, "Nudgie web", "CRITICAL", "Latest signal: unhealthy") {
		t.Fatalf("unexpected service answer intent=%s err=%v body=%s", intent, err, service)
	}
}

func TestStatusReportsEstateMonitoringCoverage(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		services: []domain.Service{
			{ID: "svc_fresh", Enabled: true, HeartbeatTimeoutSeconds: 120},
			{ID: "svc_stale", Enabled: true, HeartbeatTimeoutSeconds: 60},
			{ID: "svc_missing", Enabled: true, HeartbeatTimeoutSeconds: 60},
			{ID: "svc_no_policy", Enabled: true},
			{ID: "svc_disabled", Enabled: false, HeartbeatTimeoutSeconds: 60},
		},
		events: []domain.MonitorEvent{
			{ServiceID: "svc_fresh", Source: "observer", Status: "healthy", ObservedAt: now.Add(-30 * time.Second)},
			{ServiceID: "svc_stale", Source: "observer", Status: "healthy", ObservedAt: now.Add(-2 * time.Minute)},
		},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "status")
	if err != nil || intent != "status" || !containsAll(answer, "Services: 4 enabled", "Monitoring: 1 fresh · 1 stale · 1 missing · 1 without heartbeat policy") {
		t.Fatalf("unexpected monitoring status intent=%s body=%s err=%v", intent, answer, err)
	}
	output, err := responder.executeTool(context.Background(), "get_status", json.RawMessage(`{}`))
	if err != nil || !containsAll(output, `"monitoring":`, `"fresh":1`, `"stale":1`, `"missing":1`, `"without_heartbeat_policy":1`, `"disabled":1`) {
		t.Fatalf("monitoring coverage missing from status tool: output=%s err=%v", output, err)
	}
}

func TestDailyBriefNamesCurrentAttentionItems(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		dashboard: domain.Dashboard{OpenAlerts: 1, OpenIncidents: 1, FailedDeployments: 1},
		services:  []domain.Service{{ID: "svc_web", ProjectID: "prj_invoices", Name: "Invoice web", Enabled: true, HeartbeatTimeoutSeconds: 60}},
		events:    []domain.MonitorEvent{{ServiceID: "svc_web", Source: "observer", Status: "unhealthy", ObservedAt: now.Add(-30 * time.Second)}},
		alerts:    []domain.ServiceAlert{{ServiceID: "svc_web", ServiceName: "Invoice web", ProjectID: "prj_invoices", Severity: "critical", Summary: "readiness endpoint failed"}},
		incidents: []domain.Incident{
			{ID: "inc_open", ProjectID: "prj_invoices", ProjectName: "Invoices", Status: "triaging", Severity: "high", Summary: "PDFs are delayed", CreatedAt: now.Add(-time.Hour)},
			{ID: "inc_resolved", ProjectID: "prj_invoices", ProjectName: "Invoices", Status: "resolved", Severity: "low", Summary: "old resolved item", CreatedAt: now.Add(-2 * time.Hour)},
		},
		deployments: []domain.Deployment{
			{ID: "dep_failed", ProjectID: "prj_invoices", Environment: "production", Operation: "deploy", Repository: "ReindeerGames/invoices", Status: "failed", CreatedAt: now.Add(-20 * time.Minute)},
			{ID: "dep_verified", ProjectID: "prj_invoices", Environment: "production", Operation: "deploy", Status: "verified", CreatedAt: now.Add(-time.Hour)},
		},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "good morning")
	if err != nil || intent != "brief" || !containsAll(answer, "Echo daily brief", "Monitoring: 1 fresh · 0 stale · 0 missing", "CRITICAL · Invoice web", "TRIAGING · Invoices · inc_open · PDFs are delayed", "FAILED · deploy · production · ReindeerGames/invoices · 20m0s ago") {
		t.Fatalf("unexpected daily brief intent=%s body=%s err=%v", intent, answer, err)
	}
	if strings.Contains(answer, "old resolved item") || strings.Contains(answer, "dep_verified") {
		t.Fatalf("non-actionable history leaked into daily brief: %s", answer)
	}
}

func TestUnknownQuestionExplainsSafeCapabilities(t *testing.T) {
	answer, intent, err := New(fakeReader{}).Answer(context.Background(), "reboot everything")
	if err != nil || intent != "unknown" || !containsAll(answer, "did not understand", "never make a production change") {
		t.Fatalf("unexpected answer intent=%s err=%v body=%s", intent, err, answer)
	}
}

func TestInvestigateUsesOnlyGroundedReadTools(t *testing.T) {
	reader := fakeReader{
		services: []domain.Service{{ID: "svc_1", Key: "nudgie-web", Name: "Nudgie web", ProjectName: "Nudgie", Kind: "application", Criticality: "high", Enabled: true}},
		alerts:   []domain.ServiceAlert{{ServiceID: "svc_1", ServiceName: "Nudgie web", Severity: "critical", Summary: "Health check failed"}},
		events:   []domain.MonitorEvent{{ServiceID: "svc_1", Status: "unhealthy", Source: "docker", Summary: "Health check failed", ObservedAt: time.Now().UTC()}},
	}
	history := []domain.ConversationMessage{{Role: "user", Body: "Is the Nudgie site okay?"}}
	answer, executions, err := New(reader, fakeToolModel{}).Investigate(context.Background(), history)
	if err != nil || len(executions) != 1 || !strings.Contains(answer, "latest Docker observer signal") {
		t.Fatalf("answer=%q executions=%#v err=%v", answer, executions, err)
	}
}

func TestServiceToolProvidesFreshSanitizedOperationalEvidence(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		services: []domain.Service{{ID: "svc_1", Key: "primary-host", Name: "Primary host", ProjectName: "Platform", Kind: "host", Criticality: "critical", Enabled: true, HeartbeatTimeoutSeconds: 60}},
		alerts:   []domain.ServiceAlert{{ServiceID: "svc_1", ServiceName: "Primary host", Fingerprint: "host-resources", Severity: "critical", Summary: "memory threshold exceeded", OpenedAt: now.Add(-time.Minute), LastSeenAt: now.Add(-30 * time.Second)}},
		events: []domain.MonitorEvent{{
			ServiceID: "svc_1", Status: "unhealthy", Severity: "critical", Source: "primary-observer", Summary: "memory threshold exceeded", ObservedAt: now.Add(-90 * time.Second),
			Details: json.RawMessage(`{"snapshot":{"diskUsedPercent":72.5,"memoryUsedPercent":96.2,"load1PerCpu":0.8,"secret":"must-not-leak"},"thresholds":{"memoryCriticalPercent":95},"password":"must-not-leak","failure":"token-must-not-leak"}`),
		}},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	output, err := responder.executeTool(context.Background(), "get_service", json.RawMessage(`{"service":"primary-host"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"age_seconds":90`, `"stale":true`, `"memoryUsedPercent":96.2`, `"memoryCriticalPercent":95`, `"fingerprint":"host-resources"`) {
		t.Fatalf("operational evidence missing: %s", output)
	}
	if strings.Contains(output, "must-not-leak") || strings.Contains(output, "password") || strings.Contains(output, "secret") {
		t.Fatalf("unallowlisted evidence leaked: %s", output)
	}
}

func TestEvidenceSanitizerRejectsOversizedAndTrailingInput(t *testing.T) {
	if got := sanitizedEvidence(json.RawMessage(`{"container":"web"} trailing`)); got != nil {
		t.Fatalf("accepted trailing input: %#v", got)
	}
	oversized := json.RawMessage(`{"failure":"` + strings.Repeat("x", maxEvidenceBytes) + `"}`)
	if got := sanitizedEvidence(oversized); got != nil {
		t.Fatalf("accepted oversized evidence: %#v", got)
	}
}

func TestServiceCommandReportsObserverFreshness(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		services: []domain.Service{{ID: "svc_1", Key: "web", Name: "Web", ProjectName: "Site", Kind: "application", Criticality: "high", Enabled: true, HeartbeatTimeoutSeconds: 60}},
		events:   []domain.MonitorEvent{{ServiceID: "svc_1", Status: "healthy", Source: "observer", Summary: "ready", ObservedAt: now.Add(-90 * time.Second)}},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "service web")
	if err != nil || intent != "service" || !containsAll(answer, "observed 1m30s ago", "Observer evidence: stale", "expected within 1m0s") {
		t.Fatalf("unexpected freshness answer intent=%s err=%v body=%s", intent, err, answer)
	}
}

func TestToolArgumentsRejectTrailingData(t *testing.T) {
	var arguments struct {
		Service string `json:"service"`
	}
	if err := decodeArguments(json.RawMessage(`{"service":"web"}{"service":"other"}`), &arguments); err == nil {
		t.Fatal("trailing tool arguments were accepted")
	}
}

func TestWorkQueueAnswersDoNotExposeRawErrorsOrPayloads(t *testing.T) {
	now := time.Now().UTC()
	reader := fakeReader{jobs: []domain.Job{{ID: "job_1", Type: "prepare_patch", Payload: json.RawMessage(`{"incidentId":"inc_123","token":"must-not-leak"}`), Status: "dead", Attempts: 5, MaxAttempts: 5, LastError: "request status 403 bearer must-not-leak", AvailableAt: now, UpdatedAt: now}}}
	responder := New(reader)
	output, err := responder.executeTool(context.Background(), "get_work_queue", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"incident_id":"inc_123"`, `"error_category":"authorization_failed"`, `"status":"dead"`) || strings.Contains(output, "must-not-leak") || strings.Contains(output, "token") || strings.Contains(output, "bearer") {
		t.Fatalf("unsafe work queue output: %s", output)
	}
	answer, intent, err := responder.Answer(context.Background(), "jobs")
	if err != nil || intent != "jobs" || !containsAll(answer, "DEAD", "prepare patch", "inc_123", "authorization_failed") || strings.Contains(answer, "must-not-leak") {
		t.Fatalf("unexpected deterministic jobs answer intent=%s err=%v body=%s", intent, err, answer)
	}
}

func TestWorkerStallIsVisibleInStatusAndAlerts(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-10 * time.Minute)
	reader := fakeReader{dashboard: domain.Dashboard{OpenAlerts: 1, QueuedJobs: 3, WorkerLastSeenAt: &lastSeen, WorkerQueueStalled: true, WorkerHealthSummary: "3 ready jobs; oldest waiting 12m0s; worker heartbeat last seen 10m0s ago"}}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	status, _, err := responder.Answer(context.Background(), "status")
	if err != nil || !containsAll(status, "Worker: STALLED", "3 ready jobs") {
		t.Fatalf("worker stall missing from status: %s err=%v", status, err)
	}
	alerts, _, err := responder.Answer(context.Background(), "alerts")
	if err != nil || !containsAll(alerts, "Open operational alerts (1)", "CRITICAL · Echo worker", "oldest waiting") {
		t.Fatalf("worker stall missing from alerts: %s err=%v", alerts, err)
	}
	toolOutput, err := responder.executeTool(context.Background(), "get_open_alerts", json.RawMessage(`{}`))
	if err != nil || !containsAll(toolOutput, `"service":"Echo worker"`, `"fingerprint":"worker-queue-stalled"`, `"count":1`) {
		t.Fatalf("worker stall missing from alert tool: %s err=%v", toolOutput, err)
	}
}

func TestRuntimeDegradationIsVisibleToDeterministicAndAIToolAnswers(t *testing.T) {
	state := runtimestate.New()
	state.UpdateComponent("monitor-watchdog", "running", "")
	state.UpdateComponent("worker", "error", "raw detail must not be exposed")
	responder := NewWithRuntime(fakeReader{}, state, []string{"monitor-watchdog", "worker"})

	status, intent, err := responder.Answer(context.Background(), "status")
	if err != nil || intent != "status" || !containsAll(status, "1 runtime alert", "Echo runtime: DEGRADED", "worker error") || strings.Contains(status, "raw detail") {
		t.Fatalf("runtime status missing or unsafe: intent=%s body=%s err=%v", intent, status, err)
	}
	alerts, intent, err := responder.Answer(context.Background(), "alerts")
	if err != nil || intent != "alerts" || !containsAll(alerts, "Open operational alerts (1)", "CRITICAL · Echo component", "worker is error") || strings.Contains(alerts, "raw detail") {
		t.Fatalf("runtime alert missing or unsafe: intent=%s body=%s err=%v", intent, alerts, err)
	}
	toolOutput, err := responder.executeTool(context.Background(), "get_status", json.RawMessage(`{}`))
	if err != nil || !containsAll(toolOutput, `"echo_runtime_ready":false`, `"worker":"error"`) || strings.Contains(toolOutput, "raw detail") {
		t.Fatalf("runtime tool status missing or unsafe: output=%s err=%v", toolOutput, err)
	}
	alertOutput, err := responder.executeTool(context.Background(), "get_open_alerts", json.RawMessage(`{}`))
	if err != nil || !containsAll(alertOutput, `"fingerprint":"runtime-component-worker"`, `"summary":"worker is error"`) || strings.Contains(alertOutput, "raw detail") {
		t.Fatalf("runtime tool alert missing or unsafe: output=%s err=%v", alertOutput, err)
	}
}

func TestRepositoryStatusUsesOnlyConfiguredEnabledRepository(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{repos: []domain.Repository{
		{ID: "repo_enabled", Owner: "ReindeerGames", Name: "invoices", DefaultBranch: "main", Enabled: true},
		{ID: "repo_disabled", Owner: "Other", Name: "invoices", DefaultBranch: "main", Enabled: false},
	}}
	inspector := &fakeRepositoryInspector{status: githubapp.RepositoryStatus{
		Repository: "ReindeerGames/invoices", DefaultBranch: "main", HeadSHA: "a75cdbdaad7917a104299753c32f8358b90a50e6", HeadCommittedAt: now.Add(-time.Hour),
		WorkflowRuns: []githubapp.RepositoryWorkflowRun{{Name: "Build and deploy", Event: "push", Status: "completed", Conclusion: "success", HeadSHA: "a75cdbdaad7917a104299753c32f8358b90a50e6", UpdatedAt: now.Add(-time.Minute)}},
	}}
	responder := NewOperator(reader, nil, nil, inspector)
	responder.now = func() time.Time { return now }

	answer, intent, err := responder.Answer(context.Background(), "repo invoices")
	if err != nil || intent != "repository" || !containsAll(answer, "GitHub repository ReindeerGames/invoices", "head a75cdbda", "SUCCESS · Build and deploy · push", "updated 1m0s ago") {
		t.Fatalf("unexpected repository answer intent=%s body=%s err=%v", intent, answer, err)
	}
	if inspector.repo.ID != "repo_enabled" {
		t.Fatalf("inspected repository outside enabled scope: %#v", inspector.repo)
	}
	output, err := responder.executeTool(context.Background(), "get_repository_status", json.RawMessage(`{"repository":"ReindeerGames/invoices"}`))
	if err != nil || !containsAll(output, `"available":true`, `"conclusion":"success"`, `"head_sha":"a75cdbdaad7917a104299753c32f8358b90a50e6"`) {
		t.Fatalf("unexpected repository tool output=%s err=%v", output, err)
	}
}

func TestRepositoryStatusReportsAmbiguousConfiguredMatchesWithoutGitHubCall(t *testing.T) {
	reader := fakeReader{repos: []domain.Repository{
		{ID: "repo_1", Owner: "One", Name: "site", Enabled: true},
		{ID: "repo_2", Owner: "Two", Name: "site", Enabled: true},
	}}
	inspector := &fakeRepositoryInspector{}
	answer, intent, err := NewOperator(reader, nil, nil, inspector).Answer(context.Background(), "repo site")
	if err != nil || intent != "repository" || !containsAll(answer, "ambiguous", "One/site", "Two/site") || inspector.repo.ID != "" {
		t.Fatalf("unexpected ambiguous answer intent=%s body=%s repo=%#v err=%v", intent, answer, inspector.repo, err)
	}
}

func TestDeploymentAnswersExposeBoundedVerificationState(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{deployments: []domain.Deployment{
		{ID: "dep_verified", IncidentID: "inc_1", ProjectID: "prj_1", Environment: "production", Operation: "deploy", ArtifactRef: "a75cdbdaad7917a104299753c32f8358b90a50e6", Status: "verified", Repository: "ReindeerGames/invoices", Workflow: "deploy.yml", ExternalRunID: 42, CreatedAt: now.Add(-time.Hour)},
		{ID: "dep_failed", IncidentID: "inc_2", ProjectID: "prj_1", Environment: "staging", Operation: "rollback", ArtifactRef: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Status: "failed", Repository: "ReindeerGames/invoices", WorkflowURL: "https://example.test/private", CreatedAt: now.Add(-2 * time.Hour)},
	}}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "deployments")
	if err != nil || intent != "deployments" || !containsAll(answer, "Recent Echo deployments (2)", "VERIFIED · deploy · production", "FAILED · rollback · staging", "a75cdbdaad79") {
		t.Fatalf("unexpected deployment answer intent=%s body=%s err=%v", intent, answer, err)
	}
	output, err := responder.executeTool(context.Background(), "get_deployments", json.RawMessage(`{"status":"failed"}`))
	if err != nil || !containsAll(output, `"count":1`, `"id":"dep_failed"`, `"status":"failed"`) || strings.Contains(output, "private") || strings.Contains(output, "dep_verified") {
		t.Fatalf("unexpected deployment tool output=%s err=%v", output, err)
	}
}

func TestIncidentAnswersIncludeChangeAndDeploymentLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		incident:    domain.Incident{ID: "inc_123", ProjectName: "Invoices", Status: "resolved", Severity: "high", Summary: "Invoices were blank", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		analysis:    domain.Analysis{ID: "ana_1", IncidentID: "inc_123", Status: "complete", Confidence: "high", Summary: "Template regression", CreatedAt: now.Add(-90 * time.Minute)},
		changes:     []domain.ChangeSet{{ID: "chg_1", IncidentID: "inc_123", RepositoryID: "repo_1", BranchName: "echo/inc-123", CommitSHA: "a75cdbdaad7917a104299753c32f8358b90a50e6", PullRequestNumber: 17, ArtifactRef: "a75cdbdaad7917a104299753c32f8358b90a50e6", Status: "merged", CreatedAt: now.Add(-time.Hour), UpdatedAt: now}},
		deployments: []domain.Deployment{{ID: "dep_1", IncidentID: "inc_123", ProjectID: "prj_1", Environment: "production", Operation: "deploy", ArtifactRef: "a75cdbdaad7917a104299753c32f8358b90a50e6", Status: "verified", CreatedAt: now.Add(-30 * time.Minute)}},
	}
	responder := New(reader)
	answer, intent, err := responder.Answer(context.Background(), "incident inc_123")
	if err != nil || intent != "incident" || !containsAll(answer, "Analysis (high confidence): Template regression", "Latest change: merged · PR #17 · a75cdbda", "Latest deployment: production · deploy · verified") {
		t.Fatalf("unexpected incident answer intent=%s body=%s err=%v", intent, answer, err)
	}
	output, err := responder.executeTool(context.Background(), "get_incident", json.RawMessage(`{"incident_id":"inc_123"}`))
	if err != nil || !containsAll(output, `"pull_request_number":17`, `"commit_sha":"a75cdbdaad7917a104299753c32f8358b90a50e6"`, `"environment":"production"`, `"status":"verified"`) || strings.Contains(output, "pullRequestUrl") {
		t.Fatalf("unexpected incident tool output=%s err=%v", output, err)
	}
}

func TestIncidentFinderIsBoundedAndSearchableWithoutSensitiveSourceFields(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{incidents: []domain.Incident{
		{ID: "inc_open", ProjectName: "Invoices", Status: "triaging", Severity: "high", Summary: "Blank invoice PDFs", SourceBody: "secret raw report", ReporterJID: "27820000000@s.whatsapp.net", CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		{ID: "inc_done", ProjectName: "Website", Status: "resolved", Severity: "low", Summary: "Footer typo", CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-20 * time.Hour)},
	}}
	responder := New(reader)
	answer, intent, err := responder.Answer(context.Background(), "incidents")
	if err != nil || intent != "incidents" || !containsAll(answer, "Recent incidents (2)", "TRIAGING · inc_open · Invoices · HIGH", "Blank invoice PDFs", "RESOLVED · inc_done") || strings.Contains(answer, "secret raw report") {
		t.Fatalf("unexpected incident finder answer intent=%s body=%s err=%v", intent, answer, err)
	}
	output, err := responder.executeTool(context.Background(), "list_incidents", json.RawMessage(`{"query":"invoice","status":"triaging"}`))
	if err != nil || !containsAll(output, `"count":1`, `"id":"inc_open"`, `"summary":"Blank invoice PDFs"`) || strings.Contains(output, "secret raw report") || strings.Contains(output, "27820000000") || strings.Contains(output, "inc_done") {
		t.Fatalf("unexpected incident finder tool output=%s err=%v", output, err)
	}
}

func TestProjectOverviewJoinsOperationalEvidenceWithoutCrossProjectData(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		projects: []domain.Project{
			{ID: "prj_invoices", Key: "invoices", Name: "Invoices", Description: "Invoice generation and delivery", Enabled: true},
			{ID: "prj_other", Key: "other", Name: "Other", Enabled: true},
		},
		repos: []domain.Repository{
			{ID: "repo_1", ProjectID: "prj_invoices", Owner: "ReindeerGames", Name: "invoices", Role: "application", DefaultBranch: "main", Enabled: true},
			{ID: "repo_other", ProjectID: "prj_other", Owner: "ReindeerGames", Name: "other", Enabled: true},
		},
		services: []domain.Service{
			{ID: "svc_fresh", ProjectID: "prj_invoices", Key: "invoice-web", Name: "Invoice web", Kind: "application", Criticality: "high", Enabled: true, HeartbeatTimeoutSeconds: 120},
			{ID: "svc_stale", ProjectID: "prj_invoices", Key: "invoice-worker", Name: "Invoice worker", Kind: "worker", Criticality: "critical", Enabled: true, HeartbeatTimeoutSeconds: 60},
			{ID: "svc_missing", ProjectID: "prj_invoices", Key: "invoice-cron", Name: "Invoice cron", Kind: "worker", Criticality: "medium", Enabled: true, HeartbeatTimeoutSeconds: 60},
			{ID: "svc_untracked", ProjectID: "prj_invoices", Key: "invoice-admin", Name: "Invoice admin", Kind: "application", Criticality: "low", Enabled: true},
			{ID: "svc_disabled", ProjectID: "prj_invoices", Key: "invoice-old", Name: "Invoice old", Kind: "application", Criticality: "low", Enabled: false, HeartbeatTimeoutSeconds: 60},
			{ID: "svc_other", ProjectID: "prj_other", Key: "other", Name: "Other", Enabled: true, HeartbeatTimeoutSeconds: 60},
		},
		events: []domain.MonitorEvent{
			{EventID: "evt_fresh", ServiceID: "svc_fresh", Source: "observer", Status: "healthy", Severity: "info", Summary: "ready", ObservedAt: now.Add(-30 * time.Second)},
			{EventID: "evt_stale", ServiceID: "svc_stale", Source: "observer", Status: "degraded", Severity: "warning", Summary: "queue depth elevated", ObservedAt: now.Add(-2 * time.Minute)},
			{EventID: "evt_other", ServiceID: "svc_other", Source: "observer", Status: "unhealthy", Severity: "critical", Summary: "must not appear", ObservedAt: now},
		},
		alerts: []domain.ServiceAlert{
			{ServiceID: "svc_stale", ServiceName: "Invoice worker", ProjectID: "prj_invoices", Severity: "warning", Fingerprint: "queue-depth", Summary: "queue depth elevated", OpenedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-2 * time.Minute)},
			{ServiceID: "svc_other", ServiceName: "Other", ProjectID: "prj_other", Severity: "critical", Summary: "must not appear"},
		},
		incidents: []domain.Incident{
			{ID: "inc_invoice", ProjectID: "prj_invoices", ProjectName: "Invoices", Status: "triaging", Severity: "high", Summary: "PDFs delayed", CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
			{ID: "inc_other", ProjectID: "prj_other", ProjectName: "Other", Status: "failed", Severity: "critical", Summary: "must not appear", CreatedAt: now},
		},
		deployments: []domain.Deployment{
			{ID: "dep_invoice", IncidentID: "inc_invoice", ProjectID: "prj_invoices", Environment: "production", Operation: "deploy", ArtifactRef: "a75cdbdaad7917a104299753c32f8358b90a50e6", Status: "verified", Repository: "ReindeerGames/invoices", CreatedAt: now.Add(-45 * time.Minute)},
			{ID: "dep_other", ProjectID: "prj_other", Environment: "production", Operation: "deploy", Status: "failed", CreatedAt: now},
		},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "project invoices")
	if err != nil || intent != "project" || !containsAll(answer, "Project: Invoices (invoices)", "Repositories: 1/1 enabled · ReindeerGames/invoices", "Services: 4/5 enabled", "Monitoring: 1 fresh · 1 stale · 1 missing · 1 without heartbeat policy · 1 disabled", "Open alerts: 1", "PDFs delayed", "Latest deployment: VERIFIED · deploy · production · 45m0s ago") {
		t.Fatalf("unexpected project answer intent=%s body=%s err=%v", intent, answer, err)
	}
	if strings.Contains(answer, "must not appear") {
		t.Fatalf("cross-project data leaked into answer: %s", answer)
	}
	output, err := responder.executeTool(context.Background(), "get_project", json.RawMessage(`{"project":"Invoices"}`))
	if err != nil || !containsAll(output, `"found":true`, `"fresh":1`, `"stale":1`, `"missing":1`, `"without_heartbeat_policy":1`, `"open_alert_count":1`, `"id":"inc_invoice"`, `"id":"dep_invoice"`) {
		t.Fatalf("unexpected project tool output=%s err=%v", output, err)
	}
	if strings.Contains(output, "must not appear") || strings.Contains(output, "inc_other") || strings.Contains(output, "dep_other") || strings.Contains(output, "repo_other") {
		t.Fatalf("cross-project data leaked into tool output: %s", output)
	}
}

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
