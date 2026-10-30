package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCheckValidatesWithoutSecretProbeOrDelivery(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	path := writeObserverConfig(t, target.URL, target.URL, "web")
	var stdout, stderr bytes.Buffer
	secretReads := atomic.Int32{}
	err := run(t.Context(), []string{"-config", path, "-check"}, func(string) string {
		secretReads.Add(1)
		return ""
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "observer configuration is valid") || !strings.Contains(stdout.String(), "probes=1") || calls.Load() != 0 || secretReads.Load() != 0 {
		t.Fatalf("unsafe config check stdout=%q stderr=%q calls=%d secretReads=%d", stdout.String(), stderr.String(), calls.Load(), secretReads.Load())
	}
}

func TestDryRunPrintsSanitizedEvidenceWithoutSecretOrDelivery(t *testing.T) {
	var deliveryCalls atomic.Int32
	echo := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { deliveryCalls.Add(1) }))
	defer echo.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		_, _ = w.Write([]byte("sensitive response body must not be printed"))
	}))
	defer probe.Close()
	path := writeObserverConfig(t, echo.URL, probe.URL, "web")
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"-config", path, "-dry-run"}, func(string) string {
		t.Fatal("dry run read the webhook secret")
		return ""
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, `"status": "healthy"`) || !strings.Contains(output, `"healthy": 1`) || !strings.Contains(output, `"serviceId": "svc_web"`) || strings.Contains(output, `"id":`) || strings.Contains(output, `"receivedAt":`) || strings.Contains(output, "sensitive") || deliveryCalls.Load() != 0 {
		t.Fatalf("unsafe dry-run output=%s stderr=%q deliveryCalls=%d", output, stderr.String(), deliveryCalls.Load())
	}
}

func TestDryRunPrintsFailuresAndReturnsAttentionError(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("database password must not be printed"))
	}))
	defer probe.Close()
	path := writeObserverConfig(t, probe.URL, probe.URL, "web")
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"-config", path, "-dry-run"}, func(string) string { return "" }, &stdout, &stderr)
	if !errors.Is(err, errDryRunAttention) {
		t.Fatalf("dry run did not report attention: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, `"status": "unhealthy"`) || !strings.Contains(output, `"unhealthy": 1`) || !strings.Contains(output, "unexpected_status") || strings.Contains(output, "database password") {
		t.Fatalf("unsafe unhealthy dry-run output: %s", output)
	}
}

func TestObserverModesAreMutuallyExclusive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"-check", "-dry-run"}, func(string) string { return "" }, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("conflicting modes were accepted: %v", err)
	}
}

func writeObserverConfig(t *testing.T, echoURL, probeURL, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observer.json")
	raw := fmt.Sprintf(`{
  "echoUrl": %q,
  "source": "test-observer",
  "allowInsecureHttp": true,
  "probes": [{"name": %q, "serviceId": "svc_web", "url": %q}]
}`, echoURL, name, probeURL)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
