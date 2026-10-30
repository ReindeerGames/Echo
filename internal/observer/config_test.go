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
	if err == nil || !strings.Contains(err.Error(), "more than one probe") {
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
