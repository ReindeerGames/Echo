package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJobJSONOmitsActionPayload(t *testing.T) {
	encoded, err := json.Marshal(Job{
		ID:         "job_1",
		Type:       "prepare_patch",
		Payload:    json.RawMessage(`{"incidentId":"inc_1","token":"must-not-leak"}`),
		IncidentID: "inc_1",
		Status:     "dead",
	})
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	if strings.Contains(value, "must-not-leak") || strings.Contains(value, `"payload"`) {
		t.Fatalf("job payload leaked through JSON: %s", value)
	}
	if !strings.Contains(value, `"incidentId":"inc_1"`) {
		t.Fatalf("safe incident correlation is missing: %s", value)
	}
}

func TestJobContextAndErrorCategoriesAreSafe(t *testing.T) {
	if got := JobIncidentID(json.RawMessage(`{"incidentId":"inc_123"}`)); got != "inc_123" {
		t.Fatalf("unexpected incident ID %q", got)
	}
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"incidentId":"../../secret"}`),
		json.RawMessage(`{"incidentId":"inc 123"}`),
		json.RawMessage(`not-json`),
	} {
		if got := JobIncidentID(payload); got != "" {
			t.Fatalf("unsafe incident ID accepted from %q: %q", payload, got)
		}
	}
	if got := JobErrorCategory("request status 403 bearer must-not-leak"); got != "authorization_failed" {
		t.Fatalf("unexpected error category %q", got)
	}
}

func TestOutboundDeliveryErrorCategoriesAreSafe(t *testing.T) {
	tests := map[string]string{
		"delivery lease expired after final attempt": "lease_expired",
		"context deadline exceeded":                  "timeout",
		"server returned status 429":                 "rate_limited",
		"WhatsApp device logged out":                 "session_invalid",
		"dial tcp: connection refused":               "gateway_unavailable",
		"request status 403 bearer must-not-leak":    "authorization_failed",
		"invalid JID format":                         "invalid_destination",
		"opaque secret must-not-leak":                "delivery_failed",
	}
	for input, expected := range tests {
		if got := OutboundDeliveryErrorCategory(input); got != expected {
			t.Errorf("OutboundDeliveryErrorCategory(%q) = %q, want %q", input, got, expected)
		}
	}
}
