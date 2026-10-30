package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ReindeerGames/echo/internal/ai"
	"github.com/ReindeerGames/echo/internal/config"
	"github.com/ReindeerGames/echo/internal/githubapp"
	"github.com/ReindeerGames/echo/internal/httpapi"
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
	pruned, err := st.PruneConversationHistory(ctx, time.Now().UTC().Add(-cfg.Assistant.Retention))
	if err != nil {
		return err
	}
	if pruned > 0 {
		slog.Info("pruned expired assistant conversation messages", "count", pruned)
	}
	state := runtimestate.New()
	expectedComponents := []string{"monitor-watchdog", "worker-watchdog"}
	if cfg.Worker.Enabled {
		expectedComponents = append(expectedComponents, "worker")
	}
	if cfg.WhatsApp.Enabled {
		expectedComponents = append(expectedComponents, "whatsapp-gateway")
	}
	policies := policy.New()
	owners := owner.New(st, cfg.WhatsApp.OwnerJID)

	vault, err := security.NewVault(st, cfg.Security.EncryptionKey)
	if err != nil {
		return err
	}
	githubClient := githubapp.New(vault, cfg.GitHub.APIBaseURL, cfg.Worker.MaxRepoBytes)
	aiClient := ai.NewManaged(vault, cfg.AI.Enabled, cfg.AI.BaseURL, cfg.AI.APIKey, cfg.AI.Model)
	workflows := workflow.New(st, policies, owners, aiClient, state, expectedComponents, cfg.Assistant.HistoryLimit, cfg.Security.ApprovalTTL)

	gateway := whatsapp.New(st, workflows, state, cfg.Database.WhatsAppDSN)
	server := httpapi.New(cfg, st, workflows, owners, state, githubClient, aiClient, gateway)
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run() }()
	if cfg.WhatsApp.Enabled {
		go superviseComponent(ctx, state, "whatsapp-gateway", func() error { return runGatewayOnce(ctx, gateway, state) })
	}
	if cfg.Worker.Enabled {
		runner := worker.New(st, githubClient, aiClient, owners, cfg.Worker.WorkspaceRoot, cfg.Worker.PollInterval, cfg.Worker.JobLease, cfg.Worker.HeartbeatInterval, cfg.Worker.MaxFileBytes)
		go superviseComponent(ctx, state, "worker", func() error { return runner.Run(ctx) })
	}
	workerWatchdog := worker.NewHealthWatchdog(st, owners, cfg.Monitor.WatchdogInterval, cfg.Worker.HeartbeatTimeout, cfg.Worker.QueueStallTimeout, cfg.Worker.Enabled)
	go superviseComponent(ctx, state, "worker-watchdog", func() error { return workerWatchdog.Run(ctx) })
	heartbeatWatchdog := monitor.NewWatchdog(st, owners, cfg.Monitor.WatchdogInterval)
	go superviseComponent(ctx, state, "monitor-watchdog", func() error { return heartbeatWatchdog.Run(ctx) })

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

func superviseComponent(ctx context.Context, state *runtimestate.State, name string, run func() error) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			state.UpdateComponent(name, "stopped", "shutdown requested")
			return
		}
		state.UpdateComponent(name, "running", "")
		startedAt := time.Now()
		err := run()
		if ctx.Err() != nil {
			state.UpdateComponent(name, "stopped", "shutdown requested")
			return
		}
		state.UpdateComponent(name, "error", "stopped unexpectedly; retry scheduled")
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
