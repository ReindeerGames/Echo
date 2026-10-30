package store

import (
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestRuntimeComponentValidationAndNotificationsAreSafe(t *testing.T) {
	for _, invalid := range []string{"", "worker watchdog", "../worker", strings.Repeat("x", 81), "wørker"} {
		if validRuntimeComponent(invalid) {
			t.Errorf("invalid component accepted: %q", invalid)
		}
	}
	if !validRuntimeComponent("worker-watchdog") {
		t.Fatal("valid component rejected")
	}
	opened := runtimeComponentNotification(domain.RuntimeComponentTransition{Kind: "opened", Component: "worker", Status: "error", Summary: runtimeComponentSummary("worker", "error", false)})
	resolved := runtimeComponentNotification(domain.RuntimeComponentTransition{Kind: "resolved", Component: "worker", Status: "running", Summary: runtimeComponentSummary("worker", "running", true)})
	if !strings.Contains(opened, "automatic restart is in progress") || !strings.Contains(resolved, "remained running") {
		t.Fatalf("unexpected notifications opened=%q resolved=%q", opened, resolved)
	}
}
