package worker

import (
	"errors"
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/ai"
	"github.com/ReindeerGames/echo/internal/domain"
)

func TestAnalysisOwnerMessageExplainsReadOnlyActionGate(t *testing.T) {
	message := analysisOwnerMessage(
		domain.Incident{ID: "inc_test"},
		ai.Diagnosis{Confidence: "high", Summary: "Root cause found.", Proposal: ai.Proposal{Repository: "ReindeerGames/invoices", Description: "Prepare a focused fix."}},
		[]domain.Repository{{Owner: "ReindeerGames", Name: "invoices", Enabled: true, ReadOnly: true}},
	)
	for _, expected := range []string{"Proposed action:", "configured read-only", "enable approved changes", "request patch approval"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message is missing %q: %s", expected, message)
		}
	}
}

func TestAnalysisOwnerMessageExplainsApprovalFlow(t *testing.T) {
	message := analysisOwnerMessage(
		domain.Incident{ID: "inc_test"},
		ai.Diagnosis{Confidence: "medium", Summary: "Root cause found.", Proposal: ai.Proposal{Repository: "owner/repo"}},
		[]domain.Repository{{Owner: "owner", Name: "repo", Enabled: true, ReadOnly: false}},
	)
	for _, expected := range []string{"Request patch approval", "APPROVE", "REJECT"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message is missing %q: %s", expected, message)
		}
	}
}

func TestTerminalFailureMessageDoesNotExposeRawError(t *testing.T) {
	message := terminalFailureMessage(domain.Job{Type: "prepare_patch", MaxAttempts: 5}, "inc_test", errors.New("status 403 bearer must-not-leak"))
	if !strings.Contains(message, "authorization failed") || !strings.Contains(message, "inc_test") {
		t.Fatalf("safe terminal failure context is missing: %s", message)
	}
	if strings.Contains(message, "must-not-leak") || strings.Contains(message, "bearer") {
		t.Fatalf("raw terminal error leaked into owner message: %s", message)
	}
}
