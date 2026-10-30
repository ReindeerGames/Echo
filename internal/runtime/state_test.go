package runtime

import "testing"

func TestComponentReadinessRequiresEveryExpectedLoop(t *testing.T) {
	state := New()
	ready, checks := state.ComponentReadiness([]string{"worker", "monitor-watchdog"})
	if ready || checks["worker"] != "starting" {
		t.Fatalf("missing components reported ready: ready=%v checks=%#v", ready, checks)
	}
	state.UpdateComponent("worker", "running", "")
	state.UpdateComponent("monitor-watchdog", "error", "retrying")
	ready, checks = state.ComponentReadiness([]string{"worker", "monitor-watchdog"})
	if ready || checks["worker"] != "running" || checks["monitor-watchdog"] != "error" {
		t.Fatalf("failed component reported ready: ready=%v checks=%#v", ready, checks)
	}
	state.UpdateComponent("monitor-watchdog", "running", "")
	ready, checks = state.ComponentReadiness([]string{"worker", "monitor-watchdog"})
	if !ready || checks["monitor-watchdog"] != "running" {
		t.Fatalf("running components reported unavailable: ready=%v checks=%#v", ready, checks)
	}
}

func TestComponentsAreSortedAndDetailsBounded(t *testing.T) {
	state := New()
	state.UpdateComponent("worker", "error", string(make([]byte, 400)))
	state.UpdateComponent("gateway", "running", "")
	components := state.Components()
	if len(components) != 2 || components[0].Name != "gateway" || components[1].Name != "worker" || len(components[1].Detail) != 300 {
		t.Fatalf("unexpected components: %#v", components)
	}
}
