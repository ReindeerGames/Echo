package owner

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/store"
)

type contactStore interface {
	GetOwnerContact(context.Context) (domain.WhatsAppContact, time.Time, error)
	SetOwnerContact(context.Context, string, string) (domain.WhatsAppContact, time.Time, error)
	ClearOwnerContact(context.Context) error
}

type Service struct {
	store    contactStore
	fallback string
}

type Lookup interface {
	JID(context.Context) (string, error)
}

type notificationLookup struct {
	source  Lookup
	enabled bool
}

func New(st contactStore, fallback string) *Service {
	return &Service{store: st, fallback: strings.TrimSpace(fallback)}
}

// Notifications returns a view of the owner used only by unsolicited
// background delivery paths. Muting it does not remove the configured owner
// or prevent replies to owner-initiated conversations.
func Notifications(source Lookup, enabled bool) Lookup {
	return &notificationLookup{source: source, enabled: enabled}
}

func (s *notificationLookup) JID(ctx context.Context) (string, error) {
	if !s.enabled || s.source == nil {
		return "", nil
	}
	return s.source.JID(ctx)
}

func (s *Service) Current(ctx context.Context) (domain.OwnerSettings, error) {
	contact, updated, err := s.store.GetOwnerContact(ctx)
	if err == nil {
		return domain.OwnerSettings{Configured: true, ContactID: contact.ID, DisplayName: contact.DisplayName, JID: contact.JID, Source: "application", UpdatedAt: updated}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.OwnerSettings{}, err
	}
	if s.fallback != "" {
		return domain.OwnerSettings{Configured: true, JID: s.fallback, Source: "environment"}, nil
	}
	return domain.OwnerSettings{}, nil
}

func (s *Service) JID(ctx context.Context) (string, error) {
	settings, err := s.Current(ctx)
	return settings.JID, err
}

func (s *Service) Set(ctx context.Context, contactID, actor string) (domain.OwnerSettings, error) {
	contact, updated, err := s.store.SetOwnerContact(ctx, contactID, actor)
	if err != nil {
		return domain.OwnerSettings{}, err
	}
	return domain.OwnerSettings{Configured: true, ContactID: contact.ID, DisplayName: contact.DisplayName, JID: contact.JID, Source: "application", UpdatedAt: updated}, nil
}

func (s *Service) Clear(ctx context.Context) error { return s.store.ClearOwnerContact(ctx) }
