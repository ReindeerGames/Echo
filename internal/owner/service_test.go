package owner

import (
	"context"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/store"
)

type memoryContactStore struct {
	contact domain.WhatsAppContact
	updated time.Time
}

func (m *memoryContactStore) GetOwnerContact(context.Context) (domain.WhatsAppContact, time.Time, error) {
	if m.contact.ID == "" {
		return domain.WhatsAppContact{}, time.Time{}, store.ErrNotFound
	}
	return m.contact, m.updated, nil
}
func (m *memoryContactStore) SetOwnerContact(_ context.Context, contactID, _ string) (domain.WhatsAppContact, time.Time, error) {
	m.contact = domain.WhatsAppContact{ID: contactID, JID: "27820000000@s.whatsapp.net", DisplayName: "Jason"}
	m.updated = time.Now().UTC()
	return m.contact, m.updated, nil
}
func (m *memoryContactStore) ClearOwnerContact(context.Context) error {
	m.contact = domain.WhatsAppContact{}
	return nil
}

func TestApplicationOwnerOverridesEnvironmentFallback(t *testing.T) {
	st := &memoryContactStore{}
	service := New(st, "27990000000@s.whatsapp.net")
	initial, err := service.Current(context.Background())
	if err != nil || initial.Source != "environment" {
		t.Fatalf("unexpected fallback status: %#v err=%v", initial, err)
	}
	selected, err := service.Set(context.Background(), "ctc_test", "admin")
	if err != nil || selected.Source != "application" || selected.JID != "27820000000@s.whatsapp.net" {
		t.Fatalf("unexpected selected owner: %#v err=%v", selected, err)
	}
	jid, err := service.JID(context.Background())
	if err != nil || jid != selected.JID {
		t.Fatalf("unexpected dynamic owner JID %q err=%v", jid, err)
	}
}
