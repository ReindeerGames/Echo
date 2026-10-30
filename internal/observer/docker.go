package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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

type dockerStats struct {
	CPUAvailable      bool    `json:"cpuAvailable"`
	CPUPercent        float64 `json:"cpuPercent"`
	MemoryAvailable   bool    `json:"memoryAvailable"`
	MemoryUsageBytes  uint64  `json:"memoryUsageBytes"`
	MemoryLimitBytes  uint64  `json:"memoryLimitBytes"`
	MemoryUsedPercent float64 `json:"memoryUsedPercent"`
	PIDsCurrent       uint64  `json:"pidsCurrent"`
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
	event = dockerEventFromInspect(event, probe, inspect)
	if !dockerResourceMonitoringEnabled(probe) || event.Status == "unhealthy" || !inspect.State.Running || inspect.State.Paused || inspect.State.Restarting {
		return event
	}
	return o.observeDockerResources(ctx, event, probe)
}

func (o *Observer) observeDockerResources(ctx context.Context, event domain.MonitorEvent, probe DockerProbe) domain.MonitorEvent {
	endpoint := probe.ProxyURL + "/containers/" + url.PathEscape(probe.Container) + "/stats"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return dockerResourceFailure(event, probe, "invalid_request", 0)
	}
	request.Header.Set("User-Agent", "Echo-Observer/1.0")
	response, err := o.probe.Do(request)
	if err != nil {
		return dockerResourceFailure(event, probe, classifyProbeError(err), 0)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return dockerResourceFailure(event, probe, "stats_status", response.StatusCode)
	}
	if response.ContentLength > maxResponseBytes {
		return dockerResourceFailure(event, probe, "stats_response_too_large", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return dockerResourceFailure(event, probe, "stats_response_too_large", response.StatusCode)
	}
	var stats dockerStats
	if err := json.Unmarshal(body, &stats); err != nil || !validDockerStats(stats) {
		return dockerResourceFailure(event, probe, "invalid_stats_response", response.StatusCode)
	}
	if probe.CPUCriticalPercent > 0 && !stats.CPUAvailable {
		return dockerResourceFailure(event, probe, "cpu_metrics_unavailable", response.StatusCode)
	}
	if probe.MemoryCriticalPercent > 0 && !stats.MemoryAvailable {
		return dockerResourceFailure(event, probe, "memory_metrics_unavailable", response.StatusCode)
	}
	return dockerEventWithResources(event, probe, stats)
}

func dockerResourceMonitoringEnabled(probe DockerProbe) bool {
	return probe.CPUCriticalPercent > 0 || probe.MemoryCriticalPercent > 0
}

func dockerResourceFailure(event domain.MonitorEvent, probe DockerProbe, failure string, statusCode int) domain.MonitorEvent {
	event.Status = "unhealthy"
	event.Severity = "critical"
	event.Summary = probe.Name + " container resource evidence is unavailable"
	event.Details = dockerFailureDetails(probe.Container, failure, statusCode)
	return event
}

func validDockerStats(stats dockerStats) bool {
	if math.IsNaN(stats.CPUPercent) || math.IsInf(stats.CPUPercent, 0) || math.IsNaN(stats.MemoryUsedPercent) || math.IsInf(stats.MemoryUsedPercent, 0) {
		return false
	}
	if stats.CPUPercent < 0 || stats.CPUPercent > 1_000_000 || stats.MemoryUsedPercent < 0 || stats.MemoryUsedPercent > 1_000_000 {
		return false
	}
	if !stats.CPUAvailable && stats.CPUPercent != 0 {
		return false
	}
	if !stats.MemoryAvailable {
		return stats.MemoryUsageBytes == 0 && stats.MemoryLimitBytes == 0 && stats.MemoryUsedPercent == 0
	}
	return stats.MemoryLimitBytes > 0
}

func dockerEventWithResources(event domain.MonitorEvent, probe DockerProbe, stats dockerStats) domain.MonitorEvent {
	details := make(map[string]any)
	_ = json.Unmarshal(event.Details, &details)
	details["cpuAvailable"] = stats.CPUAvailable
	details["cpuPercent"] = stats.CPUPercent
	details["memoryAvailable"] = stats.MemoryAvailable
	details["memoryUsageBytes"] = stats.MemoryUsageBytes
	details["memoryLimitBytes"] = stats.MemoryLimitBytes
	details["memoryUsedPercent"] = stats.MemoryUsedPercent
	details["pidsCurrent"] = stats.PIDsCurrent
	details["thresholds"] = map[string]any{
		"cpuWarningPercent": probe.CPUWarningPercent, "cpuCriticalPercent": probe.CPUCriticalPercent,
		"memoryWarningPercent": probe.MemoryWarningPercent, "memoryCriticalPercent": probe.MemoryCriticalPercent,
	}
	event.Details, _ = json.Marshal(details)
	cpuCritical := probe.CPUCriticalPercent > 0 && stats.CPUPercent >= probe.CPUCriticalPercent
	memoryCritical := probe.MemoryCriticalPercent > 0 && stats.MemoryUsedPercent >= probe.MemoryCriticalPercent
	if cpuCritical || memoryCritical {
		event.Status = "unhealthy"
		event.Severity = "critical"
		event.Summary = dockerResourceSummary(probe.Name, stats, cpuCritical, memoryCritical, "critical")
		return event
	}
	cpuWarning := probe.CPUWarningPercent > 0 && stats.CPUPercent >= probe.CPUWarningPercent
	memoryWarning := probe.MemoryWarningPercent > 0 && stats.MemoryUsedPercent >= probe.MemoryWarningPercent
	if cpuWarning || memoryWarning {
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = dockerResourceSummary(probe.Name, stats, cpuWarning, memoryWarning, "warning")
	}
	return event
}

func dockerResourceSummary(name string, stats dockerStats, includeCPU, includeMemory bool, severity string) string {
	parts := make([]string, 0, 2)
	if includeCPU {
		parts = append(parts, fmt.Sprintf("CPU %.2f%%", stats.CPUPercent))
	}
	if includeMemory {
		parts = append(parts, fmt.Sprintf("memory %.2f%%", stats.MemoryUsedPercent))
	}
	return fmt.Sprintf("%s container resource %s is above the %s threshold", name, strings.Join(parts, " and "), severity)
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
