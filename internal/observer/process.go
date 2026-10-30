package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/ReindeerGames/echo/internal/domain"
)

type ProcessSnapshot struct {
	ProcessCount  int    `json:"processCount"`
	RunningCount  int    `json:"runningCount"`
	SleepingCount int    `json:"sleepingCount"`
	StoppedCount  int    `json:"stoppedCount"`
	ZombieCount   int    `json:"zombieCount"`
	OtherCount    int    `json:"otherCount"`
	ThreadCount   uint64 `json:"threadCount"`
	RSSBytes      uint64 `json:"rssBytes"`
}

func (o *Observer) observeProcess(ctx context.Context, probe ProcessProbe) domain.MonitorEvent {
	observedAt := o.now()
	event := domain.MonitorEvent{
		EventID:     fmt.Sprintf("%s:%s:%d", o.config.Source, probe.Name, observedAt.UnixNano()),
		ServiceID:   probe.ServiceID,
		Source:      o.config.Source,
		Status:      "unhealthy",
		Severity:    "critical",
		Summary:     probe.Name + " process metrics are unavailable",
		Fingerprint: "process-health",
		ObservedAt:  observedAt,
	}
	endpoint := probe.ProxyURL + "/processes/" + url.PathEscape(probe.ProcessName)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		event.Details = processFailureDetails(probe.ProcessName, "invalid_request", 0)
		return event
	}
	request.Header.Set("User-Agent", "Echo-Observer/1.0")
	response, err := o.probe.Do(request)
	if err != nil {
		event.Details = processFailureDetails(probe.ProcessName, classifyProbeError(err), 0)
		return event
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		event.Details = processFailureDetails(probe.ProcessName, "process_inspect_failed", response.StatusCode)
		return event
	}
	if response.ContentLength > maxResponseBytes {
		event.Details = processFailureDetails(probe.ProcessName, "process_response_too_large", response.StatusCode)
		return event
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		event.Details = processFailureDetails(probe.ProcessName, "process_response_too_large", response.StatusCode)
		return event
	}
	var snapshot ProcessSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil || !validProcessSnapshot(snapshot) {
		event.Details = processFailureDetails(probe.ProcessName, "invalid_process_response", response.StatusCode)
		return event
	}
	event.Details = processDetails(probe, snapshot)
	if snapshot.ProcessCount < probe.MinimumCount {
		event.Summary = fmt.Sprintf("%s process count %d is below minimum %d", probe.Name, snapshot.ProcessCount, probe.MinimumCount)
		return event
	}
	if snapshot.ZombieCount > 0 {
		event.Summary = fmt.Sprintf("%s has %d zombie %s", probe.Name, snapshot.ZombieCount, pluralized("process", "processes", uint64(snapshot.ZombieCount)))
		return event
	}
	criticalRSS := probe.RSSCriticalMiB * 1024 * 1024
	if criticalRSS > 0 && snapshot.RSSBytes >= criticalRSS {
		event.Summary = fmt.Sprintf("%s aggregate RSS is above the critical threshold", probe.Name)
		return event
	}
	event.Status = "healthy"
	event.Severity = "info"
	event.Summary = fmt.Sprintf("%s process health is normal (%d %s, %d %s)", probe.Name, snapshot.ProcessCount, pluralized("process", "processes", uint64(snapshot.ProcessCount)), snapshot.ThreadCount, pluralized("thread", "threads", snapshot.ThreadCount))
	if probe.MaximumCount > 0 && snapshot.ProcessCount > probe.MaximumCount {
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = fmt.Sprintf("%s process count %d is above maximum %d", probe.Name, snapshot.ProcessCount, probe.MaximumCount)
		return event
	}
	if snapshot.StoppedCount > 0 || snapshot.OtherCount > 0 {
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = fmt.Sprintf("%s has %d stopped and %d unknown-state processes", probe.Name, snapshot.StoppedCount, snapshot.OtherCount)
		return event
	}
	warningRSS := probe.RSSWarningMiB * 1024 * 1024
	if warningRSS > 0 && snapshot.RSSBytes >= warningRSS {
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = fmt.Sprintf("%s aggregate RSS is above the warning threshold", probe.Name)
	}
	return event
}

func processFailureDetails(processName, failure string, statusCode int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"processName": processName, "failure": failure, "statusCode": statusCode})
	return raw
}

func pluralized(singular, plural string, count uint64) string {
	if count == 1 {
		return singular
	}
	return plural
}

func processDetails(probe ProcessProbe, snapshot ProcessSnapshot) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"processName": probe.ProcessName,
		"snapshot":    snapshot,
		"thresholds": map[string]any{
			"minimumCount": probe.MinimumCount, "maximumCount": probe.MaximumCount,
			"rssWarningMiB": probe.RSSWarningMiB, "rssCriticalMiB": probe.RSSCriticalMiB,
		},
	})
	return raw
}

func validProcessSnapshot(snapshot ProcessSnapshot) bool {
	states := snapshot.RunningCount + snapshot.SleepingCount + snapshot.StoppedCount + snapshot.ZombieCount + snapshot.OtherCount
	if snapshot.ProcessCount < 0 || snapshot.RunningCount < 0 || snapshot.SleepingCount < 0 || snapshot.StoppedCount < 0 || snapshot.ZombieCount < 0 || snapshot.OtherCount < 0 {
		return false
	}
	return states == snapshot.ProcessCount && snapshot.ThreadCount >= uint64(snapshot.ProcessCount)
}
