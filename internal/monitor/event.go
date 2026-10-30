package monitor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

const MaxBodyBytes = 1 << 20

var ErrInvalidEvent = errors.New("invalid monitor event")

func VerifySignature(body []byte, header, secret string) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func Normalize(event domain.MonitorEvent, now time.Time) (domain.MonitorEvent, error) {
	event.EventID = strings.TrimSpace(event.EventID)
	event.ServiceID = strings.TrimSpace(event.ServiceID)
	event.Source = strings.ToLower(strings.TrimSpace(event.Source))
	event.Status = strings.ToLower(strings.TrimSpace(event.Status))
	event.Severity = strings.ToLower(strings.TrimSpace(event.Severity))
	event.Summary = strings.TrimSpace(event.Summary)
	event.Fingerprint = strings.ToLower(strings.TrimSpace(event.Fingerprint))
	if !printableASCII(event.EventID) || len(event.EventID) > 255 {
		return domain.MonitorEvent{}, fmt.Errorf("%w: eventId must be a printable ASCII identifier of at most 255 characters", ErrInvalidEvent)
	}
	if !printableASCII(event.ServiceID) || len(event.ServiceID) > 28 || !safeName(event.Source) || len(event.Source) > 120 {
		return domain.MonitorEvent{}, fmt.Errorf("%w: serviceId and source must be valid ASCII identifiers", ErrInvalidEvent)
	}
	if event.Source == "echo-watchdog" {
		return domain.MonitorEvent{}, fmt.Errorf("%w: source is reserved for Echo's heartbeat watchdog", ErrInvalidEvent)
	}
	if event.Summary == "" || len(event.Summary) > 1000 {
		return domain.MonitorEvent{}, fmt.Errorf("%w: summary is required and must not exceed 1000 characters", ErrInvalidEvent)
	}
	switch event.Status {
	case "healthy", "degraded", "unhealthy", "unknown":
	default:
		return domain.MonitorEvent{}, fmt.Errorf("%w: status must be healthy, degraded, unhealthy, or unknown", ErrInvalidEvent)
	}
	if event.Severity == "" {
		switch event.Status {
		case "unhealthy":
			event.Severity = "critical"
		case "degraded":
			event.Severity = "warning"
		default:
			event.Severity = "info"
		}
	}
	if event.Severity != "info" && event.Severity != "warning" && event.Severity != "critical" {
		return domain.MonitorEvent{}, fmt.Errorf("%w: severity must be info, warning, or critical", ErrInvalidEvent)
	}
	if (event.Status == "degraded" || event.Status == "unhealthy") && event.Fingerprint == "" {
		sum := sha256.Sum256([]byte(event.ServiceID + "\x00" + event.Source + "\x00" + event.Status))
		event.Fingerprint = hex.EncodeToString(sum[:16])
	}
	if event.Fingerprint != "" && (!printableASCII(event.Fingerprint) || len(event.Fingerprint) > 255) {
		return domain.MonitorEvent{}, fmt.Errorf("%w: fingerprint must be a printable ASCII identifier of at most 255 characters", ErrInvalidEvent)
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = now.UTC()
	} else {
		event.ObservedAt = event.ObservedAt.UTC()
	}
	if event.ObservedAt.After(now.Add(5 * time.Minute)) {
		return domain.MonitorEvent{}, fmt.Errorf("%w: observedAt is too far in the future", ErrInvalidEvent)
	}
	if len(event.Details) == 0 {
		event.Details = json.RawMessage(`{}`)
	}
	if !json.Valid(event.Details) {
		return domain.MonitorEvent{}, fmt.Errorf("%w: details must be valid JSON", ErrInvalidEvent)
	}
	return event, nil
}

func printableASCII(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func safeName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}
