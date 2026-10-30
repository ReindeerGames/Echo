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
	if cfg.Probes[0].Method != "GET" || cfg.Probes[0].ExpectedMin != 200 || cfg.Probes[0].ExpectedMax != 299 || cfg.Probes[0].TLSWarningDays != 30 || cfg.Probes[0].TLSCriticalDays != 7 {
		t.Fatalf("unexpected probe defaults: %#v", cfg.Probes[0])
	}
}

func TestBundledObserverConfigsRemainLoadable(t *testing.T) {
	for _, test := range []struct {
		path          string
		tcpProbes     int
		processProbes int
	}{
		{path: "../../deploy/observer/echo-observer.example.json", tcpProbes: 1, processProbes: 1},
		{path: "../../deploy/local-test/observer.json", tcpProbes: 1, processProbes: 1},
	} {
		cfg, err := LoadConfig(test.path)
		if err != nil {
			t.Fatalf("load %s: %v", test.path, err)
		}
		if len(cfg.TCPProbes) != test.tcpProbes {
			t.Fatalf("%s has %d TCP probes, want %d", test.path, len(cfg.TCPProbes), test.tcpProbes)
		}
		if len(cfg.ProcessProbes) != test.processProbes {
			t.Fatalf("%s has %d process probes, want %d", test.path, len(cfg.ProcessProbes), test.processProbes)
		}
	}
}

func TestHTTPProbeRejectsUnsafeTLSExpiryThresholds(t *testing.T) {
	_, err := normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		Probes: []HTTPProbe{{Name: "web", ServiceID: "svc_web", URL: "https://web.test/health", TLSWarningDays: 7, TLSCriticalDays: 7}},
	})
	if err == nil || !strings.Contains(err.Error(), "TLS expiry thresholds") {
		t.Fatalf("unordered TLS thresholds were not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		Probes: []HTTPProbe{{Name: "web", ServiceID: "svc_web", URL: "https://web.test/health", TLSWarningDays: 366, TLSCriticalDays: 7}},
	})
	if err == nil || !strings.Contains(err.Error(), "TLS expiry thresholds") {
		t.Fatalf("oversized TLS warning window was not rejected: %v", err)
	}
}

func TestConfigRejectsReservedWatchdogSource(t *testing.T) {
	cfg := Config{EchoURL: "https://echo.test", Source: "echo-watchdog", Probes: []HTTPProbe{{Name: "web", ServiceID: "svc_web", URL: "https://web.test/health"}}}
	if _, err := normalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved observer source was accepted: %v", err)
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

func TestTCPProbeValidationAndCrossTypeUniqueness(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		TCPProbes: []TCPProbe{{Name: "Primary-Database", ServiceID: "svc_database", Host: "DB.INTERNAL.EXAMPLE", Port: 3306}},
	})
	if err != nil || cfg.TCPProbes[0].Name != "primary-database" || cfg.TCPProbes[0].Host != "db.internal.example" {
		t.Fatalf("valid TCP probe rejected: cfg=%#v err=%v", cfg, err)
	}
	if _, err := normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", TCPProbes: []TCPProbe{{Name: "ipv6", ServiceID: "svc_ipv6", Host: "::1", Port: 6379}}}); err != nil {
		t.Fatalf("IPv6 TCP probe rejected: %v", err)
	}
	for _, probe := range []TCPProbe{
		{Name: "missing-host", ServiceID: "svc_tcp", Port: 3306},
		{Name: "embedded-port", ServiceID: "svc_tcp", Host: "database.internal:3306", Port: 3306},
		{Name: "credentials", ServiceID: "svc_tcp", Host: "user@database.internal", Port: 3306},
		{Name: "missing-port", ServiceID: "svc_tcp", Host: "database.internal"},
		{Name: "oversized-port", ServiceID: "svc_tcp", Host: "database.internal", Port: 65536},
	} {
		if _, err := normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", TCPProbes: []TCPProbe{probe}}); err == nil {
			t.Fatalf("invalid TCP probe was accepted: %#v", probe)
		}
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		Probes:    []HTTPProbe{{Name: "web", ServiceID: "svc_same", URL: "https://web.test/health"}},
		TCPProbes: []TCPProbe{{Name: "listener", ServiceID: "svc_same", Host: "database.internal", Port: 3306}},
	})
	if err == nil || !strings.Contains(err.Error(), "one aggregate probe") {
		t.Fatalf("cross-type TCP service duplicate was not rejected: %v", err)
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

func TestProcessProbeDefaultsValidationAndCrossTypeUniqueness(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		ProcessProbes: []ProcessProbe{{Name: "Database-Process", ServiceID: "svc_database", ProxyURL: "http://127.0.0.1:23751/", ProcessName: "mariadbd"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := cfg.ProcessProbes[0]
	if probe.Name != "database-process" || probe.ProxyURL != "http://127.0.0.1:23751" || probe.ProcessName != "mariadbd" || probe.MinimumCount != 1 || probe.MaximumCount != 0 {
		t.Fatalf("unexpected process defaults: %#v", probe)
	}
	invalid := []ProcessProbe{
		{Name: "unsafe-name", ServiceID: "svc_process", ProxyURL: "http://127.0.0.1:23751", ProcessName: "process with spaces"},
		{Name: "long-name", ServiceID: "svc_process", ProxyURL: "http://127.0.0.1:23751", ProcessName: "process-name-is-too-long"},
		{Name: "bad-counts", ServiceID: "svc_process", ProxyURL: "http://127.0.0.1:23751", ProcessName: "worker", MinimumCount: 3, MaximumCount: 2},
		{Name: "half-rss", ServiceID: "svc_process", ProxyURL: "http://127.0.0.1:23751", ProcessName: "worker", RSSWarningMiB: 128},
		{Name: "bad-rss", ServiceID: "svc_process", ProxyURL: "http://127.0.0.1:23751", ProcessName: "worker", RSSWarningMiB: 512, RSSCriticalMiB: 256},
		{Name: "remote-plaintext", ServiceID: "svc_process", ProxyURL: "http://process-proxy.internal:23751", ProcessName: "worker"},
	}
	for _, candidate := range invalid {
		if _, err := normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", ProcessProbes: []ProcessProbe{candidate}}); err == nil {
			t.Fatalf("invalid process probe was accepted: %#v", candidate)
		}
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		SystemProbes:  []SystemProbe{{Name: "host", ServiceID: "svc_same"}},
		ProcessProbes: []ProcessProbe{{Name: "worker", ServiceID: "svc_same", ProxyURL: "http://127.0.0.1:23751", ProcessName: "worker"}},
	})
	if err == nil || !strings.Contains(err.Error(), "one aggregate probe") {
		t.Fatalf("cross-type process service duplicate was not rejected: %v", err)
	}
}

func TestDockerProbeValidationAndCrossTypeUniqueness(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		DockerProbes: []DockerProbe{{Name: "Web Container", ServiceID: "svc_container", ProxyURL: "https://docker-proxy.test", Container: "web-1"}},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid name") {
		t.Fatalf("unsafe Docker probe name was not rejected: cfg=%#v err=%v", cfg, err)
	}
	cfg, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		DockerProbes: []DockerProbe{{Name: "web-container", ServiceID: "svc_container", ProxyURL: "https://docker-proxy.test/", Container: "web-1"}},
	})
	if err != nil || cfg.DockerProbes[0].ProxyURL != "https://docker-proxy.test" {
		t.Fatalf("valid Docker probe rejected: cfg=%#v err=%v", cfg, err)
	}
	cfg, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		DockerProbes: []DockerProbe{{Name: "web-container", ServiceID: "svc_container", ProxyURL: "http://127.0.0.1:2375", Container: "web-1"}},
	})
	if err != nil {
		t.Fatalf("loopback plaintext Docker proxy was rejected: %v", err)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		DockerProbes: []DockerProbe{{Name: "web-container", ServiceID: "svc_container", ProxyURL: "http://docker-proxy.test:2375", Container: "web-1"}},
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("remote plaintext Docker proxy was not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		DockerProbes: []DockerProbe{{Name: "web", ServiceID: "svc_container", ProxyURL: "https://docker-proxy.test", Container: "team/web"}},
	})
	if err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("unsafe container name was not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		SystemProbes: []SystemProbe{{Name: "host", ServiceID: "svc_same"}},
		DockerProbes: []DockerProbe{{Name: "web", ServiceID: "svc_same", ProxyURL: "https://docker-proxy.test", Container: "web"}},
	})
	if err == nil || !strings.Contains(err.Error(), "one aggregate probe") {
		t.Fatalf("cross-type Docker service duplicate was not rejected: %v", err)
	}
}

func TestFreshnessProbeDefaultsAndValidation(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		FreshnessProbes: []FreshnessProbe{{Name: "Database-Backup", ServiceID: "svc_backup", Path: "/var/backups/application/", MatchSuffix: ".sql.gz"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := cfg.FreshnessProbes[0]
	if probe.Name != "database-backup" || probe.Path != "/var/backups/application" || probe.WarningAge.Duration != 26*time.Hour || probe.CriticalAge.Duration != 50*time.Hour {
		t.Fatalf("unexpected freshness defaults: %#v", probe)
	}
	_, err = normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", FreshnessProbes: []FreshnessProbe{{Name: "backup", ServiceID: "svc_backup", Path: "relative"}}})
	if err == nil || !strings.Contains(err.Error(), "path must be absolute") {
		t.Fatalf("relative freshness path was not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", FreshnessProbes: []FreshnessProbe{{Name: "backup", ServiceID: "svc_backup", Path: "/", MatchSuffix: ".sql"}}})
	if err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("filesystem-root freshness scan was not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{EchoURL: "https://echo.test", Source: "observer", FreshnessProbes: []FreshnessProbe{{Name: "backup", ServiceID: "svc_backup", Path: "/var/backups", WarningAge: Duration{10 * time.Hour}, CriticalAge: Duration{5 * time.Hour}}}})
	if err == nil || !strings.Contains(err.Error(), "age thresholds") {
		t.Fatalf("inverted freshness thresholds were not rejected: %v", err)
	}
	_, err = normalizeConfig(Config{
		EchoURL: "https://echo.test", Source: "observer",
		SystemProbes:    []SystemProbe{{Name: "host", ServiceID: "svc_same"}},
		FreshnessProbes: []FreshnessProbe{{Name: "backup", ServiceID: "svc_same", Path: "/var/backups"}},
	})
	if err == nil || !strings.Contains(err.Error(), "one aggregate probe") {
		t.Fatalf("cross-type freshness service duplicate was not rejected: %v", err)
	}
}
