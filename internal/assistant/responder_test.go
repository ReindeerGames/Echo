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
	"github.com/ReindeerGames/echo/internal/store"
)

type fakeReader struct {
	dashboard domain.Dashboard
	projects  []domain.Project
	repos     []domain.Repository
	services  []domain.Service
	alerts    []domain.ServiceAlert
	events    []domain.MonitorEvent
	jobs      []domain.Job
}

type fakeToolModel struct{}

func (fakeToolModel) RespondWithTools(ctx context.Context, instructions string, history []ai.ConversationMessage, tools []ai.ToolDefinition, execute ai.ToolExecutor) (string, []ai.ToolExecution, error) {
	if !strings.Contains(instructions, "no shell") || len(history) == 0 {
		return "", nil, errors.New("missing safe investigation context")
	}
	for _, tool := range tools {
		if strings.Contains(tool.Name, "restart") || strings.Contains(tool.Name, "deploy") || strings.Contains(tool.Name, "shell") {
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
func (f fakeReader) ListJobs(context.Context, string, int) ([]domain.Job, error) { return f.jobs, nil }
func (f fakeReader) GetIncident(context.Context, string) (domain.Incident, error) {
	return domain.Incident{}, store.ErrNotFound
}
func (f fakeReader) LatestAnalysis(context.Context, string) (domain.Analysis, error) {
	return domain.Analysis{}, store.ErrNotFound
}

func TestClassifyNaturalOperatorQuestions(t *testing.T) {
	tests := map[string]string{
		"anything wrong?":     "alerts",
		"how are things":      "status",
		"service nudgie-web":  "service",
		"what can you do?":    "help",
		"list projects":       "projects",
		"incident inc_123456": "incident",
		"failed jobs":         "jobs",
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

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
