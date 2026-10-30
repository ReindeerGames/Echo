package dockerproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const maxDockerResponseBytes = 1 << 20

type Proxy struct {
	allowed map[string]struct{}
	docker  *http.Client
}

type rawInspect struct {
	Name         string `json:"Name"`
	RestartCount int    `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		OOMKilled  bool   `json:"OOMKilled"`
		Dead       bool   `json:"Dead"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		} `json:"Health"`
	} `json:"State"`
}

type inspectView struct {
	Name         string    `json:"Name"`
	RestartCount int       `json:"RestartCount"`
	State        stateView `json:"State"`
}

type stateView struct {
	Status     string      `json:"Status"`
	Running    bool        `json:"Running"`
	Paused     bool        `json:"Paused"`
	Restarting bool        `json:"Restarting"`
	OOMKilled  bool        `json:"OOMKilled"`
	Dead       bool        `json:"Dead"`
	ExitCode   int         `json:"ExitCode"`
	Error      string      `json:"Error"`
	StartedAt  string      `json:"StartedAt"`
	FinishedAt string      `json:"FinishedAt"`
	Health     *healthView `json:"Health,omitempty"`
}

type healthView struct {
	Status        string `json:"Status"`
	FailingStreak int    `json:"FailingStreak"`
}

type rawStats struct {
	CPUStats    rawCPUStats    `json:"cpu_stats"`
	PreCPUStats rawCPUStats    `json:"precpu_stats"`
	MemoryStats rawMemoryStats `json:"memory_stats"`
	PIDsStats   struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

type rawCPUStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PerCPUUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemCPUUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs     uint64 `json:"online_cpus"`
}

type rawMemoryStats struct {
	Usage uint64            `json:"usage"`
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats"`
}

type statsView struct {
	CPUAvailable      bool    `json:"cpuAvailable"`
	CPUPercent        float64 `json:"cpuPercent"`
	MemoryAvailable   bool    `json:"memoryAvailable"`
	MemoryUsageBytes  uint64  `json:"memoryUsageBytes"`
	MemoryLimitBytes  uint64  `json:"memoryLimitBytes"`
	MemoryUsedPercent float64 `json:"memoryUsedPercent"`
	PIDsCurrent       uint64  `json:"pidsCurrent"`
}

func New(socketPath string, containers []string) (*Proxy, error) {
	socketPath = filepath.Clean(strings.TrimSpace(socketPath))
	if !filepath.IsAbs(socketPath) || strings.ContainsRune(socketPath, '\x00') {
		return nil, errors.New("Docker socket path must be absolute")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
		MaxIdleConns:       2,
		IdleConnTimeout:    30 * time.Second,
	}
	return newWithClient(&http.Client{Transport: transport, Timeout: 10 * time.Second}, containers)
}

func newWithClient(client *http.Client, containers []string) (*Proxy, error) {
	if client == nil {
		return nil, errors.New("Docker client is required")
	}
	allowed := make(map[string]struct{}, len(containers))
	for _, container := range containers {
		container = strings.TrimSpace(container)
		if !safeContainerName(container) || len(container) > 128 {
			return nil, fmt.Errorf("invalid allowed container %q", container)
		}
		allowed[container] = struct{}{}
	}
	if len(allowed) == 0 || len(allowed) > 100 {
		return nil, errors.New("between 1 and 100 allowed containers are required")
	}
	return &Proxy{allowed: allowed, docker: client}, nil
}

func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("GET /containers/{name}/json", p.inspect)
	mux.HandleFunc("GET /containers/{name}/stats", p.stats)
	return securityHeaders(mux)
}

func (p *Proxy) inspect(w http.ResponseWriter, r *http.Request) {
	container := r.PathValue("name")
	if _, allowed := p.allowed[container]; !allowed || !safeContainerName(container) {
		http.NotFound(w, r)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://docker/containers/"+url.PathEscape(container)+"/json", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_request_failed")
		return
	}
	response, err := p.docker.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "docker_unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		status := http.StatusBadGateway
		if response.StatusCode == http.StatusNotFound {
			status = http.StatusNotFound
		}
		writeError(w, status, "container_inspect_failed")
		return
	}
	if response.ContentLength > maxDockerResponseBytes {
		writeError(w, http.StatusBadGateway, "inspect_response_too_large")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDockerResponseBytes+1))
	if err != nil || len(body) > maxDockerResponseBytes {
		writeError(w, http.StatusBadGateway, "inspect_response_too_large")
		return
	}
	var raw rawInspect
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadGateway, "invalid_inspect_response")
		return
	}
	view := sanitizeInspect(container, raw)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		return
	}
}

func (p *Proxy) stats(w http.ResponseWriter, r *http.Request) {
	container := r.PathValue("name")
	if _, allowed := p.allowed[container]; !allowed || !safeContainerName(container) {
		http.NotFound(w, r)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://docker/containers/"+url.PathEscape(container)+"/stats?stream=false", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_request_failed")
		return
	}
	response, err := p.docker.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "docker_unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		status := http.StatusBadGateway
		if response.StatusCode == http.StatusNotFound {
			status = http.StatusNotFound
		}
		writeError(w, status, "container_stats_failed")
		return
	}
	if response.ContentLength > maxDockerResponseBytes {
		writeError(w, http.StatusBadGateway, "stats_response_too_large")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDockerResponseBytes+1))
	if err != nil || len(body) > maxDockerResponseBytes {
		writeError(w, http.StatusBadGateway, "stats_response_too_large")
		return
	}
	var raw rawStats
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadGateway, "invalid_stats_response")
		return
	}
	view := sanitizeStats(raw)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(view)
}

func sanitizeInspect(container string, raw rawInspect) inspectView {
	view := inspectView{Name: container, RestartCount: max(0, raw.RestartCount)}
	view.State = stateView{
		Status: normalizedState(raw.State.Status), Running: raw.State.Running, Paused: raw.State.Paused,
		Restarting: raw.State.Restarting, OOMKilled: raw.State.OOMKilled, Dead: raw.State.Dead,
		ExitCode: raw.State.ExitCode, StartedAt: normalizedTime(raw.State.StartedAt), FinishedAt: normalizedTime(raw.State.FinishedAt),
	}
	if raw.State.Error != "" {
		view.State.Error = "present"
	}
	if raw.State.Health != nil {
		view.State.Health = &healthView{Status: normalizedHealth(raw.State.Health.Status), FailingStreak: max(0, raw.State.Health.FailingStreak)}
	}
	return view
}

func sanitizeStats(raw rawStats) statsView {
	view := statsView{PIDsCurrent: raw.PIDsStats.Current}
	cpuCount := raw.CPUStats.OnlineCPUs
	if cpuCount == 0 {
		cpuCount = uint64(len(raw.CPUStats.CPUUsage.PerCPUUsage))
	}
	if raw.CPUStats.CPUUsage.TotalUsage >= raw.PreCPUStats.CPUUsage.TotalUsage && raw.CPUStats.SystemCPUUsage > raw.PreCPUStats.SystemCPUUsage && cpuCount > 0 {
		cpuDelta := raw.CPUStats.CPUUsage.TotalUsage - raw.PreCPUStats.CPUUsage.TotalUsage
		systemDelta := raw.CPUStats.SystemCPUUsage - raw.PreCPUStats.SystemCPUUsage
		view.CPUPercent = roundedPercent((float64(cpuDelta) / float64(systemDelta)) * float64(cpuCount) * 100)
		view.CPUAvailable = !math.IsNaN(view.CPUPercent) && !math.IsInf(view.CPUPercent, 0)
		if !view.CPUAvailable {
			view.CPUPercent = 0
		}
	}
	if raw.MemoryStats.Limit > 0 {
		cache := raw.MemoryStats.Stats["cache"]
		if inactive, exists := raw.MemoryStats.Stats["inactive_file"]; exists {
			cache = inactive
		}
		if totalInactive, exists := raw.MemoryStats.Stats["total_inactive_file"]; exists {
			cache = totalInactive
		}
		usage := raw.MemoryStats.Usage
		if cache <= usage {
			usage -= cache
		}
		view.MemoryAvailable = true
		view.MemoryUsageBytes = usage
		view.MemoryLimitBytes = raw.MemoryStats.Limit
		view.MemoryUsedPercent = roundedPercent((float64(usage) / float64(raw.MemoryStats.Limit)) * 100)
	}
	return view
}

func roundedPercent(value float64) float64 {
	return math.Round(value*100) / 100
}

func normalizedState(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "created", "running", "paused", "restarting", "removing", "exited", "dead":
		return value
	default:
		return "unknown"
	}
}

func normalizedHealth(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "healthy", "unhealthy", "starting":
		return value
	default:
		return "unknown"
	}
}

func normalizedTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil || parsed.Year() <= 1 {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
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

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
