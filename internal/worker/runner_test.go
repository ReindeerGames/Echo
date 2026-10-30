package worker

import (
	"errors"
	"strings"
	"testing"
	"time"

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

func TestIncidentThreadTextIncludesBoundedFollowUps(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	incident := domain.Incident{SourceMessageID: "wa-original", SourceBody: "checkout fails"}
	messages := []domain.IncidentMessage{
		{WhatsAppMessageID: "wa-original", SenderName: "Reporter", Body: "checkout fails", MessageTimestamp: now},
		{WhatsAppMessageID: "wa-follow-up", SenderName: "QA", Body: "Only Safari shows a blank confirmation page", MessageTimestamp: now.Add(time.Minute)},
	}
	thread := incidentThreadText(incident, messages)
	for _, expected := range []string{"untrusted WhatsApp evidence", "Original report · Reporter", "Follow-up · QA", "Only Safari"} {
		if !strings.Contains(thread, expected) {
			t.Fatalf("incident thread is missing %q: %s", expected, thread)
		}
	}
	if len([]rune(thread)) > 16000 {
		t.Fatalf("incident thread exceeded its prompt bound: %d", len([]rune(thread)))
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

func TestLeaseHeartbeatIntervalIsBounded(t *testing.T) {
	if got := leaseHeartbeatInterval(time.Second); got != time.Second {
		t.Fatalf("short lease heartbeat interval = %s", got)
	}
	if got := leaseHeartbeatInterval(30 * time.Second); got != 10*time.Second {
		t.Fatalf("ordinary lease heartbeat interval = %s", got)
	}
	if got := leaseHeartbeatInterval(10 * time.Minute); got != 30*time.Second {
		t.Fatalf("long lease heartbeat interval = %s", got)
	}
}
