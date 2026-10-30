package domain

import (
	"encoding/json"
	"strings"
)

func JobIncidentID(payload json.RawMessage) string {
	if len(payload) == 0 || len(payload) > 64<<10 {
		return ""
	}
	var value struct {
		IncidentID string `json:"incidentId"`
	}
	if json.Unmarshal(payload, &value) != nil || len(value.IncidentID) > 80 {
		return ""
	}
	for _, character := range value.IncidentID {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return ""
	}
	return value.IncidentID
}

func JobErrorCategory(value string) string {
	value = strings.ToLower(value)
	switch {
	case strings.Contains(value, "deadline"), strings.Contains(value, "timeout"), strings.Contains(value, "timed out"):
		return "timeout"
	case strings.Contains(value, "rate limit"), strings.Contains(value, "too many requests"), strings.Contains(value, "status 429"):
		return "rate_limited"
	case strings.Contains(value, "unauthorized"), strings.Contains(value, "forbidden"), strings.Contains(value, "status 401"), strings.Contains(value, "status 403"):
		return "authorization_failed"
	case strings.Contains(value, "not configured"), strings.Contains(value, "configuration"):
		return "configuration_missing"
	case strings.Contains(value, "not found"), strings.Contains(value, "status 404"):
		return "dependency_not_found"
	case strings.Contains(value, "conflict"), strings.Contains(value, "status 409"), strings.Contains(value, "changed"):
		return "state_conflict"
	case strings.Contains(value, "lease expired"):
		return "lease_expired"
	default:
		return "execution_failed"
	}
}
