package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/store"
)

type Reader interface {
	Dashboard(context.Context) (domain.Dashboard, error)
	ListProjects(context.Context) ([]domain.Project, error)
	ListRepositories(context.Context, string) ([]domain.Repository, error)
	ListServices(context.Context, string) ([]domain.Service, error)
	ListServiceAlerts(context.Context, string) ([]domain.ServiceAlert, error)
	ListMonitorEvents(context.Context, string, int) ([]domain.MonitorEvent, error)
	GetIncident(context.Context, string) (domain.Incident, error)
	LatestAnalysis(context.Context, string) (domain.Analysis, error)
}

type Responder struct {
	reader Reader
}

func New(reader Reader) *Responder { return &Responder{reader: reader} }

func (r *Responder) Answer(ctx context.Context, text string) (string, string, error) {
	intent, argument := classify(text)
	var answer string
	var err error
	switch intent {
	case "help":
		answer = helpText()
	case "status":
		answer, err = r.status(ctx)
	case "alerts":
		answer, err = r.alerts(ctx)
	case "services":
		answer, err = r.services(ctx)
	case "projects":
		answer, err = r.projects(ctx)
	case "service":
		answer, err = r.service(ctx, argument)
	case "incident":
		answer, err = r.incident(ctx, argument)
	default:
		intent = "unknown"
		answer = "I can investigate the facts Echo currently has, but I did not understand that request yet. Try “status”, “alerts”, “services”, “service <name>”, “projects”, or “incident <id>”. I will never make a production change from a question."
	}
	return truncate(answer, 3500), intent, err
}

func classify(text string) (string, string) {
	value := strings.ToLower(strings.TrimSpace(text))
	value = strings.Trim(value, "?!., ")
	if value == "" || value == "help" || value == "commands" || strings.Contains(value, "what can you do") {
		return "help", ""
	}
	for _, prefix := range []string{"service ", "check service ", "check app ", "app ", "application "} {
		if strings.HasPrefix(value, prefix) {
			return "service", strings.TrimSpace(strings.TrimPrefix(value, prefix))
		}
	}
	if strings.HasPrefix(value, "incident ") {
		return "incident", strings.TrimSpace(strings.TrimPrefix(value, "incident "))
	}
	if value == "alerts" || value == "issues" || value == "problems" || strings.Contains(value, "anything wrong") || strings.Contains(value, "what is down") || strings.Contains(value, "what's down") {
		return "alerts", ""
	}
	if value == "services" || value == "apps" || value == "applications" || value == "list services" || value == "list apps" {
		return "services", ""
	}
	if value == "projects" || value == "repos" || value == "repositories" || value == "list projects" {
		return "projects", ""
	}
	if value == "status" || value == "overview" || value == "health" || strings.Contains(value, "how are things") || strings.Contains(value, "what's happening") || strings.Contains(value, "what is happening") {
		return "status", ""
	}
	return "unknown", ""
}

func (r *Responder) status(ctx context.Context) (string, error) {
	dashboard, err := r.reader.Dashboard(ctx)
	if err != nil {
		return "", err
	}
	services, err := r.reader.ListServices(ctx, "")
	if err != nil {
		return "", err
	}
	enabled := 0
	for _, service := range services {
		if service.Enabled {
			enabled++
		}
	}
	state := "No open runtime alerts."
	if dashboard.OpenAlerts > 0 {
		state = fmt.Sprintf("%d runtime alert(s) need attention.", dashboard.OpenAlerts)
	}
	return fmt.Sprintf("Echo operational status\n%s\n\nServices: %d enabled\nOpen incidents: %d\nPending approvals: %d\nRunning jobs: %d\nFailed deployments: %d", state, enabled, dashboard.OpenIncidents, dashboard.PendingApprovals, dashboard.RunningJobs, dashboard.FailedDeployments), nil
}

func (r *Responder) alerts(ctx context.Context) (string, error) {
	alerts, err := r.reader.ListServiceAlerts(ctx, "open")
	if err != nil {
		return "", err
	}
	if len(alerts) == 0 {
		return "No open service alerts. Echo will message you when a monitored condition opens, reopens, or resolves.", nil
	}
	var lines []string
	for i, alert := range alerts {
		if i == 10 {
			lines = append(lines, fmt.Sprintf("…and %d more in the control plane.", len(alerts)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("• %s · %s\n  %s", strings.ToUpper(alert.Severity), alert.ServiceName, alert.Summary))
	}
	return fmt.Sprintf("Open service alerts (%d)\n\n%s", len(alerts), strings.Join(lines, "\n")), nil
}

func (r *Responder) services(ctx context.Context) (string, error) {
	services, err := r.reader.ListServices(ctx, "")
	if err != nil {
		return "", err
	}
	if len(services) == 0 {
		return "No services are configured yet. Add the first project workload under Monitoring in the Echo control plane.", nil
	}
	alerts, err := r.reader.ListServiceAlerts(ctx, "open")
	if err != nil {
		return "", err
	}
	open := map[string]int{}
	for _, alert := range alerts {
		open[alert.ServiceID]++
	}
	var lines []string
	for i, service := range services {
		if i == 20 {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(services)-i))
			break
		}
		state := "no open alerts"
		if count := open[service.ID]; count > 0 {
			state = fmt.Sprintf("%d open alert(s)", count)
		}
		if !service.Enabled {
			state = "monitoring disabled"
		}
		lines = append(lines, fmt.Sprintf("• %s (%s, %s) — %s", service.Name, service.Kind, service.Criticality, state))
	}
	return fmt.Sprintf("Service catalogue (%d)\n\n%s", len(services), strings.Join(lines, "\n")), nil
}

func (r *Responder) service(ctx context.Context, query string) (string, error) {
	if query == "" {
		return "Please name the service, for example “service nudgie-web”.", nil
	}
	services, err := r.reader.ListServices(ctx, "")
	if err != nil {
		return "", err
	}
	matches := matchServices(services, query)
	if len(matches) == 0 {
		return fmt.Sprintf("I could not find a service matching “%s”. Send “services” to list the catalogue.", query), nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, service := range matches {
			names = append(names, service.Name+" ("+service.Key+")")
		}
		return "That name matches more than one service: " + strings.Join(names, ", ") + ". Please use the service key.", nil
	}
	service := matches[0]
	alerts, err := r.reader.ListServiceAlerts(ctx, "open")
	if err != nil {
		return "", err
	}
	var serviceAlerts []domain.ServiceAlert
	for _, alert := range alerts {
		if alert.ServiceID == service.ID {
			serviceAlerts = append(serviceAlerts, alert)
		}
	}
	events, err := r.reader.ListMonitorEvents(ctx, service.ID, 5)
	if err != nil {
		return "", err
	}
	lines := []string{fmt.Sprintf("%s · %s / %s", service.ProjectName, service.Kind, service.Criticality), service.Description, fmt.Sprintf("Monitoring: %s", map[bool]string{true: "enabled", false: "disabled"}[service.Enabled])}
	if len(serviceAlerts) == 0 {
		lines = append(lines, "Open alerts: none")
	} else {
		lines = append(lines, fmt.Sprintf("Open alerts: %d", len(serviceAlerts)))
		for _, alert := range serviceAlerts {
			lines = append(lines, fmt.Sprintf("• %s · %s", strings.ToUpper(alert.Severity), alert.Summary))
		}
	}
	if len(events) > 0 {
		latest := events[0]
		lines = append(lines, fmt.Sprintf("Latest signal: %s · %s · %s", latest.Status, latest.Source, latest.Summary))
	} else {
		lines = append(lines, "Latest signal: none received")
	}
	return fmt.Sprintf("Service: %s (%s)\n\n%s", service.Name, service.Key, strings.Join(nonEmpty(lines), "\n")), nil
}

func (r *Responder) projects(ctx context.Context) (string, error) {
	projects, err := r.reader.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	repositories, err := r.reader.ListRepositories(ctx, "")
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		return "No projects are configured yet.", nil
	}
	counts := map[string]int{}
	for _, repository := range repositories {
		if repository.Enabled {
			counts[repository.ProjectID]++
		}
	}
	lines := make([]string, 0, len(projects))
	for _, project := range projects {
		state := "enabled"
		if !project.Enabled {
			state = "disabled"
		}
		lines = append(lines, fmt.Sprintf("• %s (%s) — %s, %d repos", project.Name, project.Key, state, counts[project.ID]))
	}
	return fmt.Sprintf("Projects (%d)\n\n%s", len(projects), strings.Join(lines, "\n")), nil
}

func (r *Responder) incident(ctx context.Context, incidentID string) (string, error) {
	if incidentID == "" {
		return "Please include the incident ID shown in Echo, for example “incident inc_…”.", nil
	}
	incident, err := r.reader.GetIncident(ctx, incidentID)
	if errors.Is(err, store.ErrNotFound) {
		return "I could not find that incident.", nil
	}
	if err != nil {
		return "", err
	}
	lines := []string{fmt.Sprintf("Status: %s · Severity: %s", incident.Status, incident.Severity), fmt.Sprintf("Project: %s", incident.ProjectName), fmt.Sprintf("Report: %s", incident.Summary)}
	analysis, analysisErr := r.reader.LatestAnalysis(ctx, incident.ID)
	if analysisErr == nil {
		lines = append(lines, fmt.Sprintf("Analysis (%s confidence): %s", analysis.Confidence, analysis.Summary))
	} else if !errors.Is(analysisErr, store.ErrNotFound) {
		return "", analysisErr
	}
	return fmt.Sprintf("Incident %s\n\n%s", incident.ID, strings.Join(nonEmpty(lines), "\n")), nil
}

func matchServices(services []domain.Service, query string) []domain.Service {
	query = strings.ToLower(strings.TrimSpace(query))
	var exact, partial []domain.Service
	for _, service := range services {
		id := strings.ToLower(service.ID)
		key := strings.ToLower(service.Key)
		name := strings.ToLower(service.Name)
		if query == id || query == key || query == name {
			exact = append(exact, service)
		} else if strings.Contains(key, query) || strings.Contains(name, query) {
			partial = append(partial, service)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	sort.Slice(partial, func(i, j int) bool { return partial[i].Name < partial[j].Name })
	return partial
}

func helpText() string {
	return "Echo operator commands\n\n• status — estate summary\n• alerts — open runtime conditions\n• services — monitored service catalogue\n• service <name> — alerts and latest evidence\n• projects — project and repository map\n• incident <id> — incident and diagnosis\n• approve <code> / reject <code> — resolve an exact pending approval\n\nQuestions are read-only. Production changes always require a separate exact-action approval."
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "\n\n…truncated; open the Echo control plane for the complete view."
}
