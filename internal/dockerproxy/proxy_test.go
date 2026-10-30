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
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/containers/database/json", nil))
	if response.Code != http.StatusNotFound || calls.Load() != 0 {
		t.Fatalf("unexpected response status=%d upstreamCalls=%d", response.Code, calls.Load())
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

func TestProxyRejectsOversizedInspectResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"State":{"Status":"running"},"padding":"` + strings.Repeat("x", maxInspectBytes) + `"}`
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

func TestProxyConfigurationRequiresSafeAllowlist(t *testing.T) {
	for _, containers := range [][]string{nil, {"web/../../secret"}, {""}} {
		if _, err := newWithClient(&http.Client{}, containers); err == nil {
			t.Fatalf("expected error for %#v", containers)
		}
	}
}
