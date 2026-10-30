package observer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultInterval = 30 * time.Second
	defaultTimeout  = 5 * time.Second
	maxConfigBytes  = 1 << 20
)

type Config struct {
	EchoURL           string           `json:"echoUrl"`
	Source            string           `json:"source"`
	Interval          Duration         `json:"interval"`
	Timeout           Duration         `json:"timeout"`
	AllowInsecureHTTP bool             `json:"allowInsecureHttp"`
	Probes            []HTTPProbe      `json:"probes"`
	SystemProbes      []SystemProbe    `json:"systemProbes"`
	DockerProbes      []DockerProbe    `json:"dockerProbes"`
	FreshnessProbes   []FreshnessProbe `json:"freshnessProbes"`
}

type HTTPProbe struct {
	Name            string `json:"name"`
	ServiceID       string `json:"serviceId"`
	URL             string `json:"url"`
	Method          string `json:"method"`
	ExpectedMin     int    `json:"expectedStatusMin"`
	ExpectedMax     int    `json:"expectedStatusMax"`
	TLSWarningDays  int    `json:"tlsWarningDays"`
	TLSCriticalDays int    `json:"tlsCriticalDays"`
}

type SystemProbe struct {
	Name                  string  `json:"name"`
	ServiceID             string  `json:"serviceId"`
	DiskPath              string  `json:"diskPath"`
	DiskWarningPercent    float64 `json:"diskWarningPercent"`
	DiskCriticalPercent   float64 `json:"diskCriticalPercent"`
	MemoryWarningPercent  float64 `json:"memoryWarningPercent"`
	MemoryCriticalPercent float64 `json:"memoryCriticalPercent"`
	LoadWarningPerCPU     float64 `json:"loadWarningPerCpu"`
	LoadCriticalPerCPU    float64 `json:"loadCriticalPerCpu"`
}

type DockerProbe struct {
	Name      string `json:"name"`
	ServiceID string `json:"serviceId"`
	ProxyURL  string `json:"proxyUrl"`
	Container string `json:"container"`
}

type FreshnessProbe struct {
	Name        string   `json:"name"`
	ServiceID   string   `json:"serviceId"`
	Path        string   `json:"path"`
	MatchSuffix string   `json:"matchSuffix"`
	WarningAge  Duration `json:"warningAge"`
	CriticalAge Duration `json:"criticalAge"`
}

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("duration must be a string such as 30s")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration %q", value)
	}
	d.Duration = parsed
	return nil
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(&limitedReader{reader: file, remaining: maxConfigBytes})
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode observer config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("observer config contains trailing data")
		}
		return Config{}, fmt.Errorf("decode observer config: %w", err)
	}
	return normalizeConfig(cfg)
}

func normalizeConfig(cfg Config) (Config, error) {
	cfg.EchoURL = strings.TrimRight(strings.TrimSpace(cfg.EchoURL), "/")
	cfg.Source = strings.ToLower(strings.TrimSpace(cfg.Source))
	if cfg.Interval.Duration == 0 {
		cfg.Interval.Duration = defaultInterval
	}
	if cfg.Timeout.Duration == 0 {
		cfg.Timeout.Duration = defaultTimeout
	}
	if cfg.Interval.Duration < 5*time.Second || cfg.Interval.Duration > time.Hour {
		return Config{}, errors.New("interval must be between 5s and 1h")
	}
	if cfg.Timeout.Duration < 250*time.Millisecond || cfg.Timeout.Duration > 30*time.Second {
		return Config{}, errors.New("timeout must be between 250ms and 30s")
	}
	if !safeName(cfg.Source) || len(cfg.Source) > 120 {
		return Config{}, errors.New("source must use lowercase letters, numbers, dash, underscore, dot, or colon")
	}
	if cfg.Source == "echo-watchdog" {
		return Config{}, errors.New("source echo-watchdog is reserved for Echo's heartbeat watchdog")
	}
	echoURL, err := validateURL(cfg.EchoURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return Config{}, fmt.Errorf("invalid echoUrl: %w", err)
	}
	cfg.EchoURL = strings.TrimRight(echoURL.String(), "/")
	totalProbes := len(cfg.Probes) + len(cfg.SystemProbes) + len(cfg.DockerProbes) + len(cfg.FreshnessProbes)
	if totalProbes == 0 || totalProbes > 100 {
		return Config{}, errors.New("between 1 and 100 probes are required")
	}
	seenNames := make(map[string]struct{}, totalProbes)
	seenServices := make(map[string]struct{}, totalProbes)
	for index := range cfg.Probes {
		probe := &cfg.Probes[index]
		probe.Name = strings.ToLower(strings.TrimSpace(probe.Name))
		probe.ServiceID = strings.TrimSpace(probe.ServiceID)
		probe.Method = strings.ToUpper(strings.TrimSpace(probe.Method))
		if probe.Method == "" {
			probe.Method = "GET"
		}
		if err := registerProbe(probe.Name, probe.ServiceID, fmt.Sprintf("HTTP probe %d", index+1), seenNames, seenServices); err != nil {
			return Config{}, err
		}
		if probe.Method != "GET" && probe.Method != "HEAD" {
			return Config{}, fmt.Errorf("probe %q method must be GET or HEAD", probe.Name)
		}
		probeURL, err := validateURL(strings.TrimSpace(probe.URL), cfg.AllowInsecureHTTP)
		if err != nil {
			return Config{}, fmt.Errorf("probe %q URL: %w", probe.Name, err)
		}
		probe.URL = probeURL.String()
		if probe.ExpectedMin == 0 {
			probe.ExpectedMin = 200
		}
		if probe.ExpectedMax == 0 {
			probe.ExpectedMax = 299
		}
		if probe.ExpectedMin < 100 || probe.ExpectedMax > 599 || probe.ExpectedMin > probe.ExpectedMax {
			return Config{}, fmt.Errorf("probe %q expected status range is invalid", probe.Name)
		}
		if probe.TLSWarningDays == 0 {
			probe.TLSWarningDays = 30
		}
		if probe.TLSCriticalDays == 0 {
			probe.TLSCriticalDays = 7
		}
		if probe.TLSWarningDays < 2 || probe.TLSWarningDays > 365 || probe.TLSCriticalDays < 1 || probe.TLSCriticalDays >= probe.TLSWarningDays {
			return Config{}, fmt.Errorf("probe %q TLS expiry thresholds must be between 1 and 365 days with critical below warning", probe.Name)
		}
	}
	for index := range cfg.SystemProbes {
		probe := &cfg.SystemProbes[index]
		probe.Name = strings.ToLower(strings.TrimSpace(probe.Name))
		probe.ServiceID = strings.TrimSpace(probe.ServiceID)
		probe.DiskPath = strings.TrimSpace(probe.DiskPath)
		if probe.DiskPath == "" {
			probe.DiskPath = "/"
		}
		if !strings.HasPrefix(probe.DiskPath, "/") || strings.ContainsRune(probe.DiskPath, '\x00') {
			return Config{}, fmt.Errorf("system probe %q diskPath must be an absolute path", probe.Name)
		}
		probe.DiskPath = filepath.Clean(probe.DiskPath)
		if err := registerProbe(probe.Name, probe.ServiceID, fmt.Sprintf("system probe %d", index+1), seenNames, seenServices); err != nil {
			return Config{}, err
		}
		applySystemDefaults(probe)
		if err := validatePercentThresholds("disk", probe.Name, probe.DiskWarningPercent, probe.DiskCriticalPercent); err != nil {
			return Config{}, err
		}
		if err := validatePercentThresholds("memory", probe.Name, probe.MemoryWarningPercent, probe.MemoryCriticalPercent); err != nil {
			return Config{}, err
		}
		if probe.LoadWarningPerCPU <= 0 || probe.LoadCriticalPerCPU <= probe.LoadWarningPerCPU || probe.LoadCriticalPerCPU > 20 {
			return Config{}, fmt.Errorf("system probe %q load thresholds must be positive, ordered, and no greater than 20 per CPU", probe.Name)
		}
	}
	for index := range cfg.DockerProbes {
		probe := &cfg.DockerProbes[index]
		probe.Name = strings.ToLower(strings.TrimSpace(probe.Name))
		probe.ServiceID = strings.TrimSpace(probe.ServiceID)
		probe.Container = strings.TrimSpace(probe.Container)
		if err := registerProbe(probe.Name, probe.ServiceID, fmt.Sprintf("Docker probe %d", index+1), seenNames, seenServices); err != nil {
			return Config{}, err
		}
		if !safeContainerName(probe.Container) || len(probe.Container) > 128 {
			return Config{}, fmt.Errorf("Docker probe %q container must use letters, numbers, dot, dash, or underscore", probe.Name)
		}
		proxyURL, err := validateDockerProxyURL(strings.TrimSpace(probe.ProxyURL), cfg.AllowInsecureHTTP)
		if err != nil {
			return Config{}, fmt.Errorf("Docker probe %q proxyUrl: %w", probe.Name, err)
		}
		probe.ProxyURL = strings.TrimRight(proxyURL.String(), "/")
	}
	for index := range cfg.FreshnessProbes {
		probe := &cfg.FreshnessProbes[index]
		probe.Name = strings.ToLower(strings.TrimSpace(probe.Name))
		probe.ServiceID = strings.TrimSpace(probe.ServiceID)
		probe.Path = strings.TrimSpace(probe.Path)
		probe.MatchSuffix = strings.TrimSpace(probe.MatchSuffix)
		if err := registerProbe(probe.Name, probe.ServiceID, fmt.Sprintf("freshness probe %d", index+1), seenNames, seenServices); err != nil {
			return Config{}, err
		}
		if !filepath.IsAbs(probe.Path) || strings.ContainsRune(probe.Path, '\x00') {
			return Config{}, fmt.Errorf("freshness probe %q path must be absolute", probe.Name)
		}
		probe.Path = filepath.Clean(probe.Path)
		if probe.Path == "/" {
			return Config{}, fmt.Errorf("freshness probe %q path must not be the filesystem root", probe.Name)
		}
		if len(probe.MatchSuffix) > 80 || strings.ContainsAny(probe.MatchSuffix, "/\\\x00") {
			return Config{}, fmt.Errorf("freshness probe %q matchSuffix is invalid", probe.Name)
		}
		if probe.WarningAge.Duration == 0 {
			probe.WarningAge.Duration = 26 * time.Hour
		}
		if probe.CriticalAge.Duration == 0 {
			probe.CriticalAge.Duration = 50 * time.Hour
		}
		if probe.WarningAge.Duration < time.Minute || probe.WarningAge.Duration > 365*24*time.Hour || probe.CriticalAge.Duration <= probe.WarningAge.Duration || probe.CriticalAge.Duration > 365*24*time.Hour {
			return Config{}, fmt.Errorf("freshness probe %q age thresholds must be between 1m and 8760h with critical above warning", probe.Name)
		}
	}
	return cfg, nil
}

func validateDockerProxyURL(raw string, allowHTTP bool) (*url.URL, error) {
	parsed, err := validateURL(raw, true)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" || allowHTTP {
		return parsed, nil
	}
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return parsed, nil
	}
	return nil, errors.New("plaintext Docker proxy URLs must use loopback unless allowInsecureHttp is enabled")
}

func registerProbe(name, serviceID, label string, seenNames, seenServices map[string]struct{}) error {
	if !safeName(name) || len(name) > 80 {
		return fmt.Errorf("%s has an invalid name", label)
	}
	if !printableASCII(serviceID) || len(serviceID) > 28 {
		return fmt.Errorf("probe %q has an invalid serviceId", name)
	}
	if _, exists := seenNames[name]; exists {
		return fmt.Errorf("probe name %q is duplicated", name)
	}
	if _, exists := seenServices[serviceID]; exists {
		return fmt.Errorf("serviceId %q has more than one probe; each service requires one aggregate probe", serviceID)
	}
	seenNames[name] = struct{}{}
	seenServices[serviceID] = struct{}{}
	return nil
}

func applySystemDefaults(probe *SystemProbe) {
	if probe.DiskWarningPercent == 0 {
		probe.DiskWarningPercent = 85
	}
	if probe.DiskCriticalPercent == 0 {
		probe.DiskCriticalPercent = 95
	}
	if probe.MemoryWarningPercent == 0 {
		probe.MemoryWarningPercent = 85
	}
	if probe.MemoryCriticalPercent == 0 {
		probe.MemoryCriticalPercent = 95
	}
	if probe.LoadWarningPerCPU == 0 {
		probe.LoadWarningPerCPU = 1
	}
	if probe.LoadCriticalPerCPU == 0 {
		probe.LoadCriticalPerCPU = 2
	}
}

func validatePercentThresholds(kind, name string, warning, critical float64) error {
	if warning <= 0 || warning >= 100 || critical <= warning || critical > 100 {
		return fmt.Errorf("system probe %q %s thresholds must be positive percentages with warning below critical", name, kind)
	}
	return nil
}

func validateURL(raw string, allowHTTP bool) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("an absolute URL is required")
	}
	if parsed.User != nil {
		return nil, errors.New("URL credentials are not allowed")
	}
	if parsed.RawQuery != "" {
		return nil, errors.New("URL query strings are not allowed")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return nil, errors.New("HTTPS is required unless allowInsecureHttp is enabled")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("URL fragments are not allowed")
	}
	return parsed, nil
}

func safeName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func safeContainerName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func printableASCII(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

type limitedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *limitedReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("observer config exceeds one MiB")
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	read, err := r.reader.Read(buffer)
	r.remaining -= int64(read)
	return read, err
}
