package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ReindeerGames/echo/internal/observer"
)

var errDryRunAttention = errors.New("one or more dry-run probes need attention")

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		if errors.Is(err, errDryRunAttention) {
			_, _ = fmt.Fprintln(os.Stderr, "echo-observer:", err)
			os.Exit(1)
		}
		exit(err)
	}
}

type dryRunResult struct {
	Source    string        `json:"source"`
	Status    string        `json:"status"`
	Healthy   int           `json:"healthy"`
	Degraded  int           `json:"degraded"`
	Unhealthy int           `json:"unhealthy"`
	Events    []dryRunEvent `json:"events"`
}

type dryRunEvent struct {
	EventID     string          `json:"eventId"`
	ServiceID   string          `json:"serviceId"`
	Source      string          `json:"source"`
	Status      string          `json:"status"`
	Severity    string          `json:"severity"`
	Summary     string          `json:"summary"`
	Fingerprint string          `json:"fingerprint"`
	Details     json.RawMessage `json:"details,omitempty"`
	ObservedAt  time.Time       `json:"observedAt"`
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("echo-observer", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/etc/echo-observer.json", "path to the observer JSON configuration")
	once := flags.Bool("once", false, "run one observation and delivery cycle, then exit")
	check := flags.Bool("check", false, "validate configuration without probing or delivery")
	dryRun := flags.Bool("dry-run", false, "run probes and print sanitized evidence without delivery")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	modes := 0
	for _, enabled := range []bool{*once, *check, *dryRun} {
		if enabled {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("-once, -check, and -dry-run are mutually exclusive")
	}
	cfg, err := observer.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if *check {
		_, err = fmt.Fprintf(stdout, "observer configuration is valid: source=%s probes=%d interval=%s timeout=%s\n", cfg.Source, probeCount(cfg), cfg.Interval.Duration, cfg.Timeout.Duration)
		return err
	}
	if *dryRun {
		service, err := observer.NewCollector(cfg)
		if err != nil {
			return err
		}
		events, err := service.CollectOnce(ctx)
		if err != nil {
			return err
		}
		result := dryRunResult{Source: cfg.Source, Status: "healthy", Events: make([]dryRunEvent, 0, len(events))}
		for _, event := range events {
			result.Events = append(result.Events, dryRunEvent{
				EventID: event.EventID, ServiceID: event.ServiceID, Source: event.Source, Status: event.Status,
				Severity: event.Severity, Summary: event.Summary, Fingerprint: event.Fingerprint,
				Details: event.Details, ObservedAt: event.ObservedAt,
			})
			switch event.Status {
			case "healthy":
				result.Healthy++
			case "degraded":
				result.Degraded++
			default:
				result.Unhealthy++
			}
		}
		if result.Unhealthy > 0 {
			result.Status = "unhealthy"
		} else if result.Degraded > 0 {
			result.Status = "degraded"
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return err
		}
		if result.Status != "healthy" {
			return fmt.Errorf("%w: %d degraded, %d unhealthy", errDryRunAttention, result.Degraded, result.Unhealthy)
		}
		return nil
	}
	service, err := observer.New(cfg, getenv("ECHO_OBSERVER_WEBHOOK_SECRET"))
	if err != nil {
		return err
	}
	if *once {
		return service.RunOnce(ctx)
	}
	return service.Run(ctx)
}

func probeCount(cfg observer.Config) int {
	return len(cfg.Probes) + len(cfg.TCPProbes) + len(cfg.SystemProbes) + len(cfg.ProcessProbes) + len(cfg.DockerProbes) + len(cfg.FreshnessProbes)
}

func exit(err error) {
	slog.Error("Echo observer stopped", "error", err)
	_, _ = fmt.Fprintln(os.Stderr, "echo-observer:", err)
	os.Exit(1)
}
