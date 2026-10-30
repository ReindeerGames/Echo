package observer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestProcessProbeSendsOnlyAggregateMetadata(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/processes/invoice-worker" {
			t.Fatalf("unexpected process proxy request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(ProcessSnapshot{ProcessCount: 1, SleepingCount: 1, ThreadCount: 8, RSSBytes: 96 << 20})
	}))
	defer proxy.Close()
	var received domain.MonitorEvent
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer echo.Close()
	service, err := New(Config{EchoURL: echo.URL, Source: "observer", AllowInsecureHTTP: true, ProcessProbes: []ProcessProbe{{Name: "invoice-worker", ServiceID: "svc_worker", ProxyURL: proxy.URL, ProcessName: "invoice-worker", MinimumCount: 1, MaximumCount: 2, RSSWarningMiB: 256, RSSCriticalMiB: 512}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if received.Status != "healthy" || received.Severity != "info" || received.Fingerprint != "process-health" {
		t.Fatalf("unexpected process event: %#v", received)
	}
	details := string(received.Details)
	for _, expected := range []string{`"processName":"invoice-worker"`, `"processCount":1`, `"sleepingCount":1`, `"threadCount":8`, `"rssBytes":100663296`, `"minimumCount":1`, `"maximumCount":2`, `"rssWarningMiB":256`, `"rssCriticalMiB":512`} {
		if !strings.Contains(details, expected) {
			t.Fatalf("process evidence missing %q: %s", expected, details)
		}
	}
	for _, forbidden := range []string{"pid", "uid", "command", "argument", "environment", "username", "log"} {
		if strings.Contains(strings.ToLower(details), forbidden) {
			t.Fatalf("process evidence exposed %q: %s", forbidden, details)
		}
	}
}

func TestProcessProbeClassifiesMissingZombieCountsAndMemory(t *testing.T) {
	var snapshot ProcessSnapshot
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(snapshot)
	}))
	defer proxy.Close()
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, ProcessProbes: []ProcessProbe{{Name: "worker", ServiceID: "svc_worker", ProxyURL: proxy.URL, ProcessName: "worker", MinimumCount: 1, MaximumCount: 2, RSSWarningMiB: 100, RSSCriticalMiB: 200}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		snapshot ProcessSnapshot
		status   string
		severity string
		summary  string
	}{
		{name: "missing", snapshot: ProcessSnapshot{}, status: "unhealthy", severity: "critical", summary: "below minimum"},
		{name: "zombie", snapshot: ProcessSnapshot{ProcessCount: 1, ZombieCount: 1, ThreadCount: 1}, status: "unhealthy", severity: "critical", summary: "zombie"},
		{name: "too many", snapshot: ProcessSnapshot{ProcessCount: 3, SleepingCount: 3, ThreadCount: 3}, status: "degraded", severity: "warning", summary: "above maximum"},
		{name: "stopped", snapshot: ProcessSnapshot{ProcessCount: 1, StoppedCount: 1, ThreadCount: 1}, status: "degraded", severity: "warning", summary: "stopped"},
		{name: "memory warning", snapshot: ProcessSnapshot{ProcessCount: 1, SleepingCount: 1, ThreadCount: 1, RSSBytes: 150 << 20}, status: "degraded", severity: "warning", summary: "warning threshold"},
		{name: "memory critical", snapshot: ProcessSnapshot{ProcessCount: 1, SleepingCount: 1, ThreadCount: 1, RSSBytes: 250 << 20}, status: "unhealthy", severity: "critical", summary: "critical threshold"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot = test.snapshot
			event := service.observeProcess(t.Context(), service.config.ProcessProbes[0])
			if event.Status != test.status || event.Severity != test.severity || !strings.Contains(event.Summary, test.summary) {
				t.Fatalf("unexpected process event: %#v", event)
			}
		})
	}
}

func TestProcessProbeFailureDoesNotExposeProxyBody(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`/proc/123/environ token=must-not-leak`))
	}))
	defer proxy.Close()
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, ProcessProbes: []ProcessProbe{{Name: "worker", ServiceID: "svc_worker", ProxyURL: proxy.URL, ProcessName: "worker"}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	event := service.observeProcess(t.Context(), service.config.ProcessProbes[0])
	if event.Status != "unhealthy" || event.Severity != "critical" || !strings.Contains(string(event.Details), `"failure":"process_inspect_failed"`) || strings.Contains(event.Summary+string(event.Details), "must-not-leak") {
		t.Fatalf("unsafe process failure event: %#v", event)
	}
}

func TestProcessProbeRejectsInvalidOrOversizedProxyResponse(t *testing.T) {
	responses := []string{`{"processCount":1,"runningCount":2,"threadCount":1}`, strings.Repeat("x", maxResponseBytes+1)}
	for _, body := range responses {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, ProcessProbes: []ProcessProbe{{Name: "worker", ServiceID: "svc_worker", ProxyURL: proxy.URL, ProcessName: "worker"}}}, "observer-test-secret-that-is-long-enough")
		if err != nil {
			proxy.Close()
			t.Fatal(err)
		}
		event := service.observeProcess(t.Context(), service.config.ProcessProbes[0])
		proxy.Close()
		if event.Status != "unhealthy" || !strings.Contains(string(event.Details), `"failure":"`) {
			t.Fatalf("invalid proxy response accepted: %#v", event)
		}
	}
}
