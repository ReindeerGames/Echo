package briefing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

type Store interface {
	GetBriefingSettings(context.Context) (domain.BriefingSettings, error)
	EnqueueDailyBriefIfDue(context.Context, string, string, time.Time) (bool, error)
}

type OwnerLookup interface {
	JID(context.Context) (string, error)
}

type Provider interface {
	DailyBrief(context.Context) (string, error)
}

type Scheduler struct {
	store    Store
	owners   OwnerLookup
	provider Provider
	interval time.Duration
	now      func() time.Time
}

func New(store Store, owners OwnerLookup, provider Provider, interval time.Duration) *Scheduler {
	if interval < 5*time.Second || interval > 5*time.Minute {
		interval = time.Minute
	}
	return &Scheduler{store: store, owners: owners, provider: provider, interval: interval, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Scheduler) Run(ctx context.Context) error {
	if _, err := s.Sweep(ctx, s.now()); err != nil {
		return err
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := s.Sweep(ctx, s.now()); err != nil {
				return err
			}
		}
	}
}

func (s *Scheduler) Sweep(ctx context.Context, now time.Time) (bool, error) {
	settings, err := s.store.GetBriefingSettings(ctx)
	if err != nil {
		return false, fmt.Errorf("read briefing settings: %w", err)
	}
	due, err := Due(settings, now)
	if err != nil {
		return false, fmt.Errorf("evaluate briefing schedule: %w", err)
	}
	if !due {
		return false, nil
	}
	if s.owners == nil || s.provider == nil {
		return false, errors.New("daily briefing dependencies are unavailable")
	}
	ownerJID, err := s.owners.JID(ctx)
	if err != nil {
		return false, fmt.Errorf("resolve briefing owner: %w", err)
	}
	if strings.TrimSpace(ownerJID) == "" {
		return false, nil
	}
	body, err := s.provider.DailyBrief(ctx)
	if err != nil {
		return false, fmt.Errorf("render daily briefing: %w", err)
	}
	queued, err := s.store.EnqueueDailyBriefIfDue(ctx, ownerJID, body, now)
	if err != nil {
		return false, fmt.Errorf("enqueue daily briefing: %w", err)
	}
	return queued, nil
}

func Validate(settings domain.BriefingSettings) error {
	if len(settings.LocalTime) != 5 {
		return errors.New("localTime must use 24-hour HH:MM format")
	}
	if _, err := time.Parse("15:04", settings.LocalTime); err != nil {
		return errors.New("localTime must use 24-hour HH:MM format")
	}
	timezone := strings.TrimSpace(settings.Timezone)
	if timezone == "" || len(timezone) > 64 {
		return errors.New("timezone must be an IANA timezone name of at most 64 characters")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return errors.New("timezone must be a valid IANA timezone name")
	}
	return nil
}

func Due(settings domain.BriefingSettings, now time.Time) (bool, error) {
	if !settings.Enabled {
		return false, nil
	}
	if err := Validate(settings); err != nil {
		return false, err
	}
	location, _ := time.LoadLocation(strings.TrimSpace(settings.Timezone))
	localNow := now.In(location)
	scheduled, _ := time.Parse("15:04", settings.LocalTime)
	dueAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), scheduled.Hour(), scheduled.Minute(), 0, 0, location)
	localDate := localNow.Format("2006-01-02")
	return !localNow.Before(dueAt) && settings.LastSentLocalDate != localDate, nil
}
