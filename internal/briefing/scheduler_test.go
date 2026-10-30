package briefing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

type fakeStore struct {
	settings domain.BriefingSettings
	queued   bool
	calls    int
	jid      string
	body     string
	now      time.Time
	err      error
}

func (f *fakeStore) GetBriefingSettings(context.Context) (domain.BriefingSettings, error) {
	return f.settings, f.err
}

func (f *fakeStore) EnqueueDailyBriefIfDue(_ context.Context, jid, body string, now time.Time) (bool, error) {
	f.calls++
	f.jid, f.body, f.now = jid, body, now
	return f.queued, f.err
}

type fakeOwner struct {
	jid   string
	calls int
}

func (f *fakeOwner) JID(context.Context) (string, error) {
	f.calls++
	return f.jid, nil
}

type fakeProvider struct {
	body  string
	err   error
	calls int
}

func (f *fakeProvider) DailyBrief(context.Context) (string, error) {
	f.calls++
	return f.body, f.err
}

func TestDueUsesConfiguredLocalDayAndTime(t *testing.T) {
	settings := domain.BriefingSettings{Enabled: true, LocalTime: "08:00", Timezone: "Africa/Johannesburg"}
	before := time.Date(2026, 10, 30, 5, 59, 0, 0, time.UTC)
	if due, err := Due(settings, before); err != nil || due {
		t.Fatalf("brief unexpectedly due before local time: due=%v err=%v", due, err)
	}
	atTime := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	if due, err := Due(settings, atTime); err != nil || !due {
		t.Fatalf("brief not due at local time: due=%v err=%v", due, err)
	}
	settings.LastSentLocalDate = "2026-10-30"
	if due, err := Due(settings, atTime.Add(time.Hour)); err != nil || due {
		t.Fatalf("brief unexpectedly due twice in local day: due=%v err=%v", due, err)
	}
}

func TestSchedulerRendersOnlyWhenDueAndOwnerExists(t *testing.T) {
	now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	store := &fakeStore{settings: domain.BriefingSettings{Enabled: true, LocalTime: "08:00", Timezone: "Africa/Johannesburg"}, queued: true}
	owner := &fakeOwner{jid: "owner@s.whatsapp.net"}
	provider := &fakeProvider{body: "Echo daily brief"}
	scheduler := New(store, owner, provider, time.Minute)
	queued, err := scheduler.Sweep(context.Background(), now)
	if err != nil || !queued || provider.calls != 1 || owner.calls != 1 || store.calls != 1 || store.jid != owner.jid || store.body != provider.body || !store.now.Equal(now) {
		t.Fatalf("unexpected scheduled briefing: queued=%v store=%#v owner=%#v provider=%#v err=%v", queued, store, owner, provider, err)
	}

	store.settings.LastSentLocalDate = "2026-10-30"
	queued, err = scheduler.Sweep(context.Background(), now.Add(time.Hour))
	if err != nil || queued || provider.calls != 1 || owner.calls != 1 || store.calls != 1 {
		t.Fatalf("duplicate briefing work was not skipped: queued=%v store=%#v owner=%#v provider=%#v err=%v", queued, store, owner, provider, err)
	}

	store.settings.LastSentLocalDate = ""
	owner.jid = ""
	queued, err = scheduler.Sweep(context.Background(), now)
	if err != nil || queued || provider.calls != 1 || owner.calls != 2 || store.calls != 1 {
		t.Fatalf("ownerless briefing was not skipped: queued=%v store=%#v owner=%#v provider=%#v err=%v", queued, store, owner, provider, err)
	}
}

func TestSchedulerDoesNotMarkFailedBriefAsSent(t *testing.T) {
	now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	store := &fakeStore{settings: domain.BriefingSettings{Enabled: true, LocalTime: "08:00", Timezone: "Africa/Johannesburg"}, queued: true}
	provider := &fakeProvider{err: errors.New("github timed out")}
	queued, err := New(store, &fakeOwner{jid: "owner@s.whatsapp.net"}, provider, time.Minute).Sweep(context.Background(), now)
	if err == nil || queued || store.calls != 0 {
		t.Fatalf("failed brief was enqueued: queued=%v calls=%d err=%v", queued, store.calls, err)
	}
}

func TestValidateRejectsAmbiguousSchedules(t *testing.T) {
	for _, settings := range []domain.BriefingSettings{
		{LocalTime: "8:00", Timezone: "UTC"},
		{LocalTime: "25:00", Timezone: "UTC"},
		{LocalTime: "08:00", Timezone: "not/a-zone"},
	} {
		if err := Validate(settings); err == nil {
			t.Fatalf("invalid schedule accepted: %#v", settings)
		}
	}
}
