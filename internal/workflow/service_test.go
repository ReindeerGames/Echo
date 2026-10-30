package workflow

import (
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestSameJIDMatchesLinkedDeviceAndAccount(t *testing.T) {
	if !sameJID("27820000000:4@s.whatsapp.net", "27820000000@s.whatsapp.net") {
		t.Fatal("linked device identity must match its owner account")
	}
	if sameJID("27820000000@s.whatsapp.net", "27820000001@s.whatsapp.net") {
		t.Fatal("different accounts must not match")
	}
}

func TestParseAlertAcknowledgementCommandRequiresExactCode(t *testing.T) {
	for _, input := range []string{"ack abcdef012345", "ACKNOWLEDGE ABCDEF012345."} {
		if code, ok := parseAlertAcknowledgementCommand(input); !ok || code != "abcdef012345" {
			t.Fatalf("valid acknowledgement command rejected: input=%q code=%q ok=%t", input, code, ok)
		}
	}
	for _, input := range []string{"ack", "ack abc", "please ack abcdef012345", "ack abcdef01234z"} {
		if _, ok := parseAlertAcknowledgementCommand(input); ok {
			t.Fatalf("unsafe acknowledgement command accepted: %q", input)
		}
	}
}

func TestApprovalResolutionMessageExplainsQueuedPatchWork(t *testing.T) {
	message := approvalResolutionMessage(domain.Approval{IncidentID: "inc_test", ActionType: "prepare_patch"}, "approved")
	for _, expected := range []string{"now queued", "few minutes", "draft pull request", "terminal failure"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("approval feedback is missing %q: %s", expected, message)
		}
	}
}

func TestApprovalResolutionMessageExplainsRejection(t *testing.T) {
	message := approvalResolutionMessage(domain.Approval{IncidentID: "inc_test", ActionType: "prepare_patch"}, "rejected")
	if !strings.Contains(message, "No action will be taken") {
		t.Fatalf("rejection feedback is unclear: %s", message)
	}
}

func TestApprovalResolutionMessageExplainsRepositoryProductionMonitoring(t *testing.T) {
	message := approvalResolutionMessage(domain.Approval{IncidentID: "inc_test", ActionType: "merge_production"}, "approved")
	for _, expected := range []string{"exact approved pull-request commit", "main-branch deployment workflow", "verifies successfully or fails"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("repository production feedback is missing %q: %s", expected, message)
		}
	}
}
