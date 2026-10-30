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
		dashboard: domain.Dashboard{OpenAlerts: 1, OpenIncidents: 2, PendingApprovals: 1},
		services:  []domain.Service{{ID: "svc_1", Key: "nudgie-web", Name: "Nudgie web", ProjectName: "Nudgie", Kind: "application", Criticality: "high", Enabled: true}},
		alerts:    []domain.ServiceAlert{{ServiceID: "svc_1", ServiceName: "Nudgie web", Severity: "critical", Summary: "Health check failed"}},
		events:    []domain.MonitorEvent{{ServiceID: "svc_1", ServiceName: "Nudgie web", Status: "unhealthy", Source: "docker", Summary: "Health check failed", ObservedAt: now}},
	}
	responder := New(reader)
	status, intent, err := responder.Answer(context.Background(), "status")
	if err != nil || intent != "status" || !containsAll(status, "1 runtime alert", "Open incidents: 2") {
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

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
