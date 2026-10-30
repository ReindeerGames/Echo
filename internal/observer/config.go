package observer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultInterval = 30 * time.Second
	defaultTimeout  = 5 * time.Second
	maxConfigBytes  = 1 << 20
)

type Config struct {
	EchoURL           string      `json:"echoUrl"`
	Source            string      `json:"source"`
	Interval          Duration    `json:"interval"`
	Timeout           Duration    `json:"timeout"`
	AllowInsecureHTTP bool        `json:"allowInsecureHttp"`
	Probes            []HTTPProbe `json:"probes"`
}

type HTTPProbe struct {
	Name        string `json:"name"`
	ServiceID   string `json:"serviceId"`
	URL         string `json:"url"`
	Method      string `json:"method"`
	ExpectedMin int    `json:"expectedStatusMin"`
	ExpectedMax int    `json:"expectedStatusMax"`
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
	echoURL, err := validateURL(cfg.EchoURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return Config{}, fmt.Errorf("invalid echoUrl: %w", err)
	}
	cfg.EchoURL = strings.TrimRight(echoURL.String(), "/")
	if len(cfg.Probes) == 0 || len(cfg.Probes) > 100 {
		return Config{}, errors.New("between 1 and 100 probes are required")
	}
	seenNames := make(map[string]struct{}, len(cfg.Probes))
	seenServices := make(map[string]struct{}, len(cfg.Probes))
	for index := range cfg.Probes {
		probe := &cfg.Probes[index]
		probe.Name = strings.ToLower(strings.TrimSpace(probe.Name))
		probe.ServiceID = strings.TrimSpace(probe.ServiceID)
		probe.Method = strings.ToUpper(strings.TrimSpace(probe.Method))
		if probe.Method == "" {
			probe.Method = "GET"
		}
		if !safeName(probe.Name) || len(probe.Name) > 80 {
			return Config{}, fmt.Errorf("probe %d has an invalid name", index+1)
		}
		if !printableASCII(probe.ServiceID) || len(probe.ServiceID) > 28 {
			return Config{}, fmt.Errorf("probe %q has an invalid serviceId", probe.Name)
		}
		if _, exists := seenNames[probe.Name]; exists {
			return Config{}, fmt.Errorf("probe name %q is duplicated", probe.Name)
		}
		if _, exists := seenServices[probe.ServiceID]; exists {
			return Config{}, fmt.Errorf("serviceId %q has more than one probe; aggregate checks behind one health endpoint", probe.ServiceID)
		}
		seenNames[probe.Name] = struct{}{}
		seenServices[probe.ServiceID] = struct{}{}
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
	}
	return cfg, nil
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
