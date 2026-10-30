package observer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observer.json")
	raw := `{
  "echoUrl":"http://echo.test:8080/",
  "source":"Observer-01",
  "allowInsecureHttp":true,
  "probes":[{"name":"Web","serviceId":"svc_123","url":"http://web.test/readyz"}]
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "observer-01" || cfg.Interval.Duration != 30*time.Second || cfg.Timeout.Duration != 5*time.Second {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.Probes[0].Method != "GET" || cfg.Probes[0].ExpectedMin != 200 || cfg.Probes[0].ExpectedMax != 299 {
		t.Fatalf("unexpected probe defaults: %#v", cfg.Probes[0])
	}
}

func TestConfigRejectsDuplicateServiceAndCredentials(t *testing.T) {
	_, err := normalizeConfig(Config{
		EchoURL:           "https://echo.test",
		Source:            "observer",
		AllowInsecureHTTP: false,
		Probes: []HTTPProbe{
			{Name: "one", ServiceID: "svc_1", URL: "https://user:secret@app.test/health"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("URL credentials were not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		Probes: []HTTPProbe{
			{Name: "one", ServiceID: "svc_1", URL: "https://one.test/health"},
			{Name: "two", ServiceID: "svc_1", URL: "https://two.test/health"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "one aggregate probe") {
		t.Fatalf("duplicate service probe was not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		Probes: []HTTPProbe{{Name: "one", ServiceID: "svc_1", URL: "https://one.test/health?token=secret"}},
	})
	if err == nil || !strings.Contains(err.Error(), "query strings") {
		t.Fatalf("URL query string was not rejected: %v", err)
	}
}

func TestSystemProbeDefaultsAndCrossTypeUniqueness(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		SystemProbes: []SystemProbe{{Name: "Host", ServiceID: "svc_host"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := cfg.SystemProbes[0]
	if probe.Name != "host" || probe.DiskPath != "/" || probe.DiskWarningPercent != 85 || probe.DiskCriticalPercent != 95 || probe.MemoryWarningPercent != 85 || probe.LoadCriticalPerCPU != 2 {
		t.Fatalf("unexpected system defaults: %#v", probe)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		Probes:       []HTTPProbe{{Name: "web", ServiceID: "svc_same", URL: "https://web.test/health"}},
		SystemProbes: []SystemProbe{{Name: "host", ServiceID: "svc_same"}},
	})
	if err == nil || !strings.Contains(err.Error(), "one aggregate probe") {
		t.Fatalf("cross-type duplicate service was not rejected: %v", err)
	}
}

func TestSystemProbeRejectsUnsafeThresholdsAndRelativePath(t *testing.T) {
	_, err := normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", SystemProbes: []SystemProbe{{Name: "host", ServiceID: "svc_host", DiskPath: "relative"}}})
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("relative disk path was not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", SystemProbes: []SystemProbe{{Name: "host", ServiceID: "svc_host", DiskWarningPercent: 95, DiskCriticalPercent: 90}}})
	if err == nil || !strings.Contains(err.Error(), "disk thresholds") {
		t.Fatalf("inverted disk thresholds were not rejected: %v", err)
	}
}
