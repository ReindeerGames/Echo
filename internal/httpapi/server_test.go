package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/config"
	"github.com/ReindeerGames/echo/internal/domain"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
)

type fakeReadinessStore struct {
	pingErr error
	schema  domain.SchemaStatus
	err     error
}

func (f fakeReadinessStore) Ping(context.Context) error { return f.pingErr }
func (f fakeReadinessStore) SchemaStatus(context.Context) (domain.SchemaStatus, error) {
	return f.schema, f.err
}

func TestReadinessRequiresDatabaseSchemaAndComponents(t *testing.T) {
	state := runtimestate.New()
	for _, component := range []string{"monitor-watchdog", "worker-watchdog", "worker", "whatsapp-gateway"} {
		state.UpdateComponent(component, "running", "")
	}
	server := &Server{
		cfg:       config.Config{Worker: config.Worker{Enabled: true}, WhatsApp: config.WhatsApp{Enabled: true}},
		readiness: fakeReadinessStore{schema: domain.SchemaStatus{Current: true, Expected: 4, Applied: 4}},
		runtime:   state,
	}
	response := httptest.NewRecorder()
	server.ready(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReadinessFailsForMissingSchema(t *testing.T) {
	state := runtimestate.New()
	state.UpdateComponent("monitor-watchdog", "running", "")
	state.UpdateComponent("worker-watchdog", "running", "")
	server := &Server{readiness: fakeReadinessStore{schema: domain.SchemaStatus{Expected: 4, Applied: 3, Missing: 1}}, runtime: state}
	response := httptest.NewRecorder()
	server.ready(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"missing":1`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReadinessFailsForStoppedComponent(t *testing.T) {
	state := runtimestate.New()
	state.UpdateComponent("monitor-watchdog", "error", "safe public detail")
	state.UpdateComponent("worker-watchdog", "running", "")
	server := &Server{readiness: fakeReadinessStore{schema: domain.SchemaStatus{Current: true}}, runtime: state}
	response := httptest.NewRecorder()
	server.ready(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"monitor-watchdog":"error"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "safe public detail") {
		t.Fatal("readiness response leaked component detail")
	}
}

func TestReadinessFailsClosedWithoutDatabaseDetails(t *testing.T) {
	state := runtimestate.New()
	state.UpdateComponent("monitor-watchdog", "running", "")
	state.UpdateComponent("worker-watchdog", "running", "")
	server := &Server{readiness: fakeReadinessStore{pingErr: errors.New("password secret rejected")}, runtime: state}
	response := httptest.NewRecorder()
	server.ready(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "password secret") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRuntimeStatusIncludesExpectedComponentsThatAreStillStarting(t *testing.T) {
	state := runtimestate.New()
	state.UpdateComponent("monitor-watchdog", "running", "")
	server := &Server{cfg: config.Config{Worker: config.Worker{Enabled: true}}, runtime: state}
	response := httptest.NewRecorder()
	server.runtimeStatus(response, httptest.NewRequest(http.MethodGet, "/api/v1/runtime", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"ready":false`) || !strings.Contains(body, `"worker":"starting"`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

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

func TestMonitorEventLimitIsBounded(t *testing.T) {
	if value, err := monitorEventLimit(""); err != nil || value != 50 {
		t.Fatalf("unexpected default limit value=%d err=%v", value, err)
	}
	if value, err := monitorEventLimit("10"); err != nil || value != 10 {
		t.Fatalf("unexpected explicit limit value=%d err=%v", value, err)
	}
	if value, err := monitorEventLimit(" 10 "); err != nil || value != 10 {
		t.Fatalf("unexpected trimmed limit value=%d err=%v", value, err)
	}
	for _, raw := range []string{"0", "201", "many"} {
		if _, err := monitorEventLimit(raw); err == nil {
			t.Fatalf("invalid limit %q was accepted", raw)
		}
	}
}

func TestJobsRejectUnknownStatusBeforeStoreAccess(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest("GET", "/api/v1/jobs?status=unknown", nil)
	response := httptest.NewRecorder()
	server.jobs(response, request)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "invalid_status") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
