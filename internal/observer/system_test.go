package observer

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

type fakeSystemReader struct {
	snapshot SystemSnapshot
	err      error
}

func (f fakeSystemReader) Snapshot(string) (SystemSnapshot, error) { return f.snapshot, f.err }

func TestSystemProbeSendsAggregateWarning(t *testing.T) {
	var received domain.MonitorEvent
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer echo.Close()
	service, err := New(Config{EchoURL: echo.URL, Source: "observer", AllowInsecureHTTP: true, SystemProbes: []SystemProbe{{Name: "primary-host", ServiceID: "svc_host"}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	service.system = fakeSystemReader{snapshot: SystemSnapshot{DiskPath: "/", DiskTotalBytes: 1000, DiskAvailableBytes: 100, DiskUsedPercent: 90, MemoryTotalBytes: 1000, MemoryAvailableBytes: 600, MemoryUsedPercent: 40, Load1: 0.8, Load5: 0.5, Load15: 0.25, Load1PerCPU: 0.2, CPUCount: 4, UptimeSeconds: 100}}
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if received.Status != "degraded" || received.Severity != "warning" || received.Fingerprint != "host-resources" || !strings.Contains(received.Summary, "disk 90.0%") {
		t.Fatalf("unexpected system event: %#v", received)
	}
}

func TestSystemProbeMetricsFailureIsAlertable(t *testing.T) {
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, SystemProbes: []SystemProbe{{Name: "primary-host", ServiceID: "svc_host"}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	service.system = fakeSystemReader{err: errors.New("sensitive local path")}
	event := service.observeSystem(t.Context(), service.config.SystemProbes[0])
	if event.Status != "unhealthy" || event.Severity != "critical" || event.Fingerprint != "host-metrics-unavailable" || strings.Contains(string(event.Details), "sensitive") {
		t.Fatalf("unexpected metrics failure event: %#v", event)
	}
}
