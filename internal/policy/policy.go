package policy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ReindeerGames/echo/internal/domain"
)

var ErrDenied = errors.New("policy denied action")

type Engine struct{}

func New() *Engine { return &Engine{} }

func (e *Engine) ValidateApprovalRequest(incident domain.Incident, actionType string, action map[string]any) error {
	if incident.ID == "" {
		return fmt.Errorf("%w: incident is required", ErrDenied)
	}
	if incident.Status == "closed" || incident.Status == "resolved" {
		return fmt.Errorf("%w: incident is already %s", ErrDenied, incident.Status)
	}
	switch actionType {
	case "prepare_patch":
		if incident.Status != "awaiting_owner" {
			return fmt.Errorf("%w: patch preparation requires awaiting_owner status", ErrDenied)
		}
		if blank(action["repositoryId"]) {
			return fmt.Errorf("%w: repositoryId is required", ErrDenied)
		}
	case "deploy_staging":
		if incident.Status != "change_approved" && incident.Status != "patching" && incident.Status != "awaiting_owner" {
			return fmt.Errorf("%w: staging requires an approved or prepared change", ErrDenied)
		}
		if blank(action["artifactRef"]) {
			return fmt.Errorf("%w: immutable artifactRef is required", ErrDenied)
		}
		if blank(action["environmentId"]) {
			return fmt.Errorf("%w: environmentId is required", ErrDenied)
		}
	case "deploy_production":
		if incident.Status != "staging_verified" {
			return fmt.Errorf("%w: production requires verified staging", ErrDenied)
		}
		if blank(action["artifactRef"]) || blank(action["stagingDeploymentId"]) {
			return fmt.Errorf("%w: production must reference the staged artifact and deployment", ErrDenied)
		}
		if blank(action["environmentId"]) {
			return fmt.Errorf("%w: environmentId is required", ErrDenied)
		}
	case "merge_production":
		if incident.Status != "patching" && incident.Status != "change_approved" && incident.Status != "failed" {
			return fmt.Errorf("%w: repository-native production requires a prepared change", ErrDenied)
		}
		if blank(action["changeSetId"]) {
			return fmt.Errorf("%w: changeSetId is required", ErrDenied)
		}
	case "rollback":
		if blank(action["artifactRef"]) || blank(action["environmentId"]) || blank(action["sourceDeploymentId"]) {
			return fmt.Errorf("%w: rollback requires a protected environment and a verified source deployment", ErrDenied)
		}
	default:
		return fmt.Errorf("%w: unsupported action %q", ErrDenied, actionType)
	}
	return nil
}

func (e *Engine) InboundShouldCreate(binding domain.GroupBinding, message domain.InboundMessage, selfJIDs []string) bool {
	accepted, _ := e.InboundDecision(binding, message, selfJIDs)
	return accepted
}

// InboundDecision applies the intake policy and returns a stable reason that can
// be logged and audited without retaining the message body. WhatsApp identifies
// linked devices with an agent/device suffix (for example :1@s.whatsapp.net),
// while protocol mentions generally contain the account JID without that suffix.
func (e *Engine) InboundDecision(binding domain.GroupBinding, message domain.InboundMessage, selfJIDs []string) (bool, string) {
	if !binding.Enabled || !message.IsGroup || message.Body == "" {
		switch {
		case !binding.Enabled:
			return false, "binding_disabled"
		case !message.IsGroup:
			return false, "not_a_group_message"
		default:
			return false, "empty_message"
		}
	}
	if !binding.MentionRequired {
		return true, "accepted"
	}
	if len(selfJIDs) == 0 {
		return false, "echo_identity_unavailable"
	}
	for _, selfJID := range selfJIDs {
		self := normalizeJID(selfJID)
		for _, mentioned := range message.MentionedJIDs {
			if normalizeJID(mentioned) == self && self != "" {
				return true, "accepted"
			}
		}
	}
	return false, "echo_not_mentioned"
}

func blank(value any) bool { return strings.TrimSpace(fmt.Sprint(value)) == "" || value == nil }

func normalizeJID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return value
	}
	user, server := value[:at], value[at+1:]
	if device := strings.IndexByte(user, ':'); device >= 0 {
		user = user[:device]
	}
	if user == "" {
		return ""
	}
	return user + "@" + server
}
