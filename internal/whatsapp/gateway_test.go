package whatsapp

import (
	"bytes"
	"errors"
	"os"
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

func TestIncidentImageMetadataIsStrictAndBounded(t *testing.T) {
	mimeType := "image/png"
	length := uint64(1024)
	image := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: &mimeType, FileLength: &length}}
	if !hasIncidentImageCandidate(image) {
		t.Fatal("bounded PNG image was not recognized")
	}
	if shouldQueueIncidentImage(false, image) || !shouldQueueIncidentImage(true, image) {
		t.Fatal("incident screenshots must be queued only for group messages")
	}
	gotMIME, gotName, gotSize, ok := incidentImageMetadata(image)
	if !ok || gotMIME != mimeType || gotName != "" || gotSize != int64(length) {
		t.Fatalf("unexpected image metadata mime=%q name=%q size=%d ok=%v", gotMIME, gotName, gotSize, ok)
	}

	unsafeMIME := "image/svg+xml"
	unsafe := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Mimetype: &unsafeMIME, FileLength: &length}}
	if _, _, _, ok := incidentImageMetadata(unsafe); ok {
		t.Fatal("active SVG document was accepted as bounded incident evidence")
	}
	tooLarge := uint64(maxIncidentImageBytes + 1)
	large := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: &mimeType, FileLength: &tooLarge}}
	if _, _, _, ok := incidentImageMetadata(large); !ok {
		t.Fatal("oversized image should retain enough metadata to be explicitly rejected")
	}
	if got := safeAttachmentName("../../screen\x00shot.png"); got != "screenshot.png" {
		t.Fatalf("unsafe attachment name was not normalized: %q", got)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 504)...)
	if detected, err := verifiedIncidentImageMIME("image/png", png); err != nil || detected != "image/png" {
		t.Fatalf("valid PNG signature rejected: detected=%q err=%v", detected, err)
	}
	if _, err := verifiedIncidentImageMIME("image/jpeg", png); err == nil {
		t.Fatal("declared MIME mismatch was accepted")
	}
	if _, err := verifiedIncidentImageMIME("image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); err == nil {
		t.Fatal("active SVG content was accepted")
	}
}

func TestBoundedMediaFileStopsOversizedStreams(t *testing.T) {
	temporary, err := os.CreateTemp(t.TempDir(), "bounded-media-*")
	if err != nil {
		t.Fatal(err)
	}
	defer temporary.Close()
	file := &boundedMediaFile{File: temporary, max: 8}
	written, err := file.Write([]byte("123456789"))
	if written != 8 || !errors.Is(err, errIncidentImageTooLarge) {
		t.Fatalf("oversized sequential write was not bounded: written=%d err=%v", written, err)
	}
	if err := file.Truncate(9); !errors.Is(err, errIncidentImageTooLarge) {
		t.Fatalf("oversized truncate was accepted: %v", err)
	}
	if _, err := file.WriteAt([]byte("xy"), 7); !errors.Is(err, errIncidentImageTooLarge) {
		t.Fatalf("oversized random write was not bounded: %v", err)
	}
	info, err := temporary.Stat()
	if err != nil || info.Size() > 8 {
		t.Fatalf("bounded file grew beyond its limit: size=%d err=%v", info.Size(), err)
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
