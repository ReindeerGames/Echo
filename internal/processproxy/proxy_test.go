package processproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeScanner struct {
	snapshot Snapshot
	err      error
	name     string
}

func (f *fakeScanner) Snapshot(_ context.Context, name string) (Snapshot, error) {
	f.name = name
	return f.snapshot, f.err
}

func TestProxyExposesOnlyAllowedAggregateSnapshot(t *testing.T) {
	scanner := &fakeScanner{snapshot: Snapshot{ProcessCount: 2, RunningCount: 1, SleepingCount: 1, ThreadCount: 14, RSSBytes: 384 << 20}}
	proxy, err := newWithScanner(scanner, []string{"app-worker"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/processes/app-worker", nil))
	if response.Code != http.StatusOK || scanner.name != "app-worker" {
		t.Fatalf("unexpected response code=%d body=%s scanner=%#v", response.Code, response.Body.String(), scanner)
	}
	body := response.Body.String()
	for _, expected := range []string{`"processCount":2`, `"runningCount":1`, `"sleepingCount":1`, `"threadCount":14`, `"rssBytes":402653184`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response missing %s: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"pid", "uid", "argument", "environment", "command", "username", "path", "log"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, body)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing: %#v", response.Header())
	}
}

func TestProxyRejectsUnallowedNamesAndUnsupportedRoutes(t *testing.T) {
	scanner := &fakeScanner{}
	proxy, err := newWithScanner(scanner, []string{"app-worker"})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/processes/mariadbd", nil),
		httptest.NewRequest(http.MethodPost, "/processes/app-worker", nil),
		httptest.NewRequest(http.MethodGet, "/proc/1/environ", nil),
	} {
		response := httptest.NewRecorder()
		proxy.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("unexpected status %d for %s %s", response.Code, request.Method, request.URL.Path)
		}
	}
	if scanner.name != "" {
		t.Fatalf("scanner was called for a rejected route: %q", scanner.name)
	}
}

func TestProxyCollapsesScannerErrorsAndInvalidSnapshots(t *testing.T) {
	for _, scanner := range []*fakeScanner{
		{err: errors.New("/proc/123/environ token=must-not-leak")},
		{snapshot: Snapshot{ProcessCount: 1, RunningCount: 2, ThreadCount: 1}},
	} {
		proxy, err := newWithScanner(scanner, []string{"worker"})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/processes/worker", nil))
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "process_scan_failed") || strings.Contains(response.Body.String(), "must-not-leak") {
			t.Fatalf("unsafe scanner failure response: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestProxyConfigurationRequiresExactSafeAllowlist(t *testing.T) {
	for _, names := range [][]string{nil, {""}, {"worker/../../secret"}, {"process-name-is-too-long"}} {
		if _, err := newWithScanner(&fakeScanner{}, names); err == nil {
			t.Fatalf("unsafe allowlist accepted: %#v", names)
		}
	}
}
