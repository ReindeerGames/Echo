package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/config"
	"github.com/ReindeerGames/echo/internal/domain"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
)

func TestVerifyGitHubSignature(t *testing.T) {
	body := []byte(`{"action":"completed"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifyGitHubSignature(body, header, "secret") {
		t.Fatal("valid signature rejected")
	}
	if verifyGitHubSignature([]byte("changed"), header, "secret") {
		t.Fatal("forged body accepted")
	}
}

type fakeGroupLister struct{}

func (fakeGroupLister) ListGroups(context.Context) ([]domain.WhatsAppGroup, error) {
	return []domain.WhatsAppGroup{{JID: "120363@g.us", Name: "Team Nudgie", ParticipantCount: 4}}, nil
}

type fakePairer struct{ called bool }

func (f *fakePairer) RequestPairing() error {
	f.called = true
	return nil
}

func TestWhatsAppGroupDiscovery(t *testing.T) {
	server := &Server{groupLister: fakeGroupLister{}}
	request := httptest.NewRequest("GET", "/api/v1/whatsapp/groups", nil)
	response := httptest.NewRecorder()
	server.whatsappGroups(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Team Nudgie") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWhatsAppLinkStartsPairing(t *testing.T) {
	pairer := &fakePairer{}
	state := runtimestate.New()
	server := &Server{pairer: pairer, runtime: state}
	request := httptest.NewRequest("POST", "/api/v1/whatsapp/link", nil)
	response := httptest.NewRecorder()
	server.whatsappLink(response, request)
	if response.Code != 202 || !pairer.called {
		t.Fatalf("status=%d called=%v body=%s", response.Code, pairer.called, response.Body.String())
	}
}

func TestSlug(t *testing.T) {
	if got := slug(" Team Nudgie / ZA "); got != "team-nudgie-za" {
		t.Fatalf("unexpected slug %q", got)
	}
}

func TestMonitorWebhookFailsClosedWithoutSecret(t *testing.T) {
	server := &Server{cfg: config.Config{}}
	request := httptest.NewRequest("POST", "/webhooks/monitor", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.monitorWebhook(response, request)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "monitor_webhook_disabled") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMonitorWebhookRejectsUnsignedPayload(t *testing.T) {
	server := &Server{cfg: config.Config{Monitor: config.Monitor{WebhookSecret: "test-secret"}}}
	request := httptest.NewRequest("POST", "/webhooks/monitor", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.monitorWebhook(response, request)
	if response.Code != 401 || !strings.Contains(response.Body.String(), "invalid_signature") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestServiceRejectsUnsafeHeartbeatTimeout(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest("POST", "/api/v1/services", strings.NewReader(`{"projectId":"prj_1","key":"api","name":"API","heartbeatTimeoutSeconds":30,"metadata":{}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.services(response, request)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "between 60 and 86400") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
