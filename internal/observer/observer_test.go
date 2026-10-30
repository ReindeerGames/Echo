package observer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/monitor"
)

func TestRunOnceSendsSignedHealthyEventWithoutResponseBody(t *testing.T) {
	secret := "observer-test-secret-that-is-long-enough"
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		_, _ = w.Write([]byte("sensitive response content must not be reported"))
	}))
	defer probe.Close()

	var mutex sync.Mutex
	var received domain.MonitorEvent
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/webhooks/monitor" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if !monitor.VerifySignature(raw, r.Header.Get("X-Echo-Signature-256"), secret) {
			t.Fatal("observer signature was invalid")
		}
		var event domain.MonitorEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		mutex.Lock()
		received = event
		mutex.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer echo.Close()

	cfg := Config{EchoURL: echo.URL, Source: "local-observer", AllowInsecureHTTP: true, Probes: []HTTPProbe{{Name: "web", ServiceID: "svc_test", URL: probe.URL + "/readyz", ExpectedMin: 200, ExpectedMax: 299}}}
	service, err := New(cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	event := received
	mutex.Unlock()
	if event.Status != "healthy" || event.ServiceID != "svc_test" || event.EventID != "local-observer:web:1793361600000000000" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if strings.Contains(string(event.Details), "sensitive") {
		t.Fatalf("probe details leaked response content: %s", event.Details)
	}
}

func TestRunOnceReportsUnexpectedStatusAsUnhealthy(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database details", http.StatusServiceUnavailable)
	}))
	defer probe.Close()
	var received domain.MonitorEvent
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer echo.Close()
	service, err := New(Config{EchoURL: echo.URL, Source: "observer", AllowInsecureHTTP: true, Probes: []HTTPProbe{{Name: "api", ServiceID: "svc_api", URL: probe.URL}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if received.Status != "unhealthy" || received.Severity != "critical" || !strings.Contains(received.Summary, "503") || strings.Contains(string(received.Details), "database") {
		t.Fatalf("unexpected unhealthy event: %#v", received)
	}
}

func TestObserverDoesNotFollowRedirects(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer probe.Close()
	var received domain.MonitorEvent
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer echo.Close()
	service, err := New(Config{EchoURL: echo.URL, Source: "observer", AllowInsecureHTTP: true, Probes: []HTTPProbe{{Name: "api", ServiceID: "svc_api", URL: probe.URL}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if destinationCalls.Load() != 0 || received.Status != "unhealthy" || !strings.Contains(received.Summary, "302") {
		t.Fatalf("redirect was not treated as a failed probe: calls=%d event=%#v", destinationCalls.Load(), received)
	}
}
