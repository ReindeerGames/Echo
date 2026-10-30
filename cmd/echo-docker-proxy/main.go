package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ReindeerGames/echo/internal/dockerproxy"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Echo Docker proxy stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := value("ECHO_DOCKER_PROXY_LISTEN", "127.0.0.1:23750")
	if !loopbackAddress(listen) {
		return errors.New("ECHO_DOCKER_PROXY_LISTEN must bind to a loopback IP")
	}
	containers := strings.Split(strings.TrimSpace(os.Getenv("ECHO_DOCKER_PROXY_CONTAINERS")), ",")
	proxy, err := dockerproxy.New(value("ECHO_DOCKER_SOCKET", "/var/run/docker.sock"), containers)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: listen, Handler: proxy.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func value(name, fallback string) string {
	if configured := strings.TrimSpace(os.Getenv(name)); configured != "" {
		return configured
	}
	return fallback
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
