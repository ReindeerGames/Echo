package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/ai"
	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/githubapp"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
	"github.com/ReindeerGames/echo/internal/store"
)

type fakeReader struct {
	dashboard    domain.Dashboard
	projects     []domain.Project
	groups       []domain.GroupBinding
	repos        []domain.Repository
	services     []domain.Service
	dependencies []domain.ServiceDependency
	alerts       []domain.ServiceAlert
	maintenance  []domain.MaintenanceWindow
	events       []domain.MonitorEvent
	jobs         []domain.Job
	deliveries   []domain.OutboundDelivery
	deployments  []domain.Deployment
	changes      []domain.ChangeSet
	incident     domain.Incident
	incidents    []domain.Incident
	analysis     domain.Analysis
	audit        []domain.AuditEvent
	attachments  []domain.IncidentAttachment
	messages     []domain.IncidentMessage
}

type fakeToolModel struct{}
type serviceInvestigationToolModel struct{}

type fakeRepositoryInspector struct {
	status githubapp.RepositoryStatus
	repo   domain.Repository
	err    error
}

type fakeRepositorySearchInspector struct {
	fakeRepositoryInspector
	searchResult githubapp.RepositorySearchResult
	searchRepo   domain.Repository
	searchQuery  string
	searchErr    error
}

type fakeRepositoryCatalogInspector struct {
	fakeRepositoryInspector
	discovery githubapp.RepositoryDiscovery
	err       error
}

type estateRepositoryInspector struct {
	mu       sync.Mutex
	statuses map[string]githubapp.RepositoryStatus
	errors   map[string]error
	calls    []string
}

func (f *estateRepositoryInspector) InspectRepository(_ context.Context, repo domain.Repository) (githubapp.RepositoryStatus, error) {
	name := repo.Owner + "/" + repo.Name
	f.mu.Lock()
	f.calls = append(f.calls, name)
	status, err := f.statuses[name], f.errors[name]
	f.mu.Unlock()
	return status, err
}

func (f *fakeRepositoryInspector) InspectRepository(_ context.Context, repo domain.Repository) (githubapp.RepositoryStatus, error) {
	f.repo = repo
	return f.status, f.err
}

func (f *fakeRepositorySearchInspector) SearchRepository(_ context.Context, repo domain.Repository, query string) (githubapp.RepositorySearchResult, error) {
	f.searchRepo = repo
	f.searchQuery = query
	return f.searchResult, f.searchErr
}

func (f *fakeRepositoryCatalogInspector) ListAccessibleRepositories(context.Context) (githubapp.RepositoryDiscovery, error) {
	return f.discovery, f.err
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

func (serviceInvestigationToolModel) RespondWithTools(ctx context.Context, instructions string, history []ai.ConversationMessage, tools []ai.ToolDefinition, execute ai.ToolExecutor) (string, []ai.ToolExecution, error) {
	if !strings.Contains(instructions, "Use investigate_service") || len(history) == 0 {
		return "", nil, errors.New("missing service investigation guidance")
	}
	found := false
	for _, tool := range tools {
		if tool.Name == "investigate_service" {
			found = true
			break
		}
	}
	if !found {
		return "", nil, errors.New("service investigation tool is unavailable")
	}
	arguments := json.RawMessage(`{"service":"invoices-api"}`)
	output, err := execute(ctx, "investigate_service", arguments)
	if err != nil {
		return "", nil, err
	}
	if !containsAll(output, `"name":"Invoices API"`, `"limitations":["no logs","no shell","no SSH","no source download","no mutation"]`) {
		return "", nil, errors.New("correlated investigation evidence missing")
	}
	return "Invoices API is unhealthy; the available evidence is correlated but does not prove a cause.", []ai.ToolExecution{{CallID: "call-investigate", Name: "investigate_service", Arguments: arguments, Output: output}}, nil
}

func (f fakeReader) Dashboard(context.Context) (domain.Dashboard, error)    { return f.dashboard, nil }
func (f fakeReader) ListProjects(context.Context) ([]domain.Project, error) { return f.projects, nil }
func (f fakeReader) ListGroupBindings(context.Context) ([]domain.GroupBinding, error) {
	return f.groups, nil
}
func (f fakeReader) ListRepositories(context.Context, string) ([]domain.Repository, error) {
	return f.repos, nil
}
func (f fakeReader) ListServices(context.Context, string) ([]domain.Service, error) {
	return f.services, nil
}
func (f fakeReader) ListServiceDependencies(_ context.Context, serviceID string, limit int) ([]domain.ServiceDependency, error) {
	var out []domain.ServiceDependency
	for _, dependency := range f.dependencies {
		if serviceID != "" && dependency.ServiceID != serviceID && dependency.DependsOnServiceID != serviceID {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, dependency)
	}
	return out, nil
}
func (f fakeReader) ListServiceAlerts(context.Context, string) ([]domain.ServiceAlert, error) {
	return f.alerts, nil
}
func (f fakeReader) ListServiceAlertsForService(_ context.Context, serviceID, status string, limit int) ([]domain.ServiceAlert, error) {
	var out []domain.ServiceAlert
	for _, alert := range f.alerts {
		if alert.ServiceID != serviceID || (status != "" && alert.Status != "" && alert.Status != status) {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, alert)
	}
	return out, nil
}
func (f fakeReader) ListMaintenanceWindows(_ context.Context, serviceID string, limit int) ([]domain.MaintenanceWindow, error) {
	var out []domain.MaintenanceWindow
	for _, window := range f.maintenance {
		if serviceID != "" && window.ServiceID != serviceID {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, window)
	}
	return out, nil
}
func (f fakeReader) ListMonitorEvents(context.Context, string, int) ([]domain.MonitorEvent, error) {
	return f.events, nil
}
func (f fakeReader) ListLatestMonitorEvents(context.Context, string) ([]domain.MonitorEvent, error) {
	return f.events, nil
}
func (f fakeReader) ListJobs(context.Context, string, int) ([]domain.Job, error) { return f.jobs, nil }
func (f fakeReader) ListOutboundDeliveries(context.Context, string, int) ([]domain.OutboundDelivery, error) {
	return f.deliveries, nil
}
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
func (f fakeReader) ListAudit(context.Context, int) ([]domain.AuditEvent, error) {
	return f.audit, nil
}
func (f fakeReader) ListIncidentAttachments(context.Context, string) ([]domain.IncidentAttachment, error) {
	return f.attachments, nil
}
func (f fakeReader) ListIncidentMessages(context.Context, string, int) ([]domain.IncidentMessage, error) {
	return f.messages, nil
}

func TestClassifyNaturalOperatorQuestions(t *testing.T) {
	tests := map[string]string{
		"anything wrong?":                     "alerts",
		"good morning":                        "brief",
		"github status":                       "repository_overview",
		"github inventory":                    "unknown",
		"what isn't monitored?":               "coverage",
		"what happened?":                      "activity",
		"planned maintenance":                 "maintenance",
		"how are things":                      "status",
		"service nudgie-web":                  "service",
		"what can you do?":                    "help",
		"list projects":                       "projects",
		"project invoices":                    "project",
		"incident inc_123456":                 "incident",
		"failed jobs":                         "jobs",
		"why didn't WhatsApp send?":           "deliveries",
		"why didn't the WhatsApp alert send?": "deliveries",
		"WhatsApp queue":                      "deliveries",
		"outbox":                              "deliveries",
		"repo invoices":                       "repository",
		"repo invoices how does authentication work": "unknown",
		"what is deployed?":                          "deployments",
		"open incidents":                             "incidents",
	}
	for input, expected := range tests {
		got, _ := classify(input)
		if got != expected {
			t.Errorf("classify %q = %q, want %q", input, got, expected)
		}
	}
}

func TestEstateCoverageFindsRelationshipAndMonitoringGaps(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		projects: []domain.Project{
			{ID: "prj_ready", Key: "ready", Name: "Ready", Enabled: true},
			{ID: "prj_gaps", Key: "gaps", Name: "Gaps", Enabled: true},
			{ID: "prj_disabled", Key: "disabled", Name: "Disabled", Enabled: false},
		},
		repos: []domain.Repository{
			{ID: "repo_ready", ProjectID: "prj_ready", Enabled: true},
			{ID: "repo_disabled", ProjectID: "prj_gaps", Enabled: false},
		},
		groups: []domain.GroupBinding{
			{ID: "grp_ready", ProjectID: "prj_ready", GroupName: "Secret client room", GroupJID: "120363000000@g.us", Enabled: true},
			{ID: "grp_disabled", ProjectID: "prj_gaps", Enabled: false},
		},
		services: []domain.Service{
			{ID: "svc_ready", ProjectID: "prj_ready", Enabled: true, HeartbeatTimeoutSeconds: 120},
			{ID: "svc_stale", ProjectID: "prj_gaps", Enabled: true, HeartbeatTimeoutSeconds: 60},
			{ID: "svc_missing", ProjectID: "prj_gaps", Enabled: true, HeartbeatTimeoutSeconds: 60},
			{ID: "svc_no_policy", ProjectID: "prj_gaps", Enabled: true},
		},
		events: []domain.MonitorEvent{
			{ServiceID: "svc_ready", Source: "observer", Status: "healthy", ObservedAt: now.Add(-time.Minute)},
			{ServiceID: "svc_stale", Source: "observer", Status: "healthy", ObservedAt: now.Add(-2 * time.Minute)},
		},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }

	answer, intent, err := responder.Answer(context.Background(), "what isn't monitored?")
	if err != nil || intent != "coverage" || !containsAll(answer, "Estate coverage · 2 enabled project(s) · 1 with gaps", "READY · Ready (ready) · 1 repo(s) · 1 group(s) · 1 service(s) · 1 fresh", "GAPS · Gaps (gaps) · no enabled repository; no enabled WhatsApp group; 1 stale service; 1 service without evidence; 1 service without heartbeat policy") {
		t.Fatalf("unexpected coverage answer intent=%s body=%s err=%v", intent, answer, err)
	}
	if strings.Contains(answer, "Disabled") {
		t.Fatalf("disabled project leaked into coverage answer: %s", answer)
	}

	output, err := responder.executeTool(context.Background(), "get_estate_coverage", json.RawMessage(`{}`))
	if err != nil || !containsAll(output, `"enabled_project_count":2`, `"projects_with_gaps":1`, `"project":"Gaps"`, `"stale":1`, `"missing":1`, `"without_heartbeat_policy":1`, `"no enabled repository"`) || strings.Contains(output, `"Disabled"`) || strings.Contains(output, "Secret client room") || strings.Contains(output, "120363000000") {
		t.Fatalf("unexpected coverage tool output=%s err=%v", output, err)
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

func TestRecentActivityOmitsActorsMetadataAndAssistantNoise(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{audit: []domain.AuditEvent{
		{
			Action:       "assistant.query",
			ResourceType: "whatsapp_message",
			ResourceID:   "msg_secret",
			Outcome:      "succeeded",
			ActorType:    "whatsapp_contact",
			ActorID:      "27820000000@s.whatsapp.net",
			Metadata:     json.RawMessage(`{"body":"private operator question"}`),
			CreatedAt:    now.Add(-time.Minute),
		},
		{
			Action:       "deployment.verified",
			ResourceType: "deployment",
			ResourceID:   "dep_1",
			Outcome:      "succeeded",
			ActorType:    "system",
			ActorID:      "must-not-leak-actor",
			Metadata:     json.RawMessage(`{"token":"must-not-leak-token"}`),
			CreatedAt:    now.Add(-2 * time.Minute),
		},
		{
			Action:       "owner.selected",
			ResourceType: "whatsapp_contact",
			ResourceID:   "ctc_1",
			Outcome:      "succeeded",
			CreatedAt:    now.Add(-3 * time.Minute),
		},
	}}
	responder := New(reader)
	responder.now = func() time.Time { return now }

	answer, intent, err := responder.Answer(context.Background(), "what happened?")
	if err != nil || intent != "activity" || !containsAll(answer, "Recent operational activity (2)", "SUCCEEDED · deployment verified · deployment dep_1 · 2m0s ago", "SUCCEEDED · owner selected · whatsapp_contact ctc_1 · 3m0s ago") {
		t.Fatalf("unexpected activity answer intent=%s body=%s err=%v", intent, answer, err)
	}
	for _, secret := range []string{"assistant query", "27820000000", "private operator question", "must-not-leak-actor", "must-not-leak-token"} {
		if strings.Contains(answer, secret) {
			t.Fatalf("unsafe value %q leaked into activity answer: %s", secret, answer)
		}
	}

	output, err := responder.executeTool(context.Background(), "get_recent_activity", json.RawMessage(`{}`))
	if err != nil || !containsAll(output, `"count":2`, `"action":"deployment.verified"`, `"resource_id":"dep_1"`, `"action":"owner.selected"`) {
		t.Fatalf("unexpected activity tool output=%s err=%v", output, err)
	}
	for _, unsafe := range []string{"assistant.query", "actor_type", "actor_id", "metadata", "27820000000", "private operator question", "must-not-leak-actor", "must-not-leak-token"} {
		if strings.Contains(output, unsafe) {
			t.Fatalf("unsafe value %q leaked into activity tool output: %s", unsafe, output)
		}
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

func TestDailyBriefIncludesBoundedGitHubWorkflowAttention(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{repos: []domain.Repository{{ID: "repo_failed", Owner: "ReindeerGames", Name: "invoices", DefaultBranch: "main", Enabled: true}}}
	inspector := &estateRepositoryInspector{statuses: map[string]githubapp.RepositoryStatus{
		"ReindeerGames/invoices": {Repository: "ReindeerGames/invoices", DefaultBranch: "main", HeadSHA: strings.Repeat("a", 40), HeadCommittedAt: now, WorkflowRuns: []githubapp.RepositoryWorkflowRun{{Name: "Deploy", Status: "completed", Conclusion: "failure", UpdatedAt: now}}},
	}, errors: map[string]error{}}
	responder := NewOperator(reader, nil, nil, inspector)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "daily brief")
	if err != nil || intent != "brief" || !containsAll(answer, "GitHub workflows: 0 healthy · 0 running · 1 attention · 0 unavailable", "ATTENTION · ReindeerGames/invoices · 1 latest workflow(s) failed") {
		t.Fatalf("unexpected GitHub daily brief intent=%s body=%s err=%v", intent, answer, err)
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
			Details: json.RawMessage(`{"port":3306,"host":"db.internal.example","tls":{"expiresAt":"2026-11-05T12:00:00Z","daysRemaining":6,"issuer":"must-not-leak"},"modifiedAt":"2026-10-29T10:00:00Z","ageSeconds":93600,"matchedFiles":2,"snapshot":{"diskUsedPercent":72.5,"memoryUsedPercent":96.2,"load1PerCpu":0.8,"secret":"must-not-leak"},"thresholds":{"memoryCriticalPercent":95,"tlsWarningDays":30,"tlsCriticalDays":7,"warningAgeSeconds":93600,"criticalAgeSeconds":180000},"password":"must-not-leak","failure":"token-must-not-leak"}`),
		}},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	output, err := responder.executeTool(context.Background(), "get_service", json.RawMessage(`{"service":"primary-host"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"age_seconds":90`, `"stale":true`, `"port":3306`, `"memoryUsedPercent":96.2`, `"memoryCriticalPercent":95`, `"expiresAt":"2026-11-05T12:00:00Z"`, `"daysRemaining":6`, `"tlsWarningDays":30`, `"tlsCriticalDays":7`, `"modifiedAt":"2026-10-29T10:00:00Z"`, `"matchedFiles":2`, `"warningAgeSeconds":93600`, `"criticalAgeSeconds":180000`, `"fingerprint":"host-resources"`) {
		t.Fatalf("operational evidence missing: %s", output)
	}
	if strings.Contains(output, "must-not-leak") || strings.Contains(output, "password") || strings.Contains(output, "secret") || strings.Contains(output, "db.internal.example") {
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

func TestDeliveryQueueAnswersExposeMechanicsWithoutMessageData(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{deliveries: []domain.OutboundDelivery{{ChannelType: "direct", Status: "failed", Attempts: 2, MaxAttempts: 5, AvailableAt: now.Add(time.Minute), FailureCategory: "authorization_failed", CreatedAt: now.Add(-time.Minute)}}}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	output, err := responder.executeTool(context.Background(), "get_delivery_queue", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"channel_type":"direct"`, `"status":"failed"`, `"failure_category":"authorization_failed"`, `"privacy":"message content, destinations, WhatsApp IDs, and raw errors omitted"`) {
		t.Fatalf("unsafe delivery queue output: %s", output)
	}
	answer, intent, err := responder.Answer(context.Background(), "why didn't WhatsApp send?")
	if err != nil || intent != "deliveries" || !containsAll(answer, "WhatsApp delivery queue (1 active)", "RETRYING", "direct", "attempt 2/5", "next attempt in 1m0s", "authorization_failed", "message content and destinations are omitted") {
		t.Fatalf("unexpected delivery answer intent=%s err=%v body=%s", intent, err, answer)
	}
}

func TestMaintenanceIsVisibleWithoutExposingMutationTools(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	reader := fakeReader{
		maintenance: []domain.MaintenanceWindow{{ID: "mnt_secret", ServiceID: "svc_1", ServiceName: "Invoices API", ProjectID: "prj_1", StartsAt: now.Add(-time.Minute), EndsAt: until, Reason: "Planned database upgrade"}},
		alerts:      []domain.ServiceAlert{{ServiceID: "svc_1", ServiceName: "Invoices API", Severity: "critical", Summary: "Readiness unavailable", Fingerprint: "readiness", MaintenanceWindowID: "mnt_secret", NotificationSuppressedUntil: &until}},
	}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	output, err := responder.executeTool(context.Background(), "get_maintenance_windows", json.RawMessage(`{}`))
	if err != nil || !containsAll(output, `"service":"Invoices API"`, `"state":"active"`, `"reason":"Planned database upgrade"`, `"behavior":"monitoring evidence continues; alert notifications are suppressed only during the bounded window"`) || strings.Contains(output, "mnt_secret") {
		t.Fatalf("unsafe maintenance tool output: %s err=%v", output, err)
	}
	answer, intent, err := responder.Answer(context.Background(), "planned maintenance")
	if err != nil || intent != "maintenance" || !containsAll(answer, "Service maintenance (1)", "ACTIVE · Invoices API", "Planned database upgrade", "continues recording evidence") || strings.Contains(answer, "mnt_secret") {
		t.Fatalf("unexpected maintenance answer intent=%s err=%v body=%s", intent, err, answer)
	}
	alerts, intent, err := responder.Answer(context.Background(), "alerts")
	if err != nil || intent != "alerts" || !containsAll(alerts, "MAINTENANCE until 2026-10-30 13:00 UTC", "Readiness unavailable") {
		t.Fatalf("maintenance suppression missing from alerts intent=%s err=%v body=%s", intent, err, alerts)
	}
	for _, tool := range operatorTools() {
		if strings.Contains(tool.Name, "schedule") || strings.Contains(tool.Name, "cancel") || strings.Contains(tool.Name, "suppress") {
			t.Fatalf("maintenance mutation tool exposed: %s", tool.Name)
		}
	}
}

func TestCriticalAlertsExposeExactAcknowledgementCodeAsReadEvidence(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{alerts: []domain.ServiceAlert{{ID: "alt_0123456789abcdef01234567", ServiceID: "svc_1", ServiceName: "Invoices API", Status: "open", Severity: "critical", Summary: "Readiness unavailable", Fingerprint: "readiness", OpenedAt: now.Add(-time.Hour), LastSeenAt: now}}}
	responder := New(reader)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "alerts")
	if err != nil || intent != "alerts" || !containsAll(answer, "ack cdef01234567", "pause bounded reminders") {
		t.Fatalf("acknowledgement guidance missing intent=%s err=%v body=%s", intent, err, answer)
	}
	output, err := responder.executeTool(context.Background(), "get_open_alerts", json.RawMessage(`{}`))
	if err != nil || !containsAll(output, `"acknowledgement_code":"cdef01234567"`, `"reminder_count":0`) || strings.Contains(output, "alt_0123456789abcdef01234567") {
		t.Fatalf("unsafe acknowledgement tool view: output=%s err=%v", output, err)
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
		RecentCommits: []githubapp.RepositoryCommit{{SHA: "a75cdbdaad7917a104299753c32f8358b90a50e6", Summary: "Fix invoice checkout timeout", CommittedAt: now.Add(-time.Hour)}},
		WorkflowRuns:  []githubapp.RepositoryWorkflowRun{{Name: "Build and deploy", Event: "push", Status: "completed", Conclusion: "success", HeadSHA: "a75cdbdaad7917a104299753c32f8358b90a50e6", UpdatedAt: now.Add(-time.Minute)}},
	}}
	responder := NewOperator(reader, nil, nil, inspector)
	responder.now = func() time.Time { return now }

	answer, intent, err := responder.Answer(context.Background(), "repo invoices")
	if err != nil || intent != "repository" || !containsAll(answer, "GitHub repository ReindeerGames/invoices", "head a75cdbda", "SUCCESS · Build and deploy · push", "updated 1m0s ago", "Recent default-branch changes", "Fix invoice checkout timeout") {
		t.Fatalf("unexpected repository answer intent=%s body=%s err=%v", intent, answer, err)
	}
	if inspector.repo.ID != "repo_enabled" {
		t.Fatalf("inspected repository outside enabled scope: %#v", inspector.repo)
	}
	output, err := responder.executeTool(context.Background(), "get_repository_status", json.RawMessage(`{"repository":"ReindeerGames/invoices"}`))
	if err != nil || !containsAll(output, `"available":true`, `"conclusion":"success"`, `"head_sha":"a75cdbdaad7917a104299753c32f8358b90a50e6"`, `"summary":"Fix invoice checkout timeout"`, `"commits_truncated":false`) {
		t.Fatalf("unexpected repository tool output=%s err=%v", output, err)
	}
}

func TestRepositoryCommitViewsAreBoundedAndSingleLine(t *testing.T) {
	commits := make([]githubapp.RepositoryCommit, 0, 6)
	for index := 0; index < 6; index++ {
		commits = append(commits, githubapp.RepositoryCommit{SHA: strings.Repeat(fmt.Sprintf("%x", index), 80), Summary: fmt.Sprintf("change-%d\nignored body", index), CommittedAt: time.Now().UTC()})
	}
	views, truncated := repositoryCommitViews(commits, 3)
	if len(views) != 3 || !truncated {
		t.Fatalf("commit views were not bounded: views=%#v truncated=%v", views, truncated)
	}
	if views[0]["summary"] != "change-0" || len([]rune(views[0]["sha"].(string))) != 65 || !strings.HasSuffix(views[0]["sha"].(string), "…") {
		t.Fatalf("commit view was not normalized: %#v", views[0])
	}
}

func TestRepositorySearchUsesOnlyConfiguredEnabledRepositoryAndLabelsEvidence(t *testing.T) {
	reader := fakeReader{repos: []domain.Repository{
		{ID: "repo_enabled", Owner: "ReindeerGames", Name: "invoices", DefaultBranch: "main", Enabled: true},
		{ID: "repo_disabled", Owner: "Other", Name: "invoices", DefaultBranch: "main", Enabled: false},
	}}
	inspector := &fakeRepositorySearchInspector{searchResult: githubapp.RepositorySearchResult{
		Repository: "ReindeerGames/invoices", DefaultBranch: "main", Query: "how are session tokens validated",
		Matches:         []githubapp.RepositorySearchMatch{{Path: "internal/auth/session.go", LineStart: 41, LineEnd: 45, Snippet: "41: func ValidateSessionToken(token string) bool {"}},
		FilesConsidered: 28, FilesScanned: 28, BytesScanned: 4096,
	}}
	responder := NewOperator(reader, nil, nil, inspector)
	output, err := responder.executeTool(context.Background(), "search_repository", json.RawMessage(`{"repository":"invoices","query":"how are session tokens validated"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"available":true`, `"untrusted_evidence":true`, `"path":"internal/auth/session.go"`, `"line_start":41`, `"files_scanned":28`) {
		t.Fatalf("unexpected repository search output: %s", output)
	}
	if inspector.searchRepo.ID != "repo_enabled" || inspector.searchQuery != "how are session tokens validated" {
		t.Fatalf("repository search escaped configured scope: repo=%#v query=%q", inspector.searchRepo, inspector.searchQuery)
	}
}

func TestRepositorySearchReportsUnavailableWithoutSearchCapability(t *testing.T) {
	reader := fakeReader{repos: []domain.Repository{{ID: "repo_1", Owner: "Org", Name: "app", DefaultBranch: "main", Enabled: true}}}
	output, err := NewOperator(reader, nil, nil, &fakeRepositoryInspector{}).executeTool(context.Background(), "search_repository", json.RawMessage(`{"repository":"Org/app","query":"where is authentication configured"}`))
	if err != nil || !containsAll(output, `"found":true`, `"available":false`, `"reason":"repository search is unavailable"`) {
		t.Fatalf("unexpected unavailable repository search: output=%s err=%v", output, err)
	}
}

func TestGitHubInventoryComparesVisibleAndConfiguredRepositories(t *testing.T) {
	reader := fakeReader{
		projects: []domain.Project{{ID: "prj_1", Name: "Invoices"}},
		repos: []domain.Repository{
			{ID: "repo_1", ProjectID: "prj_1", Owner: "ReindeerGames", Name: "invoices", Role: "application", DefaultBranch: "main", ReadOnly: true, Enabled: true},
			{ID: "repo_missing", ProjectID: "prj_1", Owner: "ReindeerGames", Name: "removed", Role: "infrastructure", DefaultBranch: "main", ReadOnly: true, Enabled: false},
		},
	}
	inspector := &fakeRepositoryCatalogInspector{discovery: githubapp.RepositoryDiscovery{Repositories: []githubapp.AccessibleRepository{
		{Owner: "ReindeerGames", Name: "invoices", FullName: "ReindeerGames/invoices", DefaultBranch: "main", Private: true},
		{Owner: "ReindeerGames", Name: "tourism-awards", FullName: "ReindeerGames/tourism-awards", DefaultBranch: "main", Private: true},
	}}}
	output, err := NewOperator(reader, nil, nil, inspector).executeTool(context.Background(), "get_github_inventory", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"visible_count":2`, `"configured_visible_count":1`, `"unlinked_count":1`, `"configured_not_visible_count":1`, `"repository":"ReindeerGames/invoices"`, `"project":"Invoices"`, `"repository":"ReindeerGames/tourism-awards"`, `"configured":false`, `"untrusted_evidence":true`) {
		t.Fatalf("unexpected GitHub inventory output: %s", output)
	}
}

func TestGitHubInventoryOutputIsBoundedToOneHundredRepositories(t *testing.T) {
	discovery := githubapp.RepositoryDiscovery{}
	for index := 0; index < 101; index++ {
		name := fmt.Sprintf("repo-%03d", index)
		discovery.Repositories = append(discovery.Repositories, githubapp.AccessibleRepository{Owner: "Org", Name: name, FullName: "Org/" + name, DefaultBranch: "main"})
	}
	inspector := &fakeRepositoryCatalogInspector{discovery: discovery}
	output, err := NewOperator(fakeReader{}, nil, nil, inspector).executeTool(context.Background(), "get_github_inventory", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"visible_count":101`, `"unlinked_count":101`, `"truncated":true`) || strings.Count(output, `"repository":`) != 100 || strings.Contains(output, "repo-100") {
		t.Fatalf("unbounded GitHub inventory output: %s", output)
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

func TestRepositoryOverviewReportsLatestWorkflowHealthAndPartialFailures(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{repos: []domain.Repository{
		{ID: "repo_healthy", Owner: "ReindeerGames", Name: "healthy", DefaultBranch: "main", Enabled: true},
		{ID: "repo_failed", Owner: "ReindeerGames", Name: "failed", DefaultBranch: "main", Enabled: true},
		{ID: "repo_unavailable", Owner: "ReindeerGames", Name: "unavailable", DefaultBranch: "main", Enabled: true},
		{ID: "repo_disabled", Owner: "ReindeerGames", Name: "disabled", DefaultBranch: "main", Enabled: false},
	}}
	inspector := &estateRepositoryInspector{
		statuses: map[string]githubapp.RepositoryStatus{
			"ReindeerGames/healthy": {
				Repository: "ReindeerGames/healthy", DefaultBranch: "main", HeadSHA: strings.Repeat("a", 40), HeadCommittedAt: now.Add(-time.Hour),
				WorkflowRuns: []githubapp.RepositoryWorkflowRun{
					{Name: "CI", Status: "completed", Conclusion: "success", UpdatedAt: now.Add(-time.Minute)},
					{Name: "CI", Status: "completed", Conclusion: "failure", UpdatedAt: now.Add(-time.Hour)},
				},
			},
			"ReindeerGames/failed": {
				Repository: "ReindeerGames/failed", DefaultBranch: "main", HeadSHA: strings.Repeat("b", 40), HeadCommittedAt: now.Add(-2 * time.Hour),
				WorkflowRuns: []githubapp.RepositoryWorkflowRun{{Name: "Deploy", Status: "completed", Conclusion: "failure", UpdatedAt: now.Add(-time.Minute)}},
			},
		},
		errors: map[string]error{"ReindeerGames/unavailable": errors.New("secret upstream error must not leak")},
	}
	responder := NewOperator(reader, nil, nil, inspector)
	responder.now = func() time.Time { return now }
	answer, intent, err := responder.Answer(context.Background(), "github status")
	if err != nil || intent != "repository_overview" || !containsAll(answer, "GitHub repository status (3 enabled)", "HEALTHY · ReindeerGames/healthy · 1 latest workflow(s) healthy", "ATTENTION · ReindeerGames/failed · 1 latest workflow(s) failed", "UNAVAILABLE · ReindeerGames/unavailable") {
		t.Fatalf("unexpected repository overview intent=%s body=%s err=%v", intent, answer, err)
	}
	if strings.Contains(answer, "secret upstream") || strings.Contains(answer, "ReindeerGames/disabled") {
		t.Fatalf("unsafe repository overview: %s", answer)
	}
	output, err := responder.executeTool(context.Background(), "get_repository_overview", json.RawMessage(`{}`))
	if err != nil || !containsAll(output, `"configured_count":3`, `"inspected_count":2`, `"failed_count":1`, `"workflow_state":"healthy"`, `"workflow_state":"attention"`, `"reason":"inspection_failed"`) || strings.Contains(output, "secret upstream") {
		t.Fatalf("unexpected repository overview tool output=%s err=%v", output, err)
	}
}

func TestRepositoryOverviewIsBoundedToTwenty(t *testing.T) {
	reader := fakeReader{}
	inspector := &estateRepositoryInspector{statuses: map[string]githubapp.RepositoryStatus{}, errors: map[string]error{}}
	for index := 0; index < 21; index++ {
		name := fmt.Sprintf("repo-%02d", index)
		reader.repos = append(reader.repos, domain.Repository{ID: "id_" + name, Owner: "Org", Name: name, DefaultBranch: "main", Enabled: true})
		inspector.statuses["Org/"+name] = githubapp.RepositoryStatus{Repository: "Org/" + name, DefaultBranch: "main", HeadSHA: strings.Repeat("a", 40), HeadCommittedAt: time.Now().UTC()}
	}
	responder := NewOperator(reader, nil, nil, inspector)
	output, err := responder.executeTool(context.Background(), "get_repository_overview", json.RawMessage(`{}`))
	if err != nil || !containsAll(output, `"configured_count":21`, `"truncated":true`) {
		t.Fatalf("unexpected bounded overview output=%s err=%v", output, err)
	}
	inspector.mu.Lock()
	calls := len(inspector.calls)
	inspector.mu.Unlock()
	if calls != 20 {
		t.Fatalf("repository overview made %d calls, want 20", calls)
	}
}

func TestServiceInvestigationCorrelatesBoundedOperationalEvidence(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	reader := fakeReader{
		services: []domain.Service{
			{ID: "svc_api", ProjectID: "prj_1", ProjectName: "Invoices", Key: "invoices-api", Name: "Invoices API", Kind: "application", Criticality: "high", Enabled: true, HeartbeatTimeoutSeconds: 120},
			{ID: "svc_database", ProjectID: "prj_1", ProjectName: "Invoices", Key: "invoices-database", Name: "Invoices database", Kind: "database", Criticality: "critical", Enabled: true, HeartbeatTimeoutSeconds: 120},
			{ID: "svc_unrelated", ProjectID: "prj_2", ProjectName: "Other", Key: "other-database", Name: "Unrelated database", Kind: "database", Criticality: "critical", Enabled: true},
		},
		dependencies: []domain.ServiceDependency{{ID: "sdp_1", ProjectID: "prj_1", ServiceID: "svc_api", ServiceKey: "invoices-api", ServiceName: "Invoices API", DependsOnServiceID: "svc_database", DependsOnServiceKey: "invoices-database", DependsOnServiceName: "Invoices database", Kind: "data", Description: "primary relational store"}},
		events:       []domain.MonitorEvent{{ServiceID: "svc_api", Status: "unhealthy", Severity: "critical", Summary: "Readiness returned HTTP 503", Source: "observer-01", Fingerprint: "http-health", ObservedAt: now.Add(-2 * time.Minute), Details: json.RawMessage(`{"statusCode":503,"latencyMs":42,"secret":"must-not-leak"}`)}},
		alerts: []domain.ServiceAlert{
			{ServiceID: "svc_api", ServiceName: "Invoices API", ProjectID: "prj_1", Status: "open", Severity: "critical", Summary: "Readiness returned HTTP 503", Fingerprint: "http-health", OpenedAt: now.Add(-2 * time.Minute), LastSeenAt: now.Add(-time.Minute)},
			{ServiceID: "svc_database", ServiceName: "Invoices database", ProjectID: "prj_1", Status: "open", Severity: "warning", Summary: "Database backup is stale", Fingerprint: "artifact-freshness", OpenedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-5 * time.Minute)},
			{ServiceID: "svc_other", ServiceName: "Other", ProjectID: "prj_2", Status: "open", Severity: "warning", Summary: "must-not-appear"},
		},
		deployments: []domain.Deployment{{ID: "dep_1", ProjectID: "prj_1", Environment: "production", Operation: "deploy", Status: "verified", Repository: "ReindeerGames/invoices", ArtifactRef: strings.Repeat("a", 40), CreatedAt: now.Add(-10 * time.Minute)}},
		incidents:   []domain.Incident{{ID: "inc_1", ProjectID: "prj_1", ProjectName: "Invoices", Status: "triaging", Severity: "high", Summary: "Checkout failures", CreatedAt: now.Add(-3 * time.Minute), UpdatedAt: now.Add(-time.Minute)}},
		repos: []domain.Repository{
			{ID: "repo_1", ProjectID: "prj_1", Owner: "ReindeerGames", Name: "invoices", DefaultBranch: "main", Enabled: true},
			{ID: "repo_disabled", ProjectID: "prj_1", Owner: "ReindeerGames", Name: "old-invoices", DefaultBranch: "main", Enabled: false},
			{ID: "repo_other", ProjectID: "prj_2", Owner: "Other", Name: "other", DefaultBranch: "main", Enabled: true},
		},
	}
	inspector := &estateRepositoryInspector{statuses: map[string]githubapp.RepositoryStatus{
		"ReindeerGames/invoices": {
			Repository: "ReindeerGames/invoices", DefaultBranch: "main", HeadSHA: strings.Repeat("b", 40), HeadCommittedAt: now.Add(-15 * time.Minute),
			RecentCommits: []githubapp.RepositoryCommit{{SHA: strings.Repeat("b", 40), Summary: "Reduce checkout database timeout", CommittedAt: now.Add(-15 * time.Minute)}},
			WorkflowRuns:  []githubapp.RepositoryWorkflowRun{{Name: "Build", Status: "completed", Conclusion: "failure", UpdatedAt: now.Add(-5 * time.Minute)}},
		},
	}, errors: map[string]error{}}
	responder := NewOperator(reader, nil, nil, inspector)
	responder.now = func() time.Time { return now }
	output, err := responder.executeTool(context.Background(), "investigate_service", json.RawMessage(`{"service":"invoices-api"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(output, `"found":true`, `"limitations":["no logs","no shell","no SSH","no source download","no mutation"]`, `"name":"Invoices API"`, `"summary":"Readiness returned HTTP 503"`, `"statusCode":503`, `"id":"dep_1"`, `"id":"inc_1"`, `"repository":"ReindeerGames/invoices"`, `"workflow_state":"attention"`, `1 latest workflow(s) failed`, `"summary":"Reduce checkout database timeout"`, `"commits_truncated":false`, `"name":"Invoices database"`, `"state":"attention"`, `"open_alert_count":1`, `"direction":"depends_on"`, `"kind":"data"`, `"description":"primary relational store"`, `"dependency_relations"`, `"related_services_truncated":false`) {
		t.Fatalf("incomplete service investigation output: %s", output)
	}
	for _, forbidden := range []string{"must-not-leak", "must-not-appear", "old-invoices", "Other/other", "Unrelated database"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("service investigation leaked unrelated or unsafe evidence %q: %s", forbidden, output)
		}
	}
	inspector.mu.Lock()
	calls := append([]string(nil), inspector.calls...)
	inspector.mu.Unlock()
	if strings.Join(calls, ",") != "ReindeerGames/invoices" {
		t.Fatalf("service investigation inspected repositories outside the project scope: %v", calls)
	}
}

func TestRelatedServiceViewsPrioritizeAttentionAndRemainBounded(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	target := domain.Service{ID: "svc_target", ProjectID: "prj_1", Name: "Target", Enabled: true}
	services := []domain.Service{target}
	services = append(services, domain.Service{ID: "svc_other_project", ProjectID: "prj_2", Name: "Other project", Enabled: true})
	var events []domain.MonitorEvent
	for index := 0; index < 14; index++ {
		serviceID := fmt.Sprintf("svc_%02d", index)
		services = append(services, domain.Service{ID: serviceID, ProjectID: "prj_1", Key: fmt.Sprintf("service-%02d", index), Name: fmt.Sprintf("Service %02d", index), Kind: "application", Criticality: "medium", Enabled: true, HeartbeatTimeoutSeconds: 120})
		events = append(events, domain.MonitorEvent{ServiceID: serviceID, Status: "healthy", Severity: "info", Summary: "healthy", ObservedAt: now.Add(-time.Minute)})
	}
	alerts := []domain.ServiceAlert{{ServiceID: "svc_13", Status: "open", Severity: "critical", Summary: "dependency failed"}, {ServiceID: "svc_other_project", Status: "open"}}
	dependencies := []domain.ServiceDependency{{ServiceID: target.ID, DependsOnServiceID: "svc_00", Kind: "runtime", Description: "direct dependency"}}
	views, truncated := relatedServiceViews(now, target, services, events, alerts, dependencies, 12)
	if len(views) != 12 || !truncated {
		t.Fatalf("related service context was not bounded: count=%d truncated=%v", len(views), truncated)
	}
	if views[0]["id"] != "svc_00" || views[0]["dependency_relations"] == nil {
		t.Fatalf("declared dependency was not prioritized: %#v", views[0])
	}
	if views[1]["id"] != "svc_13" || views[1]["state"] != "attention" || views[1]["open_alert_count"] != 1 {
		t.Fatalf("attention service was not prioritized after direct dependencies: %#v", views[1])
	}
	for _, view := range views {
		if view["id"] == "svc_other_project" || view["id"] == target.ID {
			t.Fatalf("related service context escaped project/target scope: %#v", view)
		}
	}
}

func TestInvestigateExposesCorrelatedServiceToolToModel(t *testing.T) {
	reader := fakeReader{services: []domain.Service{{ID: "svc_api", ProjectID: "prj_1", ProjectName: "Invoices", Key: "invoices-api", Name: "Invoices API", Enabled: true}}}
	history := []domain.ConversationMessage{{Role: "user", Body: "Why is invoices-api failing?"}}
	answer, executions, err := New(reader, serviceInvestigationToolModel{}).Investigate(context.Background(), history)
	if err != nil || !strings.Contains(answer, "does not prove a cause") || len(executions) != 1 || executions[0].Name != "investigate_service" {
		t.Fatalf("unexpected correlated investigation answer=%q executions=%#v err=%v", answer, executions, err)
	}
}

func TestServiceInvestigationHandlesAmbiguousServiceWithoutExternalInspection(t *testing.T) {
	reader := fakeReader{services: []domain.Service{
		{ID: "svc_web_1", ProjectID: "prj_1", Key: "web-one", Name: "Web One"},
		{ID: "svc_web_2", ProjectID: "prj_2", Key: "web-two", Name: "Web Two"},
	}}
	inspector := &estateRepositoryInspector{statuses: map[string]githubapp.RepositoryStatus{}, errors: map[string]error{}}
	output, err := NewOperator(reader, nil, nil, inspector).executeTool(context.Background(), "investigate_service", json.RawMessage(`{"service":"web"}`))
	if err != nil || !containsAll(output, `"ambiguous":true`, `"key":"web-one"`, `"key":"web-two"`) {
		t.Fatalf("unexpected ambiguous investigation output=%s err=%v", output, err)
	}
	inspector.mu.Lock()
	calls := len(inspector.calls)
	inspector.mu.Unlock()
	if calls != 0 {
		t.Fatalf("ambiguous service triggered %d repository inspections", calls)
	}
}

func TestServiceInvestigationCollectionsAreBounded(t *testing.T) {
	reader := fakeReader{services: []domain.Service{{ID: "svc_1", ProjectID: "prj_1", Key: "api", Name: "API", Enabled: true}}}
	for index := 0; index < 13; index++ {
		reader.events = append(reader.events, domain.MonitorEvent{ServiceID: "svc_1", Status: "unhealthy", Severity: "warning", Summary: fmt.Sprintf("signal-%02d", index), Source: "observer", ObservedAt: time.Now().UTC().Add(-time.Duration(index) * time.Minute), Details: json.RawMessage(`{}`)})
	}
	for index := 0; index < 11; index++ {
		reader.alerts = append(reader.alerts, domain.ServiceAlert{ServiceID: "svc_1", Status: "open", Summary: fmt.Sprintf("alert-%02d", index)})
		reader.deployments = append(reader.deployments, domain.Deployment{ID: fmt.Sprintf("dep_%02d", index), ProjectID: "prj_1"})
		reader.incidents = append(reader.incidents, domain.Incident{ID: fmt.Sprintf("inc_%02d", index), ProjectID: "prj_1"})
	}
	for index := 0; index < 9; index++ {
		reader.repos = append(reader.repos, domain.Repository{ID: fmt.Sprintf("repo_%02d", index), ProjectID: "prj_1", Owner: "Org", Name: fmt.Sprintf("repo-%02d", index), Enabled: true})
	}
	output, err := NewOperator(reader, nil, nil, nil).executeTool(context.Background(), "investigate_service", json.RawMessage(`{"service":"api"}`))
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Signals               []json.RawMessage `json:"signals"`
		SignalsTruncated      bool              `json:"signals_truncated"`
		OpenAlerts            []json.RawMessage `json:"open_alerts"`
		AlertsTruncated       bool              `json:"alerts_truncated"`
		Deployments           []json.RawMessage `json:"deployments"`
		DeploymentsTruncated  bool              `json:"deployments_truncated"`
		Incidents             []json.RawMessage `json:"incidents"`
		IncidentsTruncated    bool              `json:"incidents_truncated"`
		Repositories          []json.RawMessage `json:"repositories"`
		RepositoriesTruncated bool              `json:"repositories_truncated"`
	}
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Signals) != 12 || !view.SignalsTruncated || len(view.OpenAlerts) != 10 || !view.AlertsTruncated || len(view.Deployments) != 10 || !view.DeploymentsTruncated || len(view.Incidents) != 10 || !view.IncidentsTruncated || len(view.Repositories) != 8 || !view.RepositoriesTruncated {
		t.Fatalf("unbounded service investigation: %#v", view)
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
		incident:    domain.Incident{ID: "inc_123", ProjectName: "Invoices", Status: "resolved", Severity: "high", Summary: "Invoices were blank", EvidenceRevision: 2, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		messages:    []domain.IncidentMessage{{ID: "msg_1", IncidentID: "inc_123", SenderName: "Reporter", Direction: "inbound", Body: "Invoices were blank", MessageTimestamp: now.Add(-2 * time.Hour)}, {ID: "msg_2", IncidentID: "inc_123", SenderName: "Reporter", Direction: "inbound", Body: "Only on Safari", MessageTimestamp: now.Add(-110 * time.Minute)}},
		attachments: []domain.IncidentAttachment{{ID: "att_1", IncidentID: "inc_123", Kind: "image", MIMEType: "image/png", FileName: "error.png", SizeBytes: 2048, SHA256: strings.Repeat("a", 64), Data: []byte("must-not-leak-image-bytes"), CreatedAt: now.Add(-2 * time.Hour)}},
		analysis:    domain.Analysis{ID: "ana_1", IncidentID: "inc_123", Status: "complete", Confidence: "high", Summary: "Template regression", CreatedAt: now.Add(-90 * time.Minute)},
		changes:     []domain.ChangeSet{{ID: "chg_1", IncidentID: "inc_123", RepositoryID: "repo_1", BranchName: "echo/inc-123", CommitSHA: "a75cdbdaad7917a104299753c32f8358b90a50e6", PullRequestNumber: 17, ArtifactRef: "a75cdbdaad7917a104299753c32f8358b90a50e6", Status: "merged", CreatedAt: now.Add(-time.Hour), UpdatedAt: now}},
		deployments: []domain.Deployment{{ID: "dep_1", IncidentID: "inc_123", ProjectID: "prj_1", Environment: "production", Operation: "deploy", ArtifactRef: "a75cdbdaad7917a104299753c32f8358b90a50e6", Status: "verified", CreatedAt: now.Add(-30 * time.Minute)}},
	}
	responder := New(reader)
	answer, intent, err := responder.Answer(context.Background(), "incident inc_123")
	if err != nil || intent != "incident" || !containsAll(answer, "Follow-up evidence: 1 message(s) · revision 2", "Screenshot evidence: 1 retained image(s)", "Analysis (high confidence): Template regression", "Latest change: merged · PR #17 · a75cdbda", "Latest deployment: production · deploy · verified") {
		t.Fatalf("unexpected incident answer intent=%s body=%s err=%v", intent, answer, err)
	}
	output, err := responder.executeTool(context.Background(), "get_incident", json.RawMessage(`{"incident_id":"inc_123"}`))
	if err != nil || !containsAll(output, `"pull_request_number":17`, `"commit_sha":"a75cdbdaad7917a104299753c32f8358b90a50e6"`, `"environment":"production"`, `"status":"verified"`, `"file_name":"error.png"`, `"size_bytes":2048`, `"body":"Only on Safari"`) || strings.Contains(output, "pullRequestUrl") || strings.Contains(output, "must-not-leak-image-bytes") {
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
