package wtree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	calls   [][]string
	fail    string
	output  string
	capture func(ctx context.Context, call string)
}

func (f *fakeRunner) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if f.capture != nil {
		f.capture(ctx, strings.Join(append([]string{bin}, args...), " "))
	}
	f.calls = append(f.calls, append([]string{bin}, args...))
	if strings.Contains(strings.Join(args, " "), f.fail) && f.fail != "" {
		out := f.output
		if out == "" {
			out = "engine failure"
		}
		return []byte(out), errors.New("failed")
	}
	return nil, nil
}

func TestEngineStartUsesHostNetworkAndReadOnlyMounts(t *testing.T) {
	t.Setenv("WTREE_SELINUX_RELABEL", "")
	f := &fakeRunner{}
	e := Engine{Binary: "podman", Runner: f}
	err := e.Start(context.Background(), Paths{Dir: "/tmp/a b", Cert: "/tmp/cert", Key: "/tmp/key"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(f.calls[0], "|")
	for _, want := range []string{"podman", "--network=host", "/tmp/a b:/etc/wtree:ro", "/tmp/cert:/tls/fullchain.pem:ro", "/tmp/key:/tls/privkey.pem:ro", "wtree-caddy-data:/data"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q: %s", want, args)
		}
	}
}

func TestSelectEngineDiscoveryIsBounded(t *testing.T) {
	var infoCtx context.Context
	f := &fakeRunner{}
	f.capture = func(ctx context.Context, call string) {
		if strings.HasPrefix(call, "podman info") {
			infoCtx = ctx
		}
	}
	_, err := selectEngine(context.Background(), "linux", "podman", func(string) (string, error) { return "podman", nil }, f)
	if err != nil {
		t.Fatalf("selectEngine: %v", err)
	}
	if infoCtx == nil {
		t.Fatal("info probe not captured")
	}
	if _, ok := infoCtx.Deadline(); !ok {
		t.Fatal("discovery info probe has no deadline; a stalled daemon would hang the CLI")
	}
}

func TestEngineRejectsUnverifiedMacNetworking(t *testing.T) {
	called := false
	_, err := selectEngine(context.Background(), "darwin", "docker", func(name string) (string, error) {
		called = true
		return name, nil
	}, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "loopback-only") || called {
		t.Fatalf("expected fail-closed macOS error before invoking Docker, got %v (looked up: %t)", err, called)
	}
}

func TestEngineBindMountRelabelOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, binary, relabel, mode string
	}{
		{"podman default", "/usr/bin/podman", "", "ro"},
		{"podman invalid value", "podman", "true", "ro"},
		{"podman opt in", "/usr/bin/podman", "1", "ro,z"},
		{"docker ignores opt in", "/usr/bin/docker", "1", "ro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WTREE_SELINUX_RELABEL", tc.relabel)
			f := &fakeRunner{}
			e := Engine{Binary: tc.binary, Runner: f}
			p := Paths{Dir: "/tmp/config", Cert: "/tmp/cert", Key: "/tmp/key"}
			if err := e.ValidateCandidate(context.Background(), p, ContainerAbsent); err != nil {
				t.Fatal(err)
			}
			if err := e.ValidateCandidate(context.Background(), p, ContainerStopped); err != nil {
				t.Fatal(err)
			}
			if err := e.Start(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			for _, call := range f.calls {
				args := strings.Join(call, "|")
				for _, mount := range []string{"/tmp/config:/etc/wtree:", "/tmp/cert:/tls/fullchain.pem:", "/tmp/key:/tls/privkey.pem:"} {
					if !strings.Contains(args, "|"+mount+tc.mode+"|") {
						t.Errorf("mount %s%s missing from %s", mount, tc.mode, args)
					}
				}
			}
			start := strings.Join(f.calls[len(f.calls)-1], "|")
			marker := "dev.wtree.selinux-relabel="
			if tc.binary == "/usr/bin/docker" {
				if strings.Contains(start, marker) {
					t.Fatalf("Docker arguments changed: %s", start)
				}
			} else if want := marker + fmt.Sprint(tc.mode == "ro,z"); !strings.Contains(start, "|"+want+"|") {
				t.Fatalf("missing relabel history %q: %s", want, start)
			}
		})
	}
}

func TestEngineRunningValidationDoesNotCreateRelabelingMounts(t *testing.T) {
	t.Setenv("WTREE_SELINUX_RELABEL", "1")
	f := &fakeRunner{}
	e := Engine{Binary: "podman", Runner: f}
	if err := e.ValidateCandidate(context.Background(), Paths{}, ContainerRunning); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || len(f.calls[0]) < 2 || f.calls[0][1] != "exec" {
		t.Fatalf("expected validation in existing container, got %v", f.calls)
	}
}

func TestEnginePodmanRestartRequiresRelabelConsent(t *testing.T) {
	for _, tc := range []struct {
		name, binary, label, relabel string
		wantStart                    bool
	}{
		{"known unlabelled mounts", "podman", `"false"`, "", true},
		{"relabelled mounts without consent", "podman", `"true"`, "", false},
		{"unknown mounts without consent", "podman", `""`, "", false},
		{"relabelled mounts with consent", "podman", `"true"`, "1", true},
		{"unknown mounts with consent", "podman", `""`, "1", true},
		{"docker unaffected", "docker", `""`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WTREE_SELINUX_RELABEL", tc.relabel)
			f := &scriptedRunner{responses: map[string]string{
				"podman inspect": `{"Config":{"Labels":{"dev.wtree.selinux-relabel":` + tc.label + `}}}`,
			}}
			err := (Engine{Binary: tc.binary, Runner: f}).StartExisting(context.Background())
			if tc.wantStart && err != nil {
				t.Fatalf("restart failed: %v", err)
			}
			if !tc.wantStart && (err == nil || !strings.Contains(err.Error(), "WTREE_SELINUX_RELABEL=1")) {
				t.Fatalf("restart should require informed consent: %v", err)
			}
			started := false
			for _, call := range f.calls {
				if strings.HasPrefix(call, tc.binary+" start ") {
					started = true
				}
			}
			if started != tc.wantStart {
				t.Fatalf("container started = %t, want %t: %v", started, tc.wantStart, f.calls)
			}
		})
	}
}

func TestEnginePodmanRestartRejectsInvalidInspection(t *testing.T) {
	t.Setenv("WTREE_SELINUX_RELABEL", "")
	f := &scriptedRunner{responses: map[string]string{"podman inspect": "invalid JSON"}}
	if err := (Engine{Binary: "podman", Runner: f}).StartExisting(context.Background()); err == nil {
		t.Fatal("restart accepted invalid inspection")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "podman start ") {
			t.Fatalf("container started after invalid inspection: %v", f.calls)
		}
	}
}

func TestEnginePermissionDeniedHint(t *testing.T) {
	for _, tc := range []struct {
		name, binary, output string
		wantHint             bool
	}{
		{"podman denial", "/usr/bin/podman", "open /etc/wtree/Caddyfile: permission denied", true},
		{"podman unrelated failure", "podman", "invalid Caddyfile", false},
		{"docker denial", "docker", "permission denied", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{fail: "validate", output: tc.output}
			err := (Engine{Binary: tc.binary, Runner: f}).Validate(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.output) {
				t.Fatalf("original error missing: %v", err)
			}
			if got := strings.Contains(err.Error(), "WTREE_SELINUX_RELABEL=1"); got != tc.wantHint {
				t.Fatalf("hint present = %t, want %t: %v", got, tc.wantHint, err)
			}
		})
	}
}

func TestEngineStartFailureIncludesContext(t *testing.T) {
	f := &fakeRunner{fail: "run"}
	e := Engine{Binary: "podman", Runner: f}
	err := e.Start(context.Background(), Paths{Dir: "/tmp", Cert: "/tmp/cert", Key: "/tmp/key"})
	if err == nil || !strings.Contains(err.Error(), "engine failure") {
		t.Fatalf("missing startup error: %v", err)
	}
}

func TestEngineReloadFailureIncludesContext(t *testing.T) {
	f := &fakeRunner{fail: "reload"}
	err := (Engine{Binary: "podman", Runner: f}).Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "engine failure") {
		t.Fatalf("missing reload error: %v", err)
	}
}

type scriptedRunner struct {
	responses map[string]string
	failures  map[string]bool
	calls     []string
}

func (s *scriptedRunner) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	key := bin + " " + strings.Join(args, " ")
	s.calls = append(s.calls, key)
	for prefix, fail := range s.failures {
		if fail && strings.HasPrefix(key, prefix) {
			return []byte("unavailable"), errors.New("failed")
		}
	}
	for prefix, response := range s.responses {
		if strings.HasPrefix(key, prefix) {
			return []byte(response), nil
		}
	}
	return nil, nil
}

func TestEngineSelectionAndDiscovery(t *testing.T) {
	f := &scriptedRunner{failures: map[string]bool{"podman info": true}}
	e, err := selectEngine(context.Background(), "linux", "", func(name string) (string, error) { return name, nil }, f)
	if err != nil || e.Binary != "docker" {
		t.Fatalf("fallback: %+v %v", e, err)
	}
	_, err = selectEngine(context.Background(), "linux", "missing", func(string) (string, error) { return "", errors.New("not found") }, f)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("override error: %v", err)
	}
	f = &scriptedRunner{responses: map[string]string{"docker ps": "wtree-caddy\n", "docker inspect": `{"Config":{"Image":"docker.io/library/caddy:2.11.9","Labels":{"dev.wtree.managed":"true"}},"HostConfig":{"NetworkMode":"host"},"State":{"Running":true}}`}}
	state, err := (Engine{Binary: "docker", Runner: f}).State(context.Background())
	if err != nil || state != ContainerRunning {
		t.Fatalf("state: %v %v", state, err)
	}
	f.responses["docker inspect"] = `{"Config":{"Labels":{}},"HostConfig":{"NetworkMode":"bridge"},"State":{"Running":true}}`
	if _, err = (Engine{Binary: "docker", Runner: f}).State(context.Background()); err == nil {
		t.Fatal("accepted unmanaged container")
	}
}

func TestEngineCandidateValidationDoesNotStartListener(t *testing.T) {
	f := &scriptedRunner{}
	e := Engine{Binary: "docker", Runner: f}
	p := Paths{Dir: "/tmp/config", Cert: "/tmp/cert", Key: "/tmp/key"}
	if err := e.ValidateCandidate(context.Background(), p, ContainerAbsent); err != nil {
		t.Fatal(err)
	}
	call := fmt.Sprint(f.calls)
	for _, part := range []string{"--network=none", "--rm", "/tmp/config:/etc/wtree:ro", "--entrypoint caddy", "caddy:2.11.9 validate"} {
		if !strings.Contains(call, part) {
			t.Errorf("missing %q: %s", part, call)
		}
	}
}

func TestEngineRejectsContainerWithDifferentMounts(t *testing.T) {
	dir := t.TempDir()
	p := Paths{Dir: dir, Cert: dir + "/cert", Key: dir + "/key"}
	for _, path := range []string{p.Cert, p.Key} {
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	response := fmt.Sprintf(`{"Mounts":[{"Source":%q,"Destination":"/etc/wtree","RW":false},{"Source":%q,"Destination":"/tls/fullchain.pem","RW":false},{"Source":%q,"Destination":"/tls/privkey.pem","RW":false}]}`, p.Dir, p.Cert, p.Key)
	f := &scriptedRunner{responses: map[string]string{"docker inspect": response}}
	e := Engine{Binary: "docker", Runner: f}
	if err := e.CheckMounts(context.Background(), p, ContainerRunning); err != nil {
		t.Fatalf("matching mounts: %v", err)
	}
	p.Dir = t.TempDir()
	if err := e.CheckMounts(context.Background(), p, ContainerRunning); err == nil {
		t.Fatal("accepted different config directory")
	}
}

func TestEngineDoesNotRemoveContainerItDidNotCreate(t *testing.T) {
	f := &scriptedRunner{responses: map[string]string{"docker ps": "wtree-caddy\n", "docker inspect": `{"Config":{"Image":"docker.io/library/caddy:2.11.9","Labels":{"dev.wtree.managed":"true","dev.wtree.creation":"other"}},"HostConfig":{"NetworkMode":"host"},"State":{"Running":true}}`}}
	e := Engine{Binary: "docker", Runner: f}
	if err := e.Remove(context.Background()); err == nil {
		t.Fatal("removed a container owned by another invocation")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "docker rm ") {
			t.Fatalf("removed unrelated container: %v", f.calls)
		}
	}
}

func TestEngineCallsCarryDeadline(t *testing.T) {
	f := &fakeRunner{}
	var gotCtx context.Context
	f.capture = func(ctx context.Context, call string) { gotCtx = ctx }
	e := Engine{Binary: "podman", Runner: f}
	if _, err := e.State(context.Background()); err != nil {
		t.Fatalf("State: %v", err)
	}
	deadline, ok := gotCtx.Deadline()
	if !ok {
		t.Fatal("engine call context has no deadline")
	}
	if d := time.Until(deadline); d < 29*time.Second || d > 31*time.Second {
		t.Fatalf("default deadline not ~30s: %v", d)
	}
}

func TestEngineTimeoutOverride(t *testing.T) {
	t.Setenv("WTREE_TIMEOUT", "5")
	if got := EngineTimeout(); got != 5*time.Second {
		t.Fatalf("EngineTimeout() = %v, want 5s", got)
	}
}
