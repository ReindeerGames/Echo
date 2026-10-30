package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

const maxFreshnessDirectoryEntries = 1000

var (
	errFreshnessNoMatch     = errors.New("no matching regular files")
	errFreshnessScanLimit   = errors.New("freshness directory scan limit exceeded")
	errFreshnessUnsupported = errors.New("freshness path is not a regular file or directory")
)

func (o *Observer) observeFreshness(ctx context.Context, probe FreshnessProbe) domain.MonitorEvent {
	observedAt := o.now()
	event := domain.MonitorEvent{
		EventID: fmt.Sprintf("%s:%s:%d", o.config.Source, probe.Name, observedAt.UnixNano()), ServiceID: probe.ServiceID,
		Source: o.config.Source, Status: "unhealthy", Severity: "critical", Summary: probe.Name + " freshness could not be verified",
		Fingerprint: "artifact-freshness", ObservedAt: observedAt,
	}
	if err := ctx.Err(); err != nil {
		event.Details = freshnessDetails(probe, time.Time{}, 0, "probe_cancelled", observedAt)
		return event
	}
	modifiedAt, matched, err := latestFreshnessTimestamp(probe.Path, probe.MatchSuffix)
	if err != nil {
		failure := classifyFreshnessError(err)
		switch failure {
		case "path_missing":
			event.Summary = probe.Name + " freshness path is missing"
		case "no_matching_files":
			event.Summary = probe.Name + " has no matching backup artifacts"
		case "scan_limit":
			event.Summary = probe.Name + " freshness scan exceeded its safe entry limit"
		default:
			event.Summary = probe.Name + " freshness metadata is unavailable"
		}
		event.Details = freshnessDetails(probe, time.Time{}, matched, failure, observedAt)
		return event
	}
	age := observedAt.Sub(modifiedAt)
	if age < -5*time.Minute {
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = probe.Name + " freshness timestamp is in the future"
		event.Details = freshnessDetails(probe, modifiedAt, matched, "future_timestamp", observedAt)
		return event
	}
	if age < 0 {
		age = 0
	}
	event.Details = freshnessDetails(probe, modifiedAt, matched, "", observedAt)
	if age >= probe.CriticalAge.Duration {
		event.Summary = fmt.Sprintf("%s is critically stale (last update %s ago)", probe.Name, freshnessAge(age))
		return event
	}
	if age >= probe.WarningAge.Duration {
		event.Status = "degraded"
		event.Severity = "warning"
		event.Summary = fmt.Sprintf("%s is stale (last update %s ago)", probe.Name, freshnessAge(age))
		return event
	}
	event.Status = "healthy"
	event.Severity = "info"
	event.Summary = fmt.Sprintf("%s is fresh (last update %s ago)", probe.Name, freshnessAge(age))
	event.Fingerprint = ""
	return event
}

func latestFreshnessTimestamp(path, suffix string) (time.Time, int, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return time.Time{}, 0, errFreshnessUnsupported
	}
	if info.Mode().IsRegular() {
		if suffix != "" && !strings.HasSuffix(filepath.Base(path), suffix) {
			return time.Time{}, 0, errFreshnessNoMatch
		}
		return info.ModTime().UTC(), 1, nil
	}
	if !info.IsDir() {
		return time.Time{}, 0, errFreshnessUnsupported
	}
	directory, err := os.Open(path)
	if err != nil {
		return time.Time{}, 0, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maxFreshnessDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return time.Time{}, 0, err
	}
	if len(entries) > maxFreshnessDirectoryEntries {
		return time.Time{}, 0, errFreshnessScanLimit
	}
	var latest time.Time
	matched := 0
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || (suffix != "" && !strings.HasSuffix(entry.Name(), suffix)) {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return time.Time{}, matched, err
		}
		if !entryInfo.Mode().IsRegular() {
			continue
		}
		matched++
		if entryInfo.ModTime().After(latest) {
			latest = entryInfo.ModTime().UTC()
		}
	}
	if matched == 0 {
		return time.Time{}, 0, errFreshnessNoMatch
	}
	return latest, matched, nil
}

func classifyFreshnessError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "path_missing"
	case errors.Is(err, errFreshnessNoMatch):
		return "no_matching_files"
	case errors.Is(err, errFreshnessScanLimit):
		return "scan_limit"
	case errors.Is(err, errFreshnessUnsupported):
		return "unsupported_path"
	default:
		return "path_unavailable"
	}
}

func freshnessDetails(probe FreshnessProbe, modifiedAt time.Time, matched int, failure string, observedAt time.Time) json.RawMessage {
	value := map[string]any{
		"probe": probe.Name, "matchedFiles": matched,
		"thresholds": map[string]any{"warningAgeSeconds": int64(probe.WarningAge.Duration / time.Second), "criticalAgeSeconds": int64(probe.CriticalAge.Duration / time.Second)},
	}
	if !modifiedAt.IsZero() {
		age := observedAt.Sub(modifiedAt)
		if age < 0 {
			age = 0
		}
		value["modifiedAt"] = modifiedAt.UTC()
		value["ageSeconds"] = int64(age / time.Second)
	}
	if failure != "" {
		value["failure"] = failure
	}
	raw, _ := json.Marshal(value)
	return raw
}

func freshnessAge(age time.Duration) string {
	if age < time.Minute {
		return "less than a minute"
	}
	return age.Round(time.Minute).String()
}
