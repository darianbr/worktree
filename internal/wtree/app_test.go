package wtree

import (
	"context"
	"os"
	"testing"
)

func TestRemoveLastRouteStopsContainer(t *testing.T) {
	d, _, r := fixture(t)
	route := Route{Slug: "alpha", Port: 8080, Worktree: "/work/alpha"}
	if err := r.Register(route); err != nil {
		t.Fatal(err)
	}
	config, _ := RenderCaddyfile([]Route{route})
	if err := os.WriteFile(d.Paths.Config, config, 0600); err != nil {
		t.Fatal(err)
	}
	e := &recordingEngine{state: ContainerRunning}
	d.Engine = func(context.Context) (LifecycleEngine, error) { return e, nil }
	removed, err := removeRoute(context.Background(), d, "alpha", "")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if !sameCalls(e.calls, []string{"CheckMounts", "Stop"}) {
		t.Fatalf("engine calls = %v, want [CheckMounts Stop]", e.calls)
	}
	if _, err := os.Stat(d.Paths.Config); !os.IsNotExist(err) {
		t.Fatalf("Caddyfile not removed: %v", err)
	}
}

func TestRemoveLastRouteRollbackRestarts(t *testing.T) {
	d, e, r := fixture(t)
	route := Route{Slug: "alpha", Port: 8080, Worktree: "/work/alpha"}
	if err := r.Register(route); err != nil {
		t.Fatal(err)
	}
	config, _ := RenderCaddyfile([]Route{route})
	if err := os.WriteFile(d.Paths.Config, config, 0600); err != nil {
		t.Fatal(err)
	}
	e.state = ContainerRunning
	undo, err := activate(context.Background(), d, nil)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := os.Stat(d.Paths.Config); !os.IsNotExist(err) {
		t.Fatalf("Caddyfile not removed: %v", err)
	}
	if e.state != ContainerStopped {
		t.Fatalf("container not stopped: %v", e.state)
	}
	// Simulate a commit failure after activate succeeded: rollback must restore
	// the Caddyfile and restart the proxy.
	if err := undo(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if e.state != ContainerRunning {
		t.Fatalf("container not restarted: %v", e.state)
	}
	if e.active != string(config) {
		t.Fatalf("Caddyfile not restored: %q", e.active)
	}
}
