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
	policies := policy.New()
	owners := owner.New(st, cfg.WhatsApp.OwnerJID)
	workflows := workflow.New(st, policies, owners, cfg.Security.ApprovalTTL)

	vault, err := security.NewVault(st, cfg.Security.EncryptionKey)
	if err != nil {
		return err
	}
	githubClient := githubapp.New(vault, cfg.GitHub.APIBaseURL, cfg.Worker.MaxRepoBytes)
	aiClient := ai.NewManaged(vault, cfg.AI.Enabled, cfg.AI.BaseURL, cfg.AI.APIKey, cfg.AI.Model)

	gateway := whatsapp.New(st, workflows, state, cfg.Database.WhatsAppDSN)
	server := httpapi.New(cfg, st, workflows, owners, state, githubClient, aiClient, gateway)
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run() }()
	if cfg.WhatsApp.Enabled {
		go runGatewayLoop(ctx, gateway, state)
	}
	if cfg.Worker.Enabled {
		runner := worker.New(st, githubClient, aiClient, owners, cfg.Worker.WorkspaceRoot, cfg.Worker.PollInterval, cfg.Worker.JobLease, cfg.Worker.MaxFileBytes)
		go func() {
			if err := runner.Run(ctx); err != nil {
				slog.Error("worker stopped", "error", err)
			}
		}()
	}

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

func runGatewayLoop(ctx context.Context, gateway *whatsapp.Gateway, state *runtimestate.State) {
	backoff := time.Second
	for {
		err := gateway.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("WhatsApp gateway stopped", "error", err)
			if state.WhatsApp().Status != "unpaired" {
				state.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) { s.Status = "error"; s.LastError = err.Error() })
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > time.Minute {
			backoff = time.Minute
		}
	}
}
