package store

import (
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestContactJIDsPreferPhoneNumberAndRemoveDevice(t *testing.T) {
	primary, alternate := contactJIDs(domain.InboundMessage{
		ChatJID:      "131220730019897@lid",
		SenderJID:    "131220730019897:1@lid",
		SenderAltJID: "27688209266:7@s.whatsapp.net",
	})
	if primary != "27688209266@s.whatsapp.net" || alternate != "131220730019897@lid" {
		t.Fatalf("unexpected identities primary=%q alternate=%q", primary, alternate)
	}
}
