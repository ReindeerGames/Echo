package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

func (o *Observer) observeTCP(ctx context.Context, probe TCPProbe) domain.MonitorEvent {
	observedAt := o.now()
	event := domain.MonitorEvent{
		EventID:     fmt.Sprintf("%s:%s:%d", o.config.Source, probe.Name, observedAt.UnixNano()),
		ServiceID:   probe.ServiceID,
		Source:      o.config.Source,
		Status:      "unhealthy",
		Severity:    "critical",
		Summary:     probe.Name + " TCP listener is unreachable",
		Fingerprint: "tcp-connectivity",
		ObservedAt:  observedAt,
	}
	started := time.Now()
	dialer := net.Dialer{Timeout: o.config.Timeout.Duration, KeepAlive: -1}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(probe.Host, strconv.Itoa(probe.Port)))
	latency := time.Since(started)
	if err != nil {
		event.Details = tcpDetails(probe, latency, classifyProbeError(err))
		return event
	}
	_ = connection.Close()
	event.Status = "healthy"
	event.Severity = "info"
	event.Summary = fmt.Sprintf("%s TCP listener is reachable (port %d)", probe.Name, probe.Port)
	event.Details = tcpDetails(probe, latency, "")
	return event
}

func tcpDetails(probe TCPProbe, latency time.Duration, failure string) json.RawMessage {
	value := map[string]any{
		"probe":     probe.Name,
		"port":      probe.Port,
		"latencyMs": latency.Milliseconds(),
	}
	if failure != "" {
		value["failure"] = failure
	}
	raw, _ := json.Marshal(value)
	return raw
}
