package policy

import (
	"errors"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestInboundRequiresActualMention(t *testing.T) {
	engine := New()
	binding := domain.GroupBinding{Enabled: true, MentionRequired: true}
	msg := domain.InboundMessage{IsGroup: true, Body: "hey @echo forms are blank", MentionedJIDs: []string{"123@s.whatsapp.net"}}
	if engine.InboundShouldCreate(binding, msg, []string{"999@s.whatsapp.net"}) {
		t.Fatal("plain text must not satisfy mention policy")
	}
	msg.MentionedJIDs = append(msg.MentionedJIDs, "999@s.whatsapp.net")
	if !engine.InboundShouldCreate(binding, msg, []string{"echo@lid", "999@s.whatsapp.net"}) {
		t.Fatal("protocol mention should create incident")
	}
}

func TestInboundMentionMatchesLinkedDeviceJID(t *testing.T) {
	engine := New()
	binding := domain.GroupBinding{Enabled: true, MentionRequired: true}
	msg := domain.InboundMessage{
		IsGroup:       true,
		Body:          "the invoice form is blank",
		MentionedJIDs: []string{"27688209266@s.whatsapp.net"},
	}
	accepted, reason := engine.InboundDecision(binding, msg, []string{"27688209266:1@s.whatsapp.net", "131220730019897:1@lid"})
	if !accepted || reason != "accepted" {
		t.Fatalf("account mention must match the linked device JID, accepted=%v reason=%q", accepted, reason)
	}
}

func TestInboundDecisionExplainsMissingMention(t *testing.T) {
	engine := New()
	binding := domain.GroupBinding{Enabled: true, MentionRequired: true}
	msg := domain.InboundMessage{IsGroup: true, Body: "plain group message"}
	accepted, reason := engine.InboundDecision(binding, msg, []string{"27688209266:1@s.whatsapp.net"})
	if accepted || reason != "echo_not_mentioned" {
		t.Fatalf("expected a diagnosable mention rejection, accepted=%v reason=%q", accepted, reason)
	}
}

func TestProductionRequiresVerifiedStagingState(t *testing.T) {
	engine := New()
	incident := domain.Incident{ID: "inc_test", Status: "staging"}
	action := map[string]any{"artifactRef": "0123456789012345678901234567890123456789", "stagingDeploymentId": "dep_test", "environmentId": "env_test"}
	err := engine.ValidateApprovalRequest(incident, "deploy_production", action)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected denial, got %v", err)
	}
	incident.Status = "staging_verified"
	if err := engine.ValidateApprovalRequest(incident, "deploy_production", action); err != nil {
		t.Fatalf("expected approval eligibility: %v", err)
	}
}

func TestPatchRequiresRepository(t *testing.T) {
	engine := New()
	incident := domain.Incident{ID: "inc_test", Status: "awaiting_owner"}
	if err := engine.ValidateApprovalRequest(incident, "prepare_patch", map[string]any{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected denial, got %v", err)
	}
}

func TestRepositoryNativeProductionRequiresPreparedChange(t *testing.T) {
	engine := New()
	action := map[string]any{"changeSetId": "cng_test"}
	for _, status := range []string{"patching", "change_approved", "failed"} {
		if err := engine.ValidateApprovalRequest(domain.Incident{ID: "inc_test", Status: status}, "merge_production", action); err != nil {
			t.Fatalf("status %s should be eligible: %v", status, err)
		}
	}
	if err := engine.ValidateApprovalRequest(domain.Incident{ID: "inc_test", Status: "awaiting_owner"}, "merge_production", action); !errors.Is(err, ErrDenied) {
		t.Fatalf("unprepared incident should be denied, got %v", err)
	}
	if err := engine.ValidateApprovalRequest(domain.Incident{ID: "inc_test", Status: "patching"}, "merge_production", map[string]any{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing change set should be denied, got %v", err)
	}
}
