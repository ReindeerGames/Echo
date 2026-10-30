package observer

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

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

func TestCollectorRunsWithoutSecretOrDeliveryAndPreservesConfigOrder(t *testing.T) {
	var deliveryCalls atomic.Int32
	echo := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { deliveryCalls.Add(1) }))
	defer echo.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(20 * time.Millisecond)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probe.Close()
	service, err := NewCollector(Config{
		EchoURL: echo.URL, Source: "dry-run-observer", AllowInsecureHTTP: true,
		Probes: []HTTPProbe{
			{Name: "slow", ServiceID: "svc_slow", URL: probe.URL + "/slow"},
			{Name: "fast", ServiceID: "svc_fast", URL: probe.URL + "/fast"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.CollectOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ServiceID != "svc_slow" || events[1].ServiceID != "svc_fast" || events[0].Status != "healthy" || events[1].Status != "healthy" {
		t.Fatalf("collector did not preserve configured order: %#v", events)
	}
	if deliveryCalls.Load() != 0 {
		t.Fatalf("collector delivered %d events", deliveryCalls.Load())
	}
	if err := service.RunOnce(t.Context()); err == nil || !strings.Contains(err.Error(), "delivery is disabled") {
		t.Fatalf("delivery-disabled collector ran a delivery cycle: %v", err)
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

func TestHTTPSProbeReportsCertificateExpiryBeforeOutage(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		expiresIn    time.Duration
		wantStatus   string
		wantSeverity string
		wantSummary  string
	}{
		{name: "warning", expiresIn: 20 * 24 * time.Hour, wantStatus: "degraded", wantSeverity: "warning", wantSummary: "expires in 20 days"},
		{name: "critical", expiresIn: 6 * 24 * time.Hour, wantStatus: "unhealthy", wantSeverity: "critical", wantSummary: "expires in 6 days"},
		{name: "healthy", expiresIn: 45 * 24 * time.Hour, wantStatus: "healthy", wantSeverity: "info", wantSummary: "is healthy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := New(Config{
				EchoURL: "https://echo.test", Source: "observer",
				Probes: []HTTPProbe{{Name: "web", ServiceID: "svc_web", URL: "https://web.test/readyz"}},
			}, "observer-test-secret-that-is-long-enough")
			if err != nil {
				t.Fatal(err)
			}
			service.now = func() time.Time { return now }
			service.probe = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Body:       io.NopCloser(strings.NewReader("")),
					TLS:        &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{NotAfter: now.Add(test.expiresIn)}}},
				}, nil
			})}
			event := service.observe(t.Context(), service.config.Probes[0])
			if event.Status != test.wantStatus || event.Severity != test.wantSeverity || !strings.Contains(event.Summary, test.wantSummary) {
				t.Fatalf("unexpected TLS expiry event: %#v", event)
			}
			if !strings.Contains(string(event.Details), `"expiresAt":"2026-`) || !strings.Contains(string(event.Details), `"tlsWarningDays":30`) || !strings.Contains(string(event.Details), `"tlsCriticalDays":7`) {
				t.Fatalf("TLS evidence missing from event: %s", event.Details)
			}
			if event.Fingerprint != "http-health" {
				t.Fatalf("certificate alert did not use aggregate HTTP fingerprint: %#v", event)
			}
		})
	}
}
