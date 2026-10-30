package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/ReindeerGames/echo/internal/domain"
)

type SystemSnapshot struct {
	DiskPath             string  `json:"diskPath"`
	DiskTotalBytes       uint64  `json:"diskTotalBytes"`
	DiskAvailableBytes   uint64  `json:"diskAvailableBytes"`
	DiskUsedPercent      float64 `json:"diskUsedPercent"`
	MemoryTotalBytes     uint64  `json:"memoryTotalBytes"`
	MemoryAvailableBytes uint64  `json:"memoryAvailableBytes"`
	MemoryUsedPercent    float64 `json:"memoryUsedPercent"`
	Load1                float64 `json:"load1"`
	Load5                float64 `json:"load5"`
	Load15               float64 `json:"load15"`
	CPUCount             int     `json:"cpuCount"`
	Load1PerCPU          float64 `json:"load1PerCpu"`
	UptimeSeconds        float64 `json:"uptimeSeconds"`
}

type SystemReader interface {
	Snapshot(string) (SystemSnapshot, error)
}

func (o *Observer) observeSystem(_ context.Context, probe SystemProbe) domain.MonitorEvent {
	observedAt := o.now()
	event := domain.MonitorEvent{
		EventID:     fmt.Sprintf("%s:%s:%d", o.config.Source, probe.Name, observedAt.UnixNano()),
		ServiceID:   probe.ServiceID,
		Source:      o.config.Source,
		Status:      "unhealthy",
		Severity:    "critical",
		Summary:     probe.Name + " host resource metrics are unavailable",
		Fingerprint: "host-metrics-unavailable",
		ObservedAt:  observedAt,
	}
	snapshot, err := o.system.Snapshot(probe.DiskPath)
	if err != nil || !validSystemSnapshot(snapshot) {
		event.Details = json.RawMessage(`{"failure":"metrics_unavailable"}`)
		return event
	}
	type breach struct {
		name     string
		value    float64
		critical bool
	}
	var breaches []breach
	classify := func(name string, value, warning, critical float64) {
		if value >= critical {
			breaches = append(breaches, breach{name: name, value: value, critical: true})
		} else if value >= warning {
			breaches = append(breaches, breach{name: name, value: value})
		}
	}
	classify("disk", snapshot.DiskUsedPercent, probe.DiskWarningPercent, probe.DiskCriticalPercent)
	classify("memory", snapshot.MemoryUsedPercent, probe.MemoryWarningPercent, probe.MemoryCriticalPercent)
	classify("load", snapshot.Load1PerCPU, probe.LoadWarningPerCPU, probe.LoadCriticalPerCPU)
	details, _ := json.Marshal(map[string]any{
		"snapshot": snapshot,
		"thresholds": map[string]any{
			"diskWarningPercent": probe.DiskWarningPercent, "diskCriticalPercent": probe.DiskCriticalPercent,
			"memoryWarningPercent": probe.MemoryWarningPercent, "memoryCriticalPercent": probe.MemoryCriticalPercent,
			"loadWarningPerCpu": probe.LoadWarningPerCPU, "loadCriticalPerCpu": probe.LoadCriticalPerCPU,
		},
	})
	event.Details = details
	if len(breaches) == 0 {
		event.Status = "healthy"
		event.Severity = "info"
		event.Summary = fmt.Sprintf("%s resources are healthy (disk %.1f%%, memory %.1f%%, load %.2f/CPU)", probe.Name, snapshot.DiskUsedPercent, snapshot.MemoryUsedPercent, snapshot.Load1PerCPU)
		event.Fingerprint = ""
		return event
	}
	parts := make([]string, 0, len(breaches))
	critical := false
	for _, item := range breaches {
		unit := "%"
		if item.name == "load" {
			unit = "/CPU"
		}
		parts = append(parts, fmt.Sprintf("%s %.1f%s", item.name, item.value, unit))
		critical = critical || item.critical
	}
	event.Status = "degraded"
	event.Severity = "warning"
	if critical {
		event.Status = "unhealthy"
		event.Severity = "critical"
	}
	event.Summary = probe.Name + " resource threshold exceeded: " + strings.Join(parts, ", ")
	event.Fingerprint = "host-resources"
	return event
}

func validSystemSnapshot(snapshot SystemSnapshot) bool {
	percentValid := func(value float64) bool {
		return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
	}
	loadValid := func(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 }
	return snapshot.DiskTotalBytes > 0 && snapshot.DiskAvailableBytes <= snapshot.DiskTotalBytes && percentValid(snapshot.DiskUsedPercent) &&
		snapshot.MemoryTotalBytes > 0 && snapshot.MemoryAvailableBytes <= snapshot.MemoryTotalBytes && percentValid(snapshot.MemoryUsedPercent) &&
		snapshot.CPUCount > 0 && loadValid(snapshot.Load1) && loadValid(snapshot.Load5) && loadValid(snapshot.Load15) && loadValid(snapshot.Load1PerCPU) && loadValid(snapshot.UptimeSeconds)
}
