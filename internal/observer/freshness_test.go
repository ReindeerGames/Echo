package observer

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/monitor"
)

func TestRunOnceSendsSignedFreshnessEvent(t *testing.T) {
	secret := "observer-test-secret-that-is-long-enough"
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	artifact := filepath.Join(t.TempDir(), "database.sql.gz")
	if err := os.WriteFile(artifact, []byte("must not be transmitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(artifact, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var received domain.MonitorEvent
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if !monitor.VerifySignature(raw, r.Header.Get("X-Echo-Signature-256"), secret) {
			t.Fatal("freshness event signature was invalid")
		}
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer echo.Close()
	service, err := New(Config{
		EchoURL: echo.URL, Source: "observer", AllowInsecureHTTP: true,
		FreshnessProbes: []FreshnessProbe{{Name: "database-backup", ServiceID: "svc_backup", Path: artifact}},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if received.ServiceID != "svc_backup" || received.Status != "healthy" || strings.Contains(string(received.Details), artifact) || strings.Contains(string(received.Details), "must not be transmitted") {
		t.Fatalf("unexpected delivered freshness event: %#v", received)
	}
}

func TestFreshnessProbeUsesNewestMatchingFileWithoutExposingNames(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	older := filepath.Join(directory, "database-older.sql.gz")
	latest := filepath.Join(directory, "database-latest.sql.gz")
	unrelated := filepath.Join(directory, "activity.log")
	for _, path := range []string{older, latest, unrelated} {
		if err := os.WriteFile(path, []byte("contents must never be read"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(older, now.Add(-60*time.Hour), now.Add(-60*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(latest, now.Add(-30*time.Hour), now.Add(-30*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelated, now, now); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		EchoURL: "https://echo.test", Source: "observer",
		FreshnessProbes: []FreshnessProbe{{Name: "database-backup", ServiceID: "svc_backup", Path: directory, MatchSuffix: ".sql.gz"}},
	}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	event := service.observeFreshness(t.Context(), service.config.FreshnessProbes[0])
	if event.Status != "degraded" || event.Severity != "warning" || event.Fingerprint != "artifact-freshness" || !strings.Contains(event.Summary, "30h0m0s ago") {
		t.Fatalf("unexpected stale backup event: %#v", event)
	}
	details := string(event.Details)
	if !strings.Contains(details, `"matchedFiles":2`) || !strings.Contains(details, `"ageSeconds":108000`) || !strings.Contains(details, `"warningAgeSeconds":93600`) {
		t.Fatalf("freshness evidence missing: %s", details)
	}
	for _, forbidden := range []string{directory, "database-latest", "activity.log", "contents"} {
		if strings.Contains(details, forbidden) || strings.Contains(event.Summary, forbidden) {
			t.Fatalf("freshness event exposed %q: %#v", forbidden, event)
		}
	}
	if err := os.Chtimes(latest, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event = service.observeFreshness(t.Context(), service.config.FreshnessProbes[0])
	if event.Status != "healthy" || event.Severity != "info" || event.Fingerprint != "" {
		t.Fatalf("fresh backup did not resolve health: %#v", event)
	}
}

func TestFreshnessProbeReportsMissingNoMatchAndFutureTimestamp(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	probe := FreshnessProbe{Name: "backup", ServiceID: "svc_backup", Path: filepath.Join(directory, "missing"), WarningAge: Duration{time.Hour}, CriticalAge: Duration{2 * time.Hour}}
	service := &Observer{config: Config{Source: "observer"}, now: func() time.Time { return now }}
	event := service.observeFreshness(t.Context(), probe)
	if event.Status != "unhealthy" || !strings.Contains(event.Summary, "path is missing") || !strings.Contains(string(event.Details), `"failure":"path_missing"`) {
		t.Fatalf("missing path was not classified safely: %#v", event)
	}
	probe.Path = directory
	probe.MatchSuffix = ".backup"
	event = service.observeFreshness(t.Context(), probe)
	if !strings.Contains(event.Summary, "no matching backup artifacts") || !strings.Contains(string(event.Details), `"failure":"no_matching_files"`) {
		t.Fatalf("empty backup directory was not classified safely: %#v", event)
	}
	future := filepath.Join(directory, "future.backup")
	if err := os.WriteFile(future, []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(future, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	event = service.observeFreshness(t.Context(), probe)
	if event.Status != "degraded" || event.Severity != "warning" || !strings.Contains(event.Summary, "timestamp is in the future") || !strings.Contains(string(event.Details), `"failure":"future_timestamp"`) {
		t.Fatalf("future timestamp was not classified safely: %#v", event)
	}
	link := filepath.Join(directory, "latest.backup")
	if err := os.Symlink(future, link); err != nil {
		t.Fatal(err)
	}
	probe.Path = link
	probe.MatchSuffix = ""
	event = service.observeFreshness(t.Context(), probe)
	if event.Status != "unhealthy" || !strings.Contains(string(event.Details), `"failure":"unsupported_path"`) {
		t.Fatalf("freshness probe followed or accepted a symlink: %#v", event)
	}
}

func TestFreshnessDirectoryScanIsBounded(t *testing.T) {
	directory := t.TempDir()
	for index := 0; index <= maxFreshnessDirectoryEntries; index++ {
		path := filepath.Join(directory, "backup-"+time.Unix(int64(index), 0).UTC().Format("150405.000000000")+".bak")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := latestFreshnessTimestamp(directory, ".bak")
	if !errors.Is(err, errFreshnessScanLimit) {
		t.Fatalf("unbounded directory scan was not rejected: %v", err)
	}
}
