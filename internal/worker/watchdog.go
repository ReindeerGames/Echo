package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

type HealthStore interface {
	ReconcileWorkerQueueHealth(context.Context, time.Time, time.Duration, time.Duration, bool, string) (domain.WorkerHealth, *domain.WorkerHealthTransition, error)
	Audit(context.Context, string, string, string, string, string, string, any) error
}

type HealthOwnerLookup interface {
	JID(context.Context) (string, error)
}

type HealthWatchdog struct {
	store            HealthStore
	owners           HealthOwnerLookup
	interval         time.Duration
	heartbeatTimeout time.Duration
	queueTimeout     time.Duration
	workerExpected   bool
	now              func() time.Time
}

func NewHealthWatchdog(store HealthStore, owners HealthOwnerLookup, interval, heartbeatTimeout, queueTimeout time.Duration, workerExpected bool) *HealthWatchdog {
	if interval < 5*time.Second || interval > 5*time.Minute {
		interval = 30 * time.Second
	}
	return &HealthWatchdog{store: store, owners: owners, interval: interval, heartbeatTimeout: heartbeatTimeout, queueTimeout: queueTimeout, workerExpected: workerExpected, now: func() time.Time { return time.Now().UTC() }}
}

func (w *HealthWatchdog) Run(ctx context.Context) error {
	if _, _, err := w.Sweep(ctx, w.now()); err != nil {
		return err
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, _, err := w.Sweep(ctx, w.now()); err != nil {
				return err
			}
		}
	}
}

func (w *HealthWatchdog) Sweep(ctx context.Context, now time.Time) (domain.WorkerHealth, *domain.WorkerHealthTransition, error) {
	ownerJID := ""
	if w.owners != nil {
		var err error
		ownerJID, err = w.owners.JID(ctx)
		if err != nil {
			return domain.WorkerHealth{}, nil, fmt.Errorf("resolve worker health notification owner: %w", err)
		}
	}
	health, transition, err := w.store.ReconcileWorkerQueueHealth(ctx, now, w.heartbeatTimeout, w.queueTimeout, w.workerExpected, ownerJID)
	if err != nil {
		return domain.WorkerHealth{}, nil, fmt.Errorf("reconcile worker queue health: %w", err)
	}
	if transition != nil {
		_ = w.store.Audit(ctx, "system", "echo-worker-watchdog", "worker.queue_"+transition.Kind, "worker", "queue", "succeeded", map[string]any{"readyJobs": health.ReadyJobs, "oldestReadyAt": health.OldestReadyAt, "workerLastSeenAt": health.LastSeenAt})
	}
	return health, transition, nil
}
