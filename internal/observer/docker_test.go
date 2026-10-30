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
	if received.Status != "healthy" || received.Severity != "info" || !strings.Contains(received.Summary, "restarts 2") || strings.Contains(string(received.Details), "must-not-leak") {
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
		{name: "oom", inspect: dockerInspect{State: dockerState{Status: "running", Running: true, OOMKilled: true}}, status: "unhealthy", severity: "critical", fingerprint: "container-runtime", contains: "OOM-killed"},
		{name: "paused", inspect: dockerInspect{State: dockerState{Status: "paused", Running: true, Paused: true}}, status: "degraded", severity: "warning", fingerprint: "container-runtime", contains: "paused"},
		{name: "health failure", inspect: dockerInspect{State: dockerState{Status: "running", Running: true, Health: &dockerHealth{Status: "unhealthy", FailingStreak: 3}}}, status: "unhealthy", severity: "critical", fingerprint: "container-health", contains: "streak 3"},
		{name: "untrusted state", inspect: dockerInspect{State: dockerState{Status: "SECRET_TEXT", Running: false}}, status: "unhealthy", severity: "critical", fingerprint: "container-runtime", contains: "unknown"},
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
	if event.Status != "unhealthy" || event.Fingerprint != "container-observer" || strings.Contains(string(event.Details), "socket") || !strings.Contains(string(event.Details), "inspect_status") {
		t.Fatalf("unexpected proxy failure event: %#v", event)
	}
}
