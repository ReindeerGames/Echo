package whatsapp

import (
	"testing"

	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
	"github.com/polymorfa/hypermeow/proto/waE2E"
)

func TestMessageBodyAndMentions(t *testing.T) {
	text := "lead form is blank"
	mentioned := []string{"123@s.whatsapp.net"}
	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: &text, ContextInfo: &waE2E.ContextInfo{MentionedJID: mentioned}}}
	if got := messageBody(msg); got != text {
		t.Fatalf("unexpected body %q", got)
	}
	got := mentionedJIDs(msg)
	if len(got) != 1 || got[0] != mentioned[0] {
		t.Fatalf("unexpected mentions %#v", got)
	}
}

func TestRequestPairingIsExplicitAndIdempotent(t *testing.T) {
	state := runtimestate.New()
	gateway := New(nil, nil, state, "")
	state.UpdateWhatsApp(func(item *runtimestate.WhatsAppState) { item.Status = "unpaired" })

	if err := gateway.RequestPairing(); err != nil {
		t.Fatal(err)
	}
	if got := state.WhatsApp().Status; got != "pairing_requested" {
		t.Fatalf("unexpected status %q", got)
	}
	if len(gateway.pairing) != 1 {
		t.Fatalf("expected one pairing request, got %d", len(gateway.pairing))
	}
	if err := gateway.RequestPairing(); err != nil {
		t.Fatal(err)
	}
	if len(gateway.pairing) != 1 {
		t.Fatalf("duplicate pairing request was queued")
	}
}
