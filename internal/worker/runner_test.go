package worker

import (
	"strings"
	"testing"
	"unicode/utf8"

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

func TestTerminalFailureMessageTextIsBounded(t *testing.T) {
	long := strings.Repeat("failure ", 200)
	if got := truncateText(long, 500); utf8.RuneCountInString(got) > 500 {
		t.Fatalf("terminal failure details exceed message limit: %d", utf8.RuneCountInString(got))
	}
}
