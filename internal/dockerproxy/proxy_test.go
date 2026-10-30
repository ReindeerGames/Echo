package dockerproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestProxyExposesOnlySanitizedAllowedInspect(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/containers/web-1/json" {
			t.Fatalf("unexpected Docker request %s %s", request.Method, request.URL.Path)
		}
		body := `{"Name":"/web-1","RestartCount":-2,"State":{"Status":"RUNNING","Running":true,"Error":"database password is secret","StartedAt":"2026-10-30T10:00:00+02:00","FinishedAt":"not-a-time","Health":{"Status":"HEALTHY","FailingStreak":-4,"Log":[{"Output":"token"}]}},"Config":{"Env":["PASSWORD=secret"],"Labels":{"private":"secret"}},"Mounts":[{"Source":"/secret/path"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	proxy, err := newWithClient(client, []string{"web-1"})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/containers/web-1/json", nil)
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	got := response.Body.String()
	for _, secret := range []string{"PASSWORD", "database password", "private", "/secret/path", "token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized response leaked %q: %s", secret, got)
		}
	}
	for _, expected := range []string{`"Name":"web-1"`, `"RestartCount":0`, `"Status":"running"`, `"Error":"present"`, `"StartedAt":"2026-10-30T08:00:00Z"`, `"FailingStreak":0`} {
		if !strings.Contains(got, expected) {
			t.Fatalf("response missing %s: %s", expected, got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one upstream call, got %d", calls.Load())
	}
}

func TestProxyRejectsUnallowedContainersWithoutDockerRequest(t *testing.T) {
	var calls atomic.Int32
	proxy, err := newWithClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	})}, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/containers/database/json", "/containers/database/stats"} {
		response := httptest.NewRecorder()
		proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound || calls.Load() != 0 {
			t.Fatalf("unexpected response path=%s status=%d upstreamCalls=%d", path, response.Code, calls.Load())
		}
	}
}

func TestProxyRejectsUnsupportedMethodsAndRoutes(t *testing.T) {
	proxy, err := newWithClient(&http.Client{}, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/containers/web/json", nil),
		httptest.NewRequest(http.MethodGet, "/containers/json", nil),
		httptest.NewRequest(http.MethodGet, "/version", nil),
	} {
		response := httptest.NewRecorder()
		proxy.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed && response.Code != http.StatusNotFound {
			t.Fatalf("unexpected status %d for %s %s", response.Code, request.Method, request.URL.Path)
		}
	}
}

func TestProxyExposesOnlySanitizedAllowedStats(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/containers/web/stats" || request.URL.Query().Get("stream") != "false" || len(request.URL.Query()) != 1 {
			t.Fatalf("unexpected Docker stats request %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		body := `{"cpu_stats":{"cpu_usage":{"total_usage":1600,"percpu_usage":[800,800]},"system_cpu_usage":5000,"online_cpus":2},"precpu_stats":{"cpu_usage":{"total_usage":1000},"system_cpu_usage":4000},"memory_stats":{"usage":104857600,"limit":209715200,"stats":{"inactive_file":10485760}},"pids_stats":{"current":12},"networks":{"eth0":{"rx_bytes":123456}},"secret":"must-not-leak"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	proxy, err := newWithClient(client, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/containers/web/stats", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"cpuAvailable":true`, `"cpuPercent":120`, `"memoryAvailable":true`, `"memoryUsageBytes":94371840`, `"memoryLimitBytes":209715200`, `"memoryUsedPercent":45`, `"pidsCurrent":12`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("stats response missing %s: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"eth0", "rx_bytes", "secret", "must-not-leak", "total_usage", "system_cpu_usage"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("stats response exposed %q: %s", forbidden, body)
		}
	}
}

func TestSanitizeStatsSupportsCgroupV1AndUnavailableCounters(t *testing.T) {
	var raw rawStats
	raw.MemoryStats.Usage = 1000
	raw.MemoryStats.Limit = 2000
	raw.MemoryStats.Stats = map[string]uint64{"cache": 900, "inactive_file": 300, "total_inactive_file": 200}
	view := sanitizeStats(raw)
	if view.CPUAvailable || !view.MemoryAvailable || view.MemoryUsageBytes != 800 || view.MemoryUsedPercent != 40 {
		t.Fatalf("unexpected sanitized stats: %#v", view)
	}
}

func TestProxyRejectsOversizedInspectResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"State":{"Status":"running"},"padding":"` + strings.Repeat("x", maxDockerResponseBytes) + `"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), ContentLength: -1}, nil
	})}
	proxy, err := newWithClient(client, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/containers/web/json", nil))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "inspect_response_too_large") {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
}

func TestProxyCollapsesStatsFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "invalid", body: `{`, want: "invalid_stats_response"},
		{name: "oversized", body: strings.Repeat("x", maxDockerResponseBytes+1), want: "stats_response_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header), ContentLength: -1}, nil
			})}
			proxy, err := newWithClient(client, []string{"web"})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/containers/web/stats", nil))
			if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProxyConfigurationRequiresSafeAllowlist(t *testing.T) {
	for _, containers := range [][]string{nil, {"web/../../secret"}, {""}} {
		if _, err := newWithClient(&http.Client{}, containers); err == nil {
			t.Fatalf("expected error for %#v", containers)
		}
	}
}
