package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ReindeerGames/echo/internal/ai"
	"github.com/ReindeerGames/echo/internal/briefing"
	"github.com/ReindeerGames/echo/internal/config"
	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/githubapp"
	"github.com/ReindeerGames/echo/internal/httpapi"
	"github.com/ReindeerGames/echo/internal/maintenance"
	"github.com/ReindeerGames/echo/internal/monitor"
	"github.com/ReindeerGames/echo/internal/owner"
	"github.com/ReindeerGames/echo/internal/policy"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
	"github.com/ReindeerGames/echo/internal/security"
	"github.com/ReindeerGames/echo/internal/store"
	"github.com/ReindeerGames/echo/internal/whatsapp"
	"github.com/ReindeerGames/echo/internal/worker"
	"github.com/ReindeerGames/echo/internal/workflow"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Echo stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Environment == "production" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, cfg.Database.CoreDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	state := runtimestate.New()
	expectedComponents := []string{"monitor-watchdog", "retention-maintenance", "worker-watchdog"}
	if cfg.Worker.Enabled {
		expectedComponents = append(expectedComponents, "worker")
	}
	if cfg.WhatsApp.Enabled {
		expectedComponents = append(expectedComponents, "whatsapp-gateway", "daily-brief-scheduler")
	}
	policies := policy.New()
	owners := owner.New(st, cfg.WhatsApp.OwnerJID)
	reporter := &runtimeComponentReporter{store: st, owners: owners, recoveryWindow: 30 * time.Second}
	resolveDisabledComponentAlerts(ctx, st, owners, cfg.Worker.Enabled, cfg.WhatsApp.Enabled)
	retention := maintenance.NewRetention(st, cfg.Assistant.Retention, cfg.Assistant.AttachmentRetention, cfg.Monitor.Retention, cfg.WhatsApp.OutboxRetention, 24*time.Hour)
	go superviseComponent(ctx, state, "retention-maintenance", func() error { return retention.Run(ctx) }, reporter)

	vault, err := security.NewVault(st, cfg.Security.EncryptionKey)
	if err != nil {
		return err
	}
	githubClient := githubapp.New(vault, cfg.GitHub.APIBaseURL, cfg.Worker.MaxRepoBytes)
	aiClient := ai.NewManaged(vault, cfg.AI.Enabled, cfg.AI.BaseURL, cfg.AI.APIKey, cfg.AI.Model)
	workflows := workflow.New(st, policies, owners, aiClient, githubClient, state, expectedComponents, cfg.Assistant.HistoryLimit, cfg.Security.ApprovalTTL)

	gateway := whatsapp.New(st, workflows, state, cfg.Database.WhatsAppDSN)
	server := httpapi.New(cfg, st, workflows, owners, state, githubClient, aiClient, gateway)
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run() }()
	if cfg.WhatsApp.Enabled {
		go superviseComponent(ctx, state, "whatsapp-gateway", func() error { return runGatewayOnce(ctx, gateway, state) }, reporter)
		dailyBriefs := briefing.New(st, owners, workflows, time.Minute)
		go superviseComponent(ctx, state, "daily-brief-scheduler", func() error { return dailyBriefs.Run(ctx) }, reporter)
	}
	if cfg.Worker.Enabled {
		runner := worker.New(st, githubClient, aiClient, owners, cfg.Worker.WorkspaceRoot, cfg.Worker.PollInterval, cfg.Worker.JobLease, cfg.Worker.HeartbeatInterval, cfg.Worker.MaxFileBytes)
		go superviseComponent(ctx, state, "worker", func() error { return runner.Run(ctx) }, reporter)
	}
	workerWatchdog := worker.NewHealthWatchdog(st, owners, cfg.Monitor.WatchdogInterval, cfg.Worker.HeartbeatTimeout, cfg.Worker.QueueStallTimeout, cfg.Worker.Enabled)
	go superviseComponent(ctx, state, "worker-watchdog", func() error { return workerWatchdog.Run(ctx) }, reporter)
	heartbeatWatchdog := monitor.NewWatchdog(st, owners, cfg.Monitor.WatchdogInterval)
	go superviseComponent(ctx, state, "monitor-watchdog", func() error { return heartbeatWatchdog.Run(ctx) }, reporter)

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if err != nil {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

type componentHealthStore interface {
	ReconcileRuntimeComponent(context.Context, string, string, bool, time.Time, string) (*domain.RuntimeComponentTransition, error)
}

type componentOwnerLookup interface {
	JID(context.Context) (string, error)
}

type componentHealthReporter interface {
	Reconcile(context.Context, string, string, bool) error
	StableWindow() time.Duration
}

type runtimeComponentReporter struct {
	store          componentHealthStore
	owners         componentOwnerLookup
	recoveryWindow time.Duration
}

func (r *runtimeComponentReporter) Reconcile(ctx context.Context, name, status string, healthy bool) error {
	eventCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ownerJID := ""
	if r.owners != nil {
		var err error
		ownerJID, err = r.owners.JID(eventCtx)
		if err != nil {
			return err
		}
	}
	_, err := r.store.ReconcileRuntimeComponent(eventCtx, name, status, healthy, time.Now().UTC(), ownerJID)
	return err
}

func (r *runtimeComponentReporter) StableWindow() time.Duration {
	if r.recoveryWindow <= 0 {
		return 30 * time.Second
	}
	return r.recoveryWindow
}

func resolveDisabledComponentAlerts(ctx context.Context, st componentHealthStore, owners componentOwnerLookup, workerEnabled, whatsappEnabled bool) {
	var disabled []string
	if !workerEnabled {
		disabled = append(disabled, "worker")
	}
	if !whatsappEnabled {
		disabled = append(disabled, "whatsapp-gateway", "daily-brief-scheduler")
	}
	if len(disabled) == 0 {
		return
	}
	ownerJID := ""
	if whatsappEnabled && owners != nil {
		var err error
		ownerJID, err = owners.JID(ctx)
		if err != nil {
			slog.Error("resolve owner for disabled component alerts", "error", err)
			ownerJID = ""
		}
	}
	for _, name := range disabled {
		if _, err := st.ReconcileRuntimeComponent(ctx, name, "disabled", true, time.Now().UTC(), ownerJID); err != nil {
			slog.Error("clear disabled component alert", "component", name, "error", err)
		}
	}
}

func superviseComponent(ctx context.Context, state *runtimestate.State, name string, run func() error, reporters ...componentHealthReporter) {
	var reporter componentHealthReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	backoff := time.Second
	failed := false
	for {
		if ctx.Err() != nil {
			state.UpdateComponent(name, "stopped", "shutdown requested")
			return
		}
		if failed && reporter != nil {
			if err := reporter.Reconcile(ctx, name, "error", false); err != nil && ctx.Err() == nil {
				slog.Error("record component failure", "component", name, "error", err)
			}
		}
		state.UpdateComponent(name, "running", "")
		startedAt := time.Now()
		result := make(chan error, 1)
		go func() { result <- run() }()
		var stableTimer *time.Timer
		var stable <-chan time.Time
		if reporter != nil {
			stableTimer = time.NewTimer(reporter.StableWindow())
			stable = stableTimer.C
		}
	attempt:
		for {
			select {
			case <-ctx.Done():
				if stableTimer != nil {
					stableTimer.Stop()
				}
				state.UpdateComponent(name, "stopped", "shutdown requested")
				return
			case <-stable:
				if err := reporter.Reconcile(ctx, name, "running", true); err != nil {
					if ctx.Err() != nil {
						state.UpdateComponent(name, "stopped", "shutdown requested")
						return
					}
					slog.Error("record component recovery", "component", name, "error", err)
					stableTimer.Reset(reporter.StableWindow())
					stable = stableTimer.C
					continue
				}
				failed = false
				stable = nil
			case err := <-result:
				if stableTimer != nil {
					stableTimer.Stop()
				}
				if ctx.Err() != nil {
					state.UpdateComponent(name, "stopped", "shutdown requested")
					return
				}
				state.UpdateComponent(name, "error", "stopped unexpectedly; retry scheduled")
				failed = true
				if reporter != nil {
					if reportErr := reporter.Reconcile(ctx, name, "error", false); reportErr != nil && ctx.Err() == nil {
						slog.Error("record component failure", "component", name, "error", reportErr)
					}
				}
				if time.Since(startedAt) >= time.Minute {
					backoff = time.Second
				}
				if err != nil {
					slog.Error("component stopped", "component", name, "error", err)
				} else {
					slog.Error("component stopped unexpectedly", "component", name)
				}
				timer := time.NewTimer(backoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					state.UpdateComponent(name, "stopped", "shutdown requested")
					return
				case <-timer.C:
				}
				if backoff < time.Minute {
					backoff *= 2
					if backoff > time.Minute {
						backoff = time.Minute
					}
				}
				break attempt
			}
		}
	}
}

func runGatewayOnce(ctx context.Context, gateway *whatsapp.Gateway, state *runtimestate.State) error {
	err := gateway.Run(ctx)
	if err != nil && ctx.Err() == nil && state.WhatsApp().Status != "unpaired" {
		state.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
			s.Status = "error"
			s.LastError = err.Error()
		})
	}
	return err
}
