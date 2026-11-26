package wtree

import (
	"context"
	"errors"
	"os"
	"testing"
)

func sameCalls(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type recordingEngine struct {
	state     ContainerState
	startErr  error
	reloadErr error
	calls     []string
}

func (e *recordingEngine) State(context.Context) (ContainerState, error) { return e.state, nil }
func (e *recordingEngine) CheckMounts(context.Context, Paths, ContainerState) error {
	e.calls = append(e.calls, "CheckMounts")
	return nil
}
func (e *recordingEngine) Start(context.Context, Paths) error {
	e.calls = append(e.calls, "Start")
	if e.startErr != nil {
		return e.startErr
	}
	e.state = ContainerRunning
	return nil
}
func (e *recordingEngine) StartExisting(context.Context) error {
	e.calls = append(e.calls, "StartExisting")
	if e.startErr != nil {
		return e.startErr
	}
	e.state = ContainerRunning
	return nil
}
func (e *recordingEngine) ValidateCandidate(context.Context, Paths, ContainerState) error { return nil }
func (e *recordingEngine) Reload(context.Context) error {
	e.calls = append(e.calls, "Reload")
	if e.reloadErr != nil {
		return e.reloadErr
	}
	return nil
}
func (e *recordingEngine) Stop(context.Context) error {
	e.calls = append(e.calls, "Stop")
	e.state = ContainerStopped
	return nil
}
func (e *recordingEngine) Remove(context.Context) error {
	e.calls = append(e.calls, "Remove")
	e.state = ContainerAbsent
	return nil
}

type configSeed int

const (
	configAbsent configSeed = iota
	configFresh
	configStale
)

func TestReconcileTable(t *testing.T) {
	route := Route{Slug: "alpha", Port: 8080, Worktree: "/work/alpha"}
	rows := []struct {
		name        string
		routes      []Route
		engineState ContainerState
		startErr    error
		seed        configSeed
		want        ProxyState
		wantErr     bool
		wantCalls   []string
	}{
		{"routes+absent starts", []Route{route}, ContainerAbsent, nil, configAbsent, ProxyServing, false, []string{"CheckMounts", "Start"}},
		{"routes+stopped starts existing", []Route{route}, ContainerStopped, nil, configStale, ProxyServing, false, []string{"CheckMounts", "StartExisting"}},
		{"routes+running stale config reloads", []Route{route}, ContainerRunning, nil, configStale, ProxyServing, false, []string{"CheckMounts", "Reload"}},
		{"routes+running fresh config no-op", []Route{route}, ContainerRunning, nil, configFresh, ProxyServing, false, []string{"CheckMounts"}},
		{"routes+absent start fails", []Route{route}, ContainerAbsent, errors.New("boom"), configAbsent, ProxyDown, true, []string{"CheckMounts", "Start"}},
		{"no routes+running stops", nil, ContainerRunning, nil, configStale, ProxyDown, false, []string{"Stop"}},
		{"no routes+absent nothing", nil, ContainerAbsent, nil, configAbsent, ProxyDown, false, nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			d, _, r := fixture(t)
			if len(row.routes) > 0 {
				if err := r.Register(row.routes[0]); err != nil {
					t.Fatal(err)
				}
			}
			var wantConfig []byte
			if len(row.routes) > 0 {
				wantConfig, _ = RenderCaddyfile(row.routes)
			}
			switch row.seed {
			case configFresh:
				if err := os.WriteFile(d.Paths.Config, wantConfig, 0600); err != nil {
					t.Fatal(err)
				}
			case configStale:
				if err := os.WriteFile(d.Paths.Config, []byte("stale"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e := &recordingEngine{state: row.engineState, startErr: row.startErr}
			d.Engine = func(context.Context) (LifecycleEngine, error) { return e, nil }
			got, err := Reconcile(context.Background(), d)
			if got != row.want {
				t.Errorf("state = %v, want %v", got, row.want)
			}
			if (err != nil) != row.wantErr {
				t.Errorf("error = %v, wantErr %v", err, row.wantErr)
			}
			if !sameCalls(e.calls, row.wantCalls) {
				t.Errorf("engine calls = %v, want %v", e.calls, row.wantCalls)
			}
		})
	}
}
