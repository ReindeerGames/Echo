package webui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesControlPlane(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Echo Control Plane") {
		t.Fatal("control plane markup missing")
	}
	if !strings.Contains(response.Body.String(), "Guided setup") {
		t.Fatal("guided setup entry point missing")
	}
	if strings.Contains(response.Body.String(), `<dialog id="modal"><form`) {
		t.Fatal("modal shell must not create invalid nested forms")
	}
}

func TestGitHubTokenSettingsAreExposedWithoutAppCredentials(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, "Configure GitHub token") {
		t.Fatal("GitHub token settings UI missing")
	}
	if strings.Contains(body, "installation ID") || strings.Contains(body, "GitHub App credentials") {
		t.Fatal("legacy GitHub App setup is still exposed")
	}
}

func TestWhatsAppOwnerDiscoveryUIIsExposed(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, "Owner contact") || !strings.Contains(body, "/api/v1/whatsapp/contacts") {
		t.Fatal("WhatsApp owner discovery UI missing")
	}
	if !strings.Contains(body, "Group participants are not automatically collected") {
		t.Fatal("contact discovery privacy boundary is not explained")
	}
}

func TestOpenAISettingsUIIsExposed(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, "AI CONNECTION") || !strings.Contains(body, "/api/v1/settings/ai") {
		t.Fatal("OpenAI settings UI missing")
	}
	if !strings.Contains(body, "encrypts the key in MariaDB") {
		t.Fatal("OpenAI credential handling is not explained")
	}
}

func TestIncidentActionFlowExplainsRepositoryGateAndApproval(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"Patch preparation is locked", "Review repository access", "Request patch approval", "one-time approval request", "APPROVE and REJECT"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("incident action flow is missing %q", expected)
		}
	}
}

func TestRepositoryAccessFormUsesNamedControlsAndConfirmsSavedPolicy(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{`form.elements.namedItem(name)?.checked`, `saved.readOnly?"Repository remains read-only":"Approved changes enabled; reopen the incident to request patch approval"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("repository access save flow is missing %q", expected)
		}
	}
}

func TestIncidentOffersSeparateRepositoryNativeProductionApproval(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"Request merge and production deploy", `actionType:"merge_production"`, "verify GitHub checks", "monitor the repository's existing main-branch deployment workflow"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("repository-native production flow is missing %q", expected)
		}
	}
}
