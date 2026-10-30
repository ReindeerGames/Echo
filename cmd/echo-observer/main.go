package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ReindeerGames/echo/internal/observer"
)

func main() {
	configPath := flag.String("config", "/etc/echo-observer.json", "path to the observer JSON configuration")
	once := flag.Bool("once", false, "run one observation cycle and exit")
	flag.Parse()

	cfg, err := observer.LoadConfig(*configPath)
	if err != nil {
		exit(err)
	}
	service, err := observer.New(cfg, os.Getenv("ECHO_OBSERVER_WEBHOOK_SECRET"))
	if err != nil {
		exit(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *once {
		err = service.RunOnce(ctx)
	} else {
		err = service.Run(ctx)
	}
	if err != nil {
		exit(err)
	}
}

func exit(err error) {
	slog.Error("Echo observer stopped", "error", err)
	_, _ = fmt.Fprintln(os.Stderr, "echo-observer:", err)
	os.Exit(1)
}
