package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
)

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
