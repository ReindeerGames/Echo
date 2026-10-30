package githubmonitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/githubapp"
)

const maxRepositoriesPerSweep = 100

type Store interface {
	ListRepositories(context.Context, string) ([]domain.Repository, error)
	ReconcileRepositoryWorkflowAlert(context.Context, string, string, string, string, []string, time.Time, string) (*domain.RepositoryWorkflowTransition, error)
}

type Inspector interface {
	TokenStatus(context.Context) (githubapp.TokenStatus, error)
	InspectRepository(context.Context, domain.Repository) (githubapp.RepositoryStatus, error)
}

type OwnerLookup interface {
	JID(context.Context) (string, error)
}

type Watchdog struct {
	store     Store
	inspector Inspector
	owners    OwnerLookup
	interval  time.Duration
	now       func() time.Time
}

func New(store Store, inspector Inspector, owners OwnerLookup, interval time.Duration) *Watchdog {
	if interval < time.Minute || interval > time.Hour {
		interval = 15 * time.Minute
	}
	return &Watchdog{store: store, inspector: inspector, owners: owners, interval: interval, now: func() time.Time { return time.Now().UTC() }}
}

func (w *Watchdog) Run(ctx context.Context) error {
	if _, err := w.Sweep(ctx, w.now()); err != nil {
		return err
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := w.Sweep(ctx, w.now()); err != nil {
				return err
			}
		}
	}
}

func (w *Watchdog) Sweep(ctx context.Context, now time.Time) ([]domain.RepositoryWorkflowTransition, error) {
	if w.store == nil || w.inspector == nil {
		return nil, errors.New("GitHub workflow watchdog dependencies are unavailable")
	}
	ownerJID := ""
	if w.owners != nil {
		var err error
		ownerJID, err = w.owners.JID(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve GitHub notification owner: %w", err)
		}
	}
	repositories, err := w.store.ListRepositories(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list configured repositories: %w", err)
	}
	token, err := w.inspector.TokenStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("read GitHub connection status: %w", err)
	}
	transitions := make([]domain.RepositoryWorkflowTransition, 0)
	for _, repository := range repositories {
		if repository.Enabled && token.Configured {
			continue
		}
		transition, reconcileErr := w.store.ReconcileRepositoryWorkflowAlert(ctx, repository.ID, "disabled", "", "", nil, now, ownerJID)
		if reconcileErr != nil {
			return transitions, fmt.Errorf("clear disabled GitHub workflow alert for %s/%s: %w", repository.Owner, repository.Name, reconcileErr)
		}
		if transition != nil {
			transitions = append(transitions, *transition)
		}
	}
	if !token.Configured {
		return transitions, nil
	}
	enabled := make([]domain.Repository, 0, len(repositories))
	for _, repository := range repositories {
		if repository.Enabled {
			enabled = append(enabled, repository)
		}
	}
	if len(enabled) > maxRepositoriesPerSweep {
		return transitions, fmt.Errorf("%d enabled repositories exceed the watchdog limit of %d", len(enabled), maxRepositoriesPerSweep)
	}

	type result struct {
		repository domain.Repository
		status     githubapp.RepositoryStatus
		err        error
	}
	results := make([]result, len(enabled))
	workCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	jobs := make(chan int, len(enabled))
	for index := range enabled {
		jobs <- index
	}
	close(jobs)
	var wait sync.WaitGroup
	workers := min(4, len(enabled))
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for index := range jobs {
				result := result{repository: enabled[index]}
				inspectCtx, inspectCancel := context.WithTimeout(workCtx, 20*time.Second)
				result.status, result.err = w.inspector.InspectRepository(inspectCtx, enabled[index])
				inspectCancel()
				results[index] = result
			}
		}()
	}
	wait.Wait()

	var sweepErrors []error
	for _, result := range results {
		name := result.repository.Owner + "/" + result.repository.Name
		if result.err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("inspect %s: %w", name, result.err))
			continue
		}
		health := githubapp.AssessWorkflowHealth(result.status)
		if health.State != "attention" && health.State != "healthy" {
			continue
		}
		transition, reconcileErr := w.store.ReconcileRepositoryWorkflowAlert(ctx, result.repository.ID, health.State, result.status.HeadSHA, health.Summary, health.FailedWorkflows, now, ownerJID)
		if reconcileErr != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("reconcile %s: %w", name, reconcileErr))
			continue
		}
		if transition != nil {
			transitions = append(transitions, *transition)
		}
	}
	return transitions, errors.Join(sweepErrors...)
}
