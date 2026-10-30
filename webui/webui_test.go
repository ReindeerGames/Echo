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

func TestGitHubRepositoryDiscoveryDefaultsToReadOnly(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"Discover from GitHub", "/api/v1/github/repositories", "github-discovery-form", "Read-only by default", "readOnly:true", "archived"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("GitHub repository discovery is missing %q", expected)
		}
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

func TestDailyBriefingScheduleIsVisibleAndTimezoneAware(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"Daily briefing", "/api/v1/settings/briefing", "IANA timezone", "At most once per local date", "resolvedOptions().timeZone"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("daily briefing UI is missing %q", expected)
		}
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

func TestWhatsAppDeliveryFailuresAreVisible(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"pendingWhatsAppMessages", "deadWhatsAppMessages", "dead outbound messages"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("WhatsApp delivery health is missing %q", expected)
		}
	}
}

func TestWhatsAppDeliveryDiagnosticsArePrivacySafeAndReadOnly(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"/api/v1/whatsapp/outbox", "deliveryTable", "WhatsApp delivery queue", "Safe failure category", "Message bodies and destinations are omitted", "dead messages are never retried blindly"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("WhatsApp delivery diagnostics are missing %q", expected)
		}
	}
	if strings.Contains(body, "data-retry-delivery") {
		t.Fatal("WhatsApp diagnostics must not expose a blind retry control")
	}
}

func TestWorkerQueueFailuresAreVisibleWithoutRetryControl(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"/api/v1/jobs", "queuedJobs", "retryingJobs", "deadJobs", "workerQueueStalled", "workerHealthSummary", "Worker queue stalled", "Work queue", "Job queue", "dead work is never retried blindly"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("worker queue health is missing %q", expected)
		}
	}
	if strings.Contains(body, "data-retry-job") {
		t.Fatal("worker queue must not expose a blind retry control")
	}
}

func TestRuntimeComponentsAndReadinessContractAreVisible(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"/api/v1/runtime", "Runtime components", "/healthz reports process liveness", "/readyz additionally verifies", "runtimeReady", "normalizedRuntime"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("runtime health UI is missing %q", expected)
		}
	}
}

func TestMonitoringSupportsServiceEvidenceAndPolicyEditing(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"data-service=", `tabindex="0" role="button"`, "SERVICE EVIDENCE", "Observer freshness", "Edit service policy", "data-edit-service", "serviceId=", "limit=20&includeDetails=false", "limit=10"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("service evidence workflow is missing %q", expected)
		}
	}
}

func TestMonitoringSupportsBoundedPlannedMaintenance(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"/api/v1/maintenance", "Planned maintenance", "Schedule maintenance", "maintenance-form", "Evidence collection continues", "data-cancel-maintenance", "alert notifications restored", "cannot overlap"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("planned maintenance workflow is missing %q", expected)
		}
	}
	if strings.Contains(body, "data-disable-monitoring") {
		t.Fatal("maintenance must not disable monitoring evidence collection")
	}
}

func TestMonitoringSupportsAlertAcknowledgementLifecycle(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"/acknowledgement", "data-alert-acknowledgement", "Acknowledge", "Clear acknowledgement", "Alert acknowledged; attention recorded", "Unacknowledged critical"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("alert acknowledgement workflow is missing %q", expected)
		}
	}
}

func TestMonitoringSupportsProjectScopedServiceTopology(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"/api/v1/service-dependencies", "dependency-form", "Add dependency", "Declared topology", "Topology is evidence, not causation", "data-delete-dependency", "Service dependency added"} {
		if response.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("service topology workflow is missing %q", expected)
		}
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

func TestIncidentScreenshotEvidenceIsVisibleWithoutPublicMediaURL(t *testing.T) {
	request := httptest.NewRequest("GET", "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"Screenshot evidence", "retained evidence", "SHA-256", "item.sizeBytes"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("incident screenshot evidence is missing %q", expected)
		}
	}
	for _, expected := range []string{"INCIDENT THREAD", "follow-up", "Evidence revision", "followUpCount", "item.whatsappMessageId===i.sourceMessageId"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("incident follow-up evidence is missing %q", expected)
		}
	}
	if strings.Contains(body, "/attachments/${") {
		t.Fatal("incident UI must not expose a public attachment download route")
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
