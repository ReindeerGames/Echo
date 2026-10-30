package observer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestDockerProbeSendsSanitizedHealthyEvidence(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/web-1/json" {
			t.Fatalf("unexpected Docker API path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Name":"/web-1","RestartCount":2,"State":{"Status":"running","Running":true,"StartedAt":"2026-10-30T10:00:00Z","Health":{"Status":"healthy","FailingStreak":0}},"Config":{"Env":["DATABASE_PASSWORD=must-not-leak"]}}`))
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
	service, err := New(Config{EchoURL: echo.URL, Source: "observer", AllowInsecureHTTP: true, DockerProbes: []DockerProbe{{Name: "web", ServiceID: "svc_web", ProxyURL: proxy.URL, Container: "web-1"}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if received.Status != "healthy" || received.Severity != "info" || received.Fingerprint != "container-health" || !strings.Contains(received.Summary, "restarts 2") || strings.Contains(string(received.Details), "must-not-leak") {
		t.Fatalf("unexpected Docker event: %#v", received)
	}
}

func TestDockerProbeClassifiesLifecycleWithoutTrustingRawText(t *testing.T) {
	base := domain.MonitorEvent{ServiceID: "svc_web", Source: "observer"}
	probe := DockerProbe{Name: "web", ServiceID: "svc_web", Container: "web"}
	tests := []struct {
		name        string
		inspect     dockerInspect
		status      string
		severity    string
		fingerprint string
		contains    string
	}{
		{name: "oom", inspect: dockerInspect{State: dockerState{Status: "running", Running: true, OOMKilled: true}}, status: "unhealthy", severity: "critical", fingerprint: "container-health", contains: "OOM-killed"},
		{name: "paused", inspect: dockerInspect{State: dockerState{Status: "paused", Running: true, Paused: true}}, status: "degraded", severity: "warning", fingerprint: "container-health", contains: "paused"},
		{name: "health failure", inspect: dockerInspect{State: dockerState{Status: "running", Running: true, Health: &dockerHealth{Status: "unhealthy", FailingStreak: 3}}}, status: "unhealthy", severity: "critical", fingerprint: "container-health", contains: "streak 3"},
		{name: "untrusted state", inspect: dockerInspect{State: dockerState{Status: "SECRET_TEXT", Running: false}}, status: "unhealthy", severity: "critical", fingerprint: "container-health", contains: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := dockerEventFromInspect(base, probe, test.inspect)
			if event.Status != test.status || event.Severity != test.severity || event.Fingerprint != test.fingerprint || !strings.Contains(event.Summary, test.contains) || strings.Contains(event.Summary, "SECRET_TEXT") || strings.Contains(string(event.Details), "SECRET_TEXT") {
				t.Fatalf("unexpected event: %#v", event)
			}
		})
	}
}

func TestDockerProbeAddsSanitizedResourceEvidence(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/web/json":
			_, _ = w.Write([]byte(`{"Name":"web","State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}`))
		case "/containers/web/stats":
			_, _ = w.Write([]byte(`{"cpuAvailable":true,"cpuPercent":92.5,"memoryAvailable":true,"memoryUsageBytes":104857600,"memoryLimitBytes":209715200,"memoryUsedPercent":50,"pidsCurrent":18,"networkInterfaces":{"eth0":"must-not-leak"}}`))
		default:
			t.Fatalf("unexpected proxy path %s", r.URL.Path)
		}
	}))
	defer proxy.Close()
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, DockerProbes: []DockerProbe{{Name: "web", ServiceID: "svc_web", ProxyURL: proxy.URL, Container: "web", CPUWarningPercent: 80, CPUCriticalPercent: 120, MemoryWarningPercent: 85, MemoryCriticalPercent: 95}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	event := service.observeDocker(t.Context(), service.config.DockerProbes[0])
	if event.Status != "degraded" || event.Severity != "warning" || !strings.Contains(event.Summary, "CPU 92.50%") {
		t.Fatalf("unexpected Docker resource event: %#v", event)
	}
	details := string(event.Details)
	for _, expected := range []string{`"cpuPercent":92.5`, `"memoryUsageBytes":104857600`, `"memoryLimitBytes":209715200`, `"memoryUsedPercent":50`, `"pidsCurrent":18`, `"cpuWarningPercent":80`, `"memoryCriticalPercent":95`} {
		if !strings.Contains(details, expected) {
			t.Fatalf("resource evidence missing %s: %s", expected, details)
		}
	}
	if strings.Contains(details, "eth0") || strings.Contains(details, "must-not-leak") {
		t.Fatalf("unrecognized proxy evidence leaked: %s", details)
	}
}

func TestDockerResourceThresholdClassification(t *testing.T) {
	base := domain.MonitorEvent{Status: "healthy", Severity: "info", Details: json.RawMessage(`{"container":"web","state":"running"}`)}
	probe := DockerProbe{Name: "web", Container: "web", CPUWarningPercent: 80, CPUCriticalPercent: 120, MemoryWarningPercent: 85, MemoryCriticalPercent: 95}
	tests := []struct {
		name     string
		stats    dockerStats
		status   string
		severity string
		contains string
	}{
		{name: "healthy", stats: dockerStats{CPUAvailable: true, CPUPercent: 20, MemoryAvailable: true, MemoryLimitBytes: 100, MemoryUsedPercent: 30}, status: "healthy", severity: "info", contains: ""},
		{name: "cpu warning", stats: dockerStats{CPUAvailable: true, CPUPercent: 90, MemoryAvailable: true, MemoryLimitBytes: 100, MemoryUsedPercent: 30}, status: "degraded", severity: "warning", contains: "CPU 90.00%"},
		{name: "memory critical", stats: dockerStats{CPUAvailable: true, CPUPercent: 20, MemoryAvailable: true, MemoryLimitBytes: 100, MemoryUsedPercent: 97}, status: "unhealthy", severity: "critical", contains: "memory 97.00%"},
		{name: "both critical", stats: dockerStats{CPUAvailable: true, CPUPercent: 130, MemoryAvailable: true, MemoryLimitBytes: 100, MemoryUsedPercent: 99}, status: "unhealthy", severity: "critical", contains: "CPU 130.00% and memory 99.00%"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := dockerEventWithResources(base, probe, test.stats)
			if event.Status != test.status || event.Severity != test.severity || (test.contains != "" && !strings.Contains(event.Summary, test.contains)) {
				t.Fatalf("unexpected event: %#v", event)
			}
		})
	}
}

func TestDockerResourceFailuresAreFixedAndDoNotExposeProxyBody(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"State":{"Status":"running","Running":true}}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`socket=/var/run/docker.sock token=must-not-leak`))
	}))
	defer proxy.Close()
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, DockerProbes: []DockerProbe{{Name: "web", ServiceID: "svc_web", ProxyURL: proxy.URL, Container: "web", CPUWarningPercent: 80, CPUCriticalPercent: 120}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	event := service.observeDocker(t.Context(), service.config.DockerProbes[0])
	if event.Status != "unhealthy" || !strings.Contains(string(event.Details), `"failure":"stats_status"`) || strings.Contains(event.Summary+string(event.Details), "must-not-leak") {
		t.Fatalf("unsafe Docker stats failure event: %#v", event)
	}
}

func TestDockerResourceProbeRequiresConfiguredMetrics(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"State":{"Status":"running","Running":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"cpuAvailable":false,"memoryAvailable":false}`))
	}))
	defer proxy.Close()
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, DockerProbes: []DockerProbe{{Name: "web", ServiceID: "svc_web", ProxyURL: proxy.URL, Container: "web", CPUWarningPercent: 80, CPUCriticalPercent: 120}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	event := service.observeDocker(t.Context(), service.config.DockerProbes[0])
	if event.Status != "unhealthy" || !strings.Contains(string(event.Details), `"failure":"cpu_metrics_unavailable"`) {
		t.Fatalf("missing configured CPU evidence was accepted: %#v", event)
	}
}

func TestDockerProxyFailureProducesAlertableGenericEvidence(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "socket path and secret", http.StatusForbidden)
	}))
	defer proxy.Close()
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, DockerProbes: []DockerProbe{{Name: "web", ServiceID: "svc_web", ProxyURL: proxy.URL, Container: "web"}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	event := service.observeDocker(t.Context(), service.config.DockerProbes[0])
	if event.Status != "unhealthy" || event.Fingerprint != "container-health" || strings.Contains(string(event.Details), "socket") || !strings.Contains(string(event.Details), "inspect_status") {
		t.Fatalf("unexpected proxy failure event: %#v", event)
	}
}
