package processproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Snapshot struct {
	ProcessCount  int    `json:"processCount"`
	RunningCount  int    `json:"runningCount"`
	SleepingCount int    `json:"sleepingCount"`
	StoppedCount  int    `json:"stoppedCount"`
	ZombieCount   int    `json:"zombieCount"`
	OtherCount    int    `json:"otherCount"`
	ThreadCount   uint64 `json:"threadCount"`
	RSSBytes      uint64 `json:"rssBytes"`
}

type Scanner interface {
	Snapshot(context.Context, string) (Snapshot, error)
}

type Proxy struct {
	allowed map[string]struct{}
	scanner Scanner
}

func New(processNames []string) (*Proxy, error) {
	return newWithScanner(newScanner(), processNames)
}

func newWithScanner(scanner Scanner, processNames []string) (*Proxy, error) {
	if scanner == nil {
		return nil, errors.New("process scanner is required")
	}
	allowed := make(map[string]struct{}, len(processNames))
	for _, processName := range processNames {
		processName = strings.TrimSpace(processName)
		if !safeProcessName(processName) || len(processName) > 15 {
			return nil, fmt.Errorf("invalid allowed process name %q", processName)
		}
		allowed[processName] = struct{}{}
	}
	if len(allowed) == 0 || len(allowed) > 100 {
		return nil, errors.New("between 1 and 100 allowed process names are required")
	}
	return &Proxy{allowed: allowed, scanner: scanner}, nil
}

func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("GET /processes/{name}", p.inspect)
	return securityHeaders(mux)
}

func (p *Proxy) inspect(w http.ResponseWriter, r *http.Request) {
	processName := r.PathValue("name")
	if _, allowed := p.allowed[processName]; !allowed || !safeProcessName(processName) {
		http.NotFound(w, r)
		return
	}
	snapshot, err := p.scanner.Snapshot(r.Context(), processName)
	if err != nil || !validSnapshot(snapshot) {
		writeError(w, http.StatusBadGateway, "process_scan_failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func validSnapshot(snapshot Snapshot) bool {
	states := snapshot.RunningCount + snapshot.SleepingCount + snapshot.StoppedCount + snapshot.ZombieCount + snapshot.OtherCount
	if snapshot.ProcessCount < 0 || snapshot.RunningCount < 0 || snapshot.SleepingCount < 0 || snapshot.StoppedCount < 0 || snapshot.ZombieCount < 0 || snapshot.OtherCount < 0 {
		return false
	}
	return states == snapshot.ProcessCount && snapshot.ThreadCount >= uint64(snapshot.ProcessCount)
}

func safeProcessName(value string) bool {
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
