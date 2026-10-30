package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

type fakeHealthStore struct {
	health       domain.WorkerHealth
	transition   *domain.WorkerHealthTransition
	ownerJID     string
	reconciled   int
	auditActions []string
}

func (f *fakeHealthStore) ReconcileWorkerQueueHealth(_ context.Context, _ time.Time, _, _ time.Duration, _ bool, ownerJID string) (domain.WorkerHealth, *domain.WorkerHealthTransition, error) {
	f.ownerJID = ownerJID
	f.reconciled++
	return f.health, f.transition, nil
}

func (f *fakeHealthStore) Audit(_ context.Context, _, _, action, _, _, _ string, _ any) error {
	f.auditActions = append(f.auditActions, action)
	return nil
}

type fakeHealthOwner struct {
	jid string
	err error
}

func (f fakeHealthOwner) JID(context.Context) (string, error) { return f.jid, f.err }

func TestHealthWatchdogPropagatesOwnerAndAuditsTransitions(t *testing.T) {
	health := domain.WorkerHealth{Status: "stale", ReadyJobs: 2, QueueStalled: true, Summary: "queue stalled"}
	store := &fakeHealthStore{health: health, transition: &domain.WorkerHealthTransition{Kind: "opened", Health: health}}
	watchdog := NewHealthWatchdog(store, fakeHealthOwner{jid: "owner@s.whatsapp.net"}, 30*time.Second, time.Minute, 5*time.Minute, true)
	got, transition, err := watchdog.Sweep(context.Background(), time.Now())
	if err != nil || !got.QueueStalled || transition == nil || transition.Kind != "opened" {
		t.Fatalf("unexpected sweep result health=%#v transition=%#v err=%v", got, transition, err)
	}
	if store.ownerJID != "owner@s.whatsapp.net" || len(store.auditActions) != 1 || store.auditActions[0] != "worker.queue_opened" {
		t.Fatalf("owner or audit not propagated: owner=%q audits=%#v", store.ownerJID, store.auditActions)
	}
}

func TestHealthWatchdogDoesNotMutateWithoutNotificationOwner(t *testing.T) {
	store := &fakeHealthStore{}
	watchdog := NewHealthWatchdog(store, fakeHealthOwner{err: errors.New("lookup failed")}, 30*time.Second, time.Minute, 5*time.Minute, true)
	if _, _, err := watchdog.Sweep(context.Background(), time.Now()); err == nil {
		t.Fatal("owner lookup failure was ignored")
	}
	if store.reconciled != 0 {
		t.Fatal("health state changed without resolving the notification owner")
	}
}
