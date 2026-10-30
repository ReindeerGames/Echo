package observer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

const maxResponseBytes = 64 << 10

type Observer struct {
	config Config
	secret string
	probe  *http.Client
	sender *http.Client
	system SystemReader
	now    func() time.Time
}

func New(cfg Config, secret string) (*Observer, error) {
	var err error
	cfg, err = normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	secret = strings.TrimSpace(secret)
	if len(secret) < 32 {
		return nil, errors.New("ECHO_OBSERVER_WEBHOOK_SECRET must contain at least 32 characters")
	}
	noRedirect := func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Observer{
		config: cfg,
		secret: secret,
		probe:  &http.Client{Timeout: cfg.Timeout.Duration, CheckRedirect: noRedirect, Transport: directTransport()},
		sender: &http.Client{Timeout: cfg.Timeout.Duration, CheckRedirect: noRedirect, Transport: directTransport()},
		system: newSystemReader(),
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

func directTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return transport
}

func (o *Observer) Run(ctx context.Context) error {
	if err := o.RunOnce(ctx); err != nil {
		slog.Error("observer cycle completed with delivery failures", "error", err)
	}
	ticker := time.NewTicker(o.config.Interval.Duration)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := o.RunOnce(ctx); err != nil {
				slog.Error("observer cycle completed with delivery failures", "error", err)
			}
		}
	}
}

func (o *Observer) RunOnce(ctx context.Context) error {
	type probeJob struct {
		serviceID string
		observe   func(context.Context) domain.MonitorEvent
	}
	allJobs := make([]probeJob, 0, len(o.config.Probes)+len(o.config.SystemProbes)+len(o.config.DockerProbes))
	for _, configured := range o.config.Probes {
		probe := configured
		allJobs = append(allJobs, probeJob{serviceID: probe.ServiceID, observe: func(ctx context.Context) domain.MonitorEvent { return o.observe(ctx, probe) }})
	}
	for _, configured := range o.config.SystemProbes {
		probe := configured
		allJobs = append(allJobs, probeJob{serviceID: probe.ServiceID, observe: func(ctx context.Context) domain.MonitorEvent { return o.observeSystem(ctx, probe) }})
	}
	for _, configured := range o.config.DockerProbes {
		probe := configured
		allJobs = append(allJobs, probeJob{serviceID: probe.ServiceID, observe: func(ctx context.Context) domain.MonitorEvent { return o.observeDocker(ctx, probe) }})
	}
	workerCount := min(8, len(allJobs))
	jobs := make(chan probeJob)
	failures := make(chan error, len(allJobs))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				event := job.observe(ctx)
				if err := o.sendWithRetry(ctx, event); err != nil {
					failures <- fmt.Errorf("%s: %w", job.serviceID, err)
				}
			}
		}()
	}
	for _, job := range allJobs {
		select {
		case jobs <- job:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			close(failures)
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	close(failures)
	var deliveryErrors []error
	for err := range failures {
		deliveryErrors = append(deliveryErrors, err)
	}
	return errors.Join(deliveryErrors...)
}

func (o *Observer) observe(ctx context.Context, probe HTTPProbe) domain.MonitorEvent {
	observedAt := o.now()
	event := domain.MonitorEvent{
		EventID:     fmt.Sprintf("%s:%s:%d", o.config.Source, probe.Name, observedAt.UnixNano()),
		ServiceID:   probe.ServiceID,
		Source:      o.config.Source,
		Status:      "unhealthy",
		Severity:    "critical",
		Summary:     probe.Name + " health check failed",
		Fingerprint: "http-health",
		ObservedAt:  observedAt,
	}
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, probe.Method, probe.URL, nil)
	if err != nil {
		event.Details = details(probe, 0, time.Since(started), "invalid_request", nil, observedAt)
		return event
	}
	request.Header.Set("User-Agent", "Echo-Observer/1.0")
	response, err := o.probe.Do(request)
	latency := time.Since(started)
	if err != nil {
		event.Details = details(probe, 0, latency, classifyProbeError(err), nil, observedAt)
		return event
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	_ = response.Body.Close()
	if response.StatusCode < probe.ExpectedMin || response.StatusCode > probe.ExpectedMax {
		event.Summary = fmt.Sprintf("%s returned HTTP %d", probe.Name, response.StatusCode)
		event.Details = details(probe, response.StatusCode, latency, "unexpected_status", response.TLS, observedAt)
		return event
	}
	if expiresAt, ok := certificateExpiry(response.TLS); ok {
		remaining := expiresAt.Sub(observedAt)
		if remaining <= time.Duration(probe.TLSWarningDays)*24*time.Hour {
			daysRemaining := int(math.Ceil(remaining.Hours() / 24))
			event.Status = "degraded"
			event.Severity = "warning"
			if remaining <= time.Duration(probe.TLSCriticalDays)*24*time.Hour {
				event.Status = "unhealthy"
				event.Severity = "critical"
			}
			event.Summary = certificateExpirySummary(probe.Name, daysRemaining)
			event.Details = details(probe, response.StatusCode, latency, "", response.TLS, observedAt)
			return event
		}
	}
	event.Status = "healthy"
	event.Severity = "info"
	event.Summary = fmt.Sprintf("%s is healthy (HTTP %d)", probe.Name, response.StatusCode)
	event.Fingerprint = ""
	event.Details = details(probe, response.StatusCode, latency, "", response.TLS, observedAt)
	return event
}

func details(probe HTTPProbe, status int, latency time.Duration, failure string, connection *tls.ConnectionState, observedAt time.Time) json.RawMessage {
	parsed, _ := url.Parse(probe.URL)
	target := ""
	if parsed != nil {
		target = parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
	}
	value := map[string]any{
		"probe":      probe.Name,
		"method":     probe.Method,
		"target":     target,
		"statusCode": status,
		"latencyMs":  latency.Milliseconds(),
	}
	if failure != "" {
		value["failure"] = failure
	}
	if expiresAt, ok := certificateExpiry(connection); ok {
		value["tls"] = map[string]any{
			"expiresAt":     expiresAt,
			"daysRemaining": int(math.Ceil(expiresAt.Sub(observedAt).Hours() / 24)),
		}
		value["thresholds"] = map[string]any{
			"tlsWarningDays":  probe.TLSWarningDays,
			"tlsCriticalDays": probe.TLSCriticalDays,
		}
	}
	raw, _ := json.Marshal(value)
	return raw
}

func certificateExpiry(connection *tls.ConnectionState) (time.Time, bool) {
	if connection == nil || len(connection.PeerCertificates) == 0 || connection.PeerCertificates[0] == nil {
		return time.Time{}, false
	}
	return connection.PeerCertificates[0].NotAfter.UTC(), true
}

func certificateExpirySummary(name string, daysRemaining int) string {
	if daysRemaining <= 0 {
		return name + " TLS certificate has expired"
	}
	unit := "days"
	if daysRemaining == 1 {
		unit = "day"
	}
	return fmt.Sprintf("%s TLS certificate expires in %d %s", name, daysRemaining, unit)
}

func classifyProbeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Timeout() {
		return "timeout"
	}
	return "connection_failed"
}

func (o *Observer) sendWithRetry(ctx context.Context, event domain.MonitorEvent) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt*attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		lastErr = o.send(ctx, raw)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func (o *Observer) send(ctx context.Context, raw []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.config.EchoURL+"/webhooks/monitor", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(o.secret))
	_, _ = mac.Write(raw)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Echo-Observer/1.0")
	request.Header.Set("X-Echo-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response, err := o.sender.Do(request)
	if err != nil {
		return fmt.Errorf("deliver event: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Echo monitor webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}
