package monitor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"eventId":"evt-1"}`)
	mac := hmac.New(sha256.New, []byte("local-secret"))
	_, _ = mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifySignature(body, header, "local-secret") {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature([]byte("changed"), header, "local-secret") {
		t.Fatal("tampered payload accepted")
	}
	if VerifySignature(body, header, "") {
		t.Fatal("empty secret must disable verification")
	}
}

func TestNormalizeDefaultsAndFingerprint(t *testing.T) {
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	event, err := Normalize(domain.MonitorEvent{
		EventID: " evt-1 ", ServiceID: "svc_123", Source: " Docker ", Status: "DEGRADED",
		Summary: "High restart count", Details: json.RawMessage(`{"restarts":5}`),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if event.Severity != "warning" || event.Fingerprint == "" || event.Source != "docker" || !event.ObservedAt.Equal(now) {
		t.Fatalf("unexpected normalized event: %#v", event)
	}
}

func TestNormalizeRejectsInvalidEvent(t *testing.T) {
	_, err := Normalize(domain.MonitorEvent{EventID: "evt", ServiceID: "svc", Source: "docker", Status: "burning", Summary: "bad"}, time.Now())
	if err == nil {
		t.Fatal("invalid status accepted")
	}
}

func TestNormalizeRejectsReservedWatchdogSource(t *testing.T) {
	_, err := Normalize(domain.MonitorEvent{EventID: "evt", ServiceID: "svc_123", Source: "echo-watchdog", Status: "healthy", Summary: "forged heartbeat recovery"}, time.Now())
	if !errors.Is(err, ErrInvalidEvent) || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved watchdog source was accepted: %v", err)
	}
}

func TestNormalizeRejectsNonASCIIIdentifiers(t *testing.T) {
	_, err := Normalize(domain.MonitorEvent{EventID: "événement", ServiceID: "svc_123", Source: "docker", Status: "healthy", Summary: "ok"}, time.Now())
	if err == nil {
		t.Fatal("non-ASCII event identifier accepted")
	}
}
