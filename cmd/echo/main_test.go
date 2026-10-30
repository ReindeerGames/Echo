package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
)

type fakeComponentReporter struct {
	failures  atomic.Int32
	recovered atomic.Int32
	window    time.Duration
	recovery  chan struct{}
}

func (f *fakeComponentReporter) Reconcile(_ context.Context, _ string, _ string, healthy bool) error {
	if healthy {
		if f.recovered.Add(1) == 1 && f.recovery != nil {
			close(f.recovery)
		}
	} else {
		f.failures.Add(1)
	}
	return nil
}

func (f *fakeComponentReporter) StableWindow() time.Duration { return f.window }

type componentReconcileCall struct {
	name     string
	status   string
	healthy  bool
	ownerJID string
}

type fakeComponentHealthStore struct {
	calls []componentReconcileCall
}

func (f *fakeComponentHealthStore) ReconcileRuntimeComponent(_ context.Context, name, status string, healthy bool, _ time.Time, ownerJID string) (*domain.RuntimeComponentTransition, error) {
	f.calls = append(f.calls, componentReconcileCall{name: name, status: status, healthy: healthy, ownerJID: ownerJID})
	return nil, nil
}

type fakeComponentOwner struct {
	calls atomic.Int32
	jid   string
}

func (f *fakeComponentOwner) JID(context.Context) (string, error) {
	f.calls.Add(1)
	return f.jid, nil
}

func TestSuperviseComponentReportsFailureAndRestarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	state := runtimestate.New()
	var attempts atomic.Int32
	done := make(chan struct{})
	go func() {
		superviseComponent(ctx, state, "test-loop", func() error {
			attempts.Add(1)
			return errors.New("test failure")
		})
		close(done)
	}()

	deadline := time.Now().Add(2500 * time.Millisecond)
	for attempts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		cancel()
		<-done
		t.Fatalf("component was not restarted; attempts=%d", attempts.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop after cancellation")
	}
	components := state.Components()
	if len(components) != 1 || components[0].Status != "stopped" {
		t.Fatalf("unexpected final component state: %#v", components)
	}
}

func TestSuperviseComponentReportsRecoveryOnlyAfterStableWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	state := runtimestate.New()
	reporter := &fakeComponentReporter{window: 20 * time.Millisecond, recovery: make(chan struct{})}
	var attempts atomic.Int32
	done := make(chan struct{})
	go func() {
		superviseComponent(ctx, state, "test-loop", func() error {
			if attempts.Add(1) == 1 {
				return errors.New("first attempt failed")
			}
			<-ctx.Done()
			return nil
		}, reporter)
		close(done)
	}()

	select {
	case <-reporter.recovery:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("stable recovery was not reported")
	}
	if reporter.failures.Load() < 1 || reporter.recovered.Load() != 1 {
		t.Fatalf("unexpected reports: failures=%d recovered=%d", reporter.failures.Load(), reporter.recovered.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop after cancellation")
	}
}

func TestResolveDisabledComponentAlertsDoesNotAddressDisabledWhatsApp(t *testing.T) {
	store := &fakeComponentHealthStore{}
	owner := &fakeComponentOwner{jid: "owner@s.whatsapp.net"}
	resolveDisabledComponentAlerts(context.Background(), store, owner, false, false)

	if owner.calls.Load() != 0 {
		t.Fatalf("owner was looked up while WhatsApp was disabled: calls=%d", owner.calls.Load())
	}
	if len(store.calls) != 3 {
		t.Fatalf("unexpected reconciliation calls: %#v", store.calls)
	}
	for _, call := range store.calls {
		if call.status != "disabled" || !call.healthy || call.ownerJID != "" {
			t.Fatalf("unsafe disabled reconciliation: %#v", call)
		}
	}
}

func TestResolveDisabledWorkerAlertCanNotifyConfiguredOwner(t *testing.T) {
	store := &fakeComponentHealthStore{}
	owner := &fakeComponentOwner{jid: "owner@s.whatsapp.net"}
	resolveDisabledComponentAlerts(context.Background(), store, owner, false, true)

	if owner.calls.Load() != 1 || len(store.calls) != 1 {
		t.Fatalf("unexpected calls: owner=%d store=%#v", owner.calls.Load(), store.calls)
	}
	call := store.calls[0]
	if call.name != "worker" || call.status != "disabled" || !call.healthy || call.ownerJID != owner.jid {
		t.Fatalf("unexpected disabled worker reconciliation: %#v", call)
	}
}
