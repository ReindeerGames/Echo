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
