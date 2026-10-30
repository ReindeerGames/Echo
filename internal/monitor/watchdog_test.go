package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

type fakeHeartbeatStore struct {
	stale     []domain.StaleServiceHeartbeat
	inactive  []domain.AlertTransition
	seen      map[string]bool
	events    []domain.MonitorEvent
	ownerJIDs []string
	listCalls int
	due       []domain.AlertTransition
	dueCalls  int
}

func (f *fakeHeartbeatStore) ListStaleServiceHeartbeats(context.Context, time.Time) ([]domain.StaleServiceHeartbeat, error) {
	f.listCalls++
	return f.stale, nil
}

func (f *fakeHeartbeatStore) ResolveInactiveHeartbeatAlerts(_ context.Context, _ time.Time, owner ...string) ([]domain.AlertTransition, error) {
	if len(owner) > 0 {
		f.ownerJIDs = append(f.ownerJIDs, owner[0])
	}
	return f.inactive, nil
}

func (f *fakeHeartbeatStore) RecordMonitorEvent(_ context.Context, event domain.MonitorEvent, owner ...string) (domain.MonitorResult, error) {
	if len(owner) > 0 {
		f.ownerJIDs = append(f.ownerJIDs, owner[0])
	}
	f.events = append(f.events, event)
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[event.EventID] {
		return domain.MonitorResult{Event: event, Duplicate: true}, nil
	}
	f.seen[event.EventID] = true
	alert := domain.ServiceAlert{ID: "alt_1", ServiceID: event.ServiceID, ServiceName: "API", Fingerprint: event.Fingerprint, Status: "open", Severity: event.Severity, Summary: event.Summary}
	transition := domain.AlertTransition{Kind: "opened", Alert: alert}
	return domain.MonitorResult{Event: event, Transitions: []domain.AlertTransition{transition}}, nil
}

func (f *fakeHeartbeatStore) QueueDueServiceAlertNotifications(_ context.Context, _ time.Time, _ time.Duration, owner string, _ int) ([]domain.AlertTransition, error) {
	f.dueCalls++
	f.ownerJIDs = append(f.ownerJIDs, owner)
	return f.due, nil
}

func (*fakeHeartbeatStore) Audit(context.Context, string, string, string, string, string, string, any) error {
	return nil
}

func TestWatchdogIncludesDueAlertFollowThrough(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	due := domain.AlertTransition{Kind: "reminder", Alert: domain.ServiceAlert{ID: "alt_1", ServiceID: "svc_1", ReminderCount: 2}}
	store := &fakeHeartbeatStore{due: []domain.AlertTransition{due}}
	watchdog := NewWatchdog(store, fakeOwner{jid: "owner@s.whatsapp.net"}, 30*time.Second, time.Hour)
	transitions, err := watchdog.Sweep(t.Context(), now)
	if err != nil || len(transitions) != 1 || transitions[0].Kind != "reminder" || store.dueCalls != 1 {
		t.Fatalf("due alert follow-through missing: transitions=%#v calls=%d err=%v", transitions, store.dueCalls, err)
	}
}

type fakeOwner struct {
	jid string
	err error
}

func (f fakeOwner) JID(context.Context) (string, error) { return f.jid, f.err }

func TestWatchdogCreatesOneStableCriticalSilenceAlert(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	last := now.Add(-5 * time.Minute)
	store := &fakeHeartbeatStore{stale: []domain.StaleServiceHeartbeat{{ServiceID: "svc_api", ServiceName: "API", Criticality: "high", TimeoutSeconds: 120, LastEventID: "observer:event-1", LastObservedAt: &last, PolicyUpdatedAt: last.Add(-time.Hour), PolicyReference: last}}}
	watchdog := NewWatchdog(store, fakeOwner{jid: "owner@s.whatsapp.net"}, 30*time.Second)
	first, err := watchdog.Sweep(t.Context(), now)
	if err != nil || len(first) != 1 || first[0].Kind != "opened" {
		t.Fatalf("first sweep: transitions=%#v err=%v", first, err)
	}
	second, err := watchdog.Sweep(t.Context(), now.Add(time.Minute))
	if err != nil || len(second) != 0 {
		t.Fatalf("duplicate sweep: transitions=%#v err=%v", second, err)
	}
	if len(store.events) != 2 || store.events[0].EventID != store.events[1].EventID || store.events[0].Status != "unhealthy" || store.events[0].Severity != "critical" || store.events[0].Fingerprint != "observer-silent" {
		t.Fatalf("unexpected watchdog events: %#v", store.events)
	}
	for _, ownerJID := range store.ownerJIDs {
		if ownerJID != "owner@s.whatsapp.net" {
			t.Fatalf("notification owner was not propagated: %q", ownerJID)
		}
	}
}

func TestWatchdogFailsClosedWhenOwnerLookupFails(t *testing.T) {
	store := &fakeHeartbeatStore{}
	watchdog := NewWatchdog(store, fakeOwner{err: errors.New("database unavailable")}, 30*time.Second)
	if _, err := watchdog.Sweep(t.Context(), time.Now()); err == nil {
		t.Fatal("owner lookup error was ignored")
	}
	if store.listCalls != 0 || len(store.events) != 0 {
		t.Fatal("watchdog mutated monitoring state without resolving the notification owner")
	}
}
