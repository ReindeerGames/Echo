package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

const maxDockerInspectBytes = 1 << 20

type dockerInspect struct {
	Name         string      `json:"Name"`
	RestartCount int         `json:"RestartCount"`
	State        dockerState `json:"State"`
}

type dockerState struct {
	Status     string        `json:"Status"`
	Running    bool          `json:"Running"`
	Paused     bool          `json:"Paused"`
	Restarting bool          `json:"Restarting"`
	OOMKilled  bool          `json:"OOMKilled"`
	Dead       bool          `json:"Dead"`
	ExitCode   int           `json:"ExitCode"`
	Error      string        `json:"Error"`
	StartedAt  string        `json:"StartedAt"`
	FinishedAt string        `json:"FinishedAt"`
	Health     *dockerHealth `json:"Health"`
}

type dockerHealth struct {
	Status        string `json:"Status"`
	FailingStreak int    `json:"FailingStreak"`
}

func (o *Observer) observeDocker(ctx context.Context, probe DockerProbe) domain.MonitorEvent {
	observedAt := o.now()
	event := domain.MonitorEvent{
		EventID:     fmt.Sprintf("%s:%s:%d", o.config.Source, probe.Name, observedAt.UnixNano()),
		ServiceID:   probe.ServiceID,
		Source:      o.config.Source,
		Status:      "unhealthy",
		Severity:    "critical",
		Summary:     probe.Name + " container evidence is unavailable",
		Fingerprint: "container-health",
		ObservedAt:  observedAt,
	}
	endpoint := probe.ProxyURL + "/containers/" + url.PathEscape(probe.Container) + "/json"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		event.Details = dockerFailureDetails(probe.Container, "invalid_request", 0)
		return event
	}
	request.Header.Set("User-Agent", "Echo-Observer/1.0")
	response, err := o.probe.Do(request)
	if err != nil {
		event.Details = dockerFailureDetails(probe.Container, classifyProbeError(err), 0)
		return event
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		event.Details = dockerFailureDetails(probe.Container, "inspect_status", response.StatusCode)
		return event
	}
	if response.ContentLength > maxDockerInspectBytes {
		event.Details = dockerFailureDetails(probe.Container, "inspect_response_too_large", response.StatusCode)
		return event
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDockerInspectBytes+1))
	if err != nil || len(body) > maxDockerInspectBytes {
		event.Details = dockerFailureDetails(probe.Container, "inspect_response_too_large", response.StatusCode)
		return event
	}
	var inspect dockerInspect
	if err := json.Unmarshal(body, &inspect); err != nil {
		event.Details = dockerFailureDetails(probe.Container, "invalid_inspect_response", response.StatusCode)
		return event
	}
	return dockerEventFromInspect(event, probe, inspect)
}

func dockerEventFromInspect(event domain.MonitorEvent, probe DockerProbe, inspect dockerInspect) domain.MonitorEvent {
	event.Status = "unhealthy"
	event.Severity = "critical"
	event.Fingerprint = "container-health"
	state := normalizedContainerState(inspect.State.Status)
	health := "none"
	failingStreak := 0
	if inspect.State.Health != nil {
		health = normalizedContainerHealth(inspect.State.Health.Status)
		failingStreak = max(0, inspect.State.Health.FailingStreak)
	}
	restartCount := max(0, inspect.RestartCount)
	details, _ := json.Marshal(map[string]any{
		"container":     probe.Container,
		"state":         state,
		"running":       inspect.State.Running,
		"paused":        inspect.State.Paused,
		"restarting":    inspect.State.Restarting,
		"oomKilled":     inspect.State.OOMKilled,
		"dead":          inspect.State.Dead,
		"exitCode":      inspect.State.ExitCode,
		"stateError":    inspect.State.Error != "",
		"health":        health,
		"failingStreak": failingStreak,
		"restartCount":  restartCount,
		"startedAt":     normalizedDockerTime(inspect.State.StartedAt),
		"finishedAt":    normalizedDockerTime(inspect.State.FinishedAt),
	})
	event.Details = details
	switch {
	case inspect.State.OOMKilled:
		event.Summary = probe.Name + " container was OOM-killed"
	case inspect.State.Dead:
		event.Summary = probe.Name + " container is dead"
	case inspect.State.Restarting:
		event.Summary = probe.Name + " container is restarting"
	case inspect.State.Paused:
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = probe.Name + " container is paused"
	case !inspect.State.Running || state != "running":
		event.Summary = fmt.Sprintf("%s container is %s (exit %d)", probe.Name, state, inspect.State.ExitCode)
	case health == "unhealthy":
		event.Summary = fmt.Sprintf("%s Docker health check is unhealthy (streak %d)", probe.Name, failingStreak)
	case health == "starting":
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = probe.Name + " Docker health check is still starting"
	case health != "none" && health != "healthy":
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = probe.Name + " Docker health state is unknown"
	default:
		event.Status = "healthy"
		event.Severity = "info"
		if health == "healthy" {
			event.Summary = fmt.Sprintf("%s container is running and healthy (restarts %d)", probe.Name, restartCount)
		} else {
			event.Summary = fmt.Sprintf("%s container is running without a Docker health check (restarts %d)", probe.Name, restartCount)
		}
	}
	return event
}

func dockerFailureDetails(container, failure string, statusCode int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"container": container, "failure": failure, "statusCode": statusCode})
	return raw
}

func normalizedContainerState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "created", "running", "paused", "restarting", "removing", "exited", "dead":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return "unknown"
	}
}

func normalizedContainerHealth(health string) string {
	switch strings.ToLower(strings.TrimSpace(health)) {
	case "healthy", "unhealthy", "starting":
		return strings.ToLower(strings.TrimSpace(health))
	default:
		return "unknown"
	}
}

func normalizedDockerTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil || parsed.Year() <= 1 {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}
