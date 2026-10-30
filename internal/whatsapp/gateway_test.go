package whatsapp

import (
	"strings"
	"testing"

	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
	"github.com/polymorfa/hypermeow/proto/waE2E"
)

func TestOutboundMessageIDIsStableAndWhatsAppShaped(t *testing.T) {
	first := outboundMessageID("out_0123456789abcdef01234567")
	second := outboundMessageID("out_0123456789abcdef01234567")
	other := outboundMessageID("out_0123456789abcdef01234568")
	if first != second || first == other || !strings.HasPrefix(string(first), "3EB0") || len(first) != 22 {
		t.Fatalf("unexpected deterministic message IDs first=%q second=%q other=%q", first, second, other)
	}
}

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
