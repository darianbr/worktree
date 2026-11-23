package wtree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const containerName = "wtree-caddy"
const caddyImage = "docker.io/library/caddy:2.11.9"

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).CombinedOutput()
}

type Engine struct {
	Binary     string
	Runner     CommandRunner
	creationID string
}

func SelectEngine(ctx context.Context) (Engine, error) {
	return selectEngine(ctx, runtime.GOOS, os.Getenv("WTREE_CONTAINER_ENGINE"), exec.LookPath, ExecRunner{})
}

func selectEngine(ctx context.Context, platform, override string, lookPath func(string) (string, error), runner CommandRunner) (Engine, error) {
	if platform == "darwin" {
		return Engine{}, errors.New("macOS container networking has not been validated for loopback-only isolation; wtree cannot safely start or modify routes on macOS yet")
	}
	// Discovery runs before an Engine exists, so bound it here: a stalled
	// container daemon must fail the probe, not hang the CLI (G1).
	ctx, cancel := bounded(ctx)
	defer cancel()
	candidates := []string{"podman", "docker"}
	if override != "" {
		candidates = []string{override}
	}
	var available []Engine
	var existing []Engine
	var failures []string
	for _, name := range candidates {
		bin, err := lookPath(name)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		e := Engine{Binary: bin, Runner: runner}
		if out, err := runner.Run(ctx, bin, "info"); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v: %s", name, err, strings.TrimSpace(string(out))))
			continue
		}
		available = append(available, e)
		state, err := e.State(ctx)
		if err != nil {
			return Engine{}, err
		}
		if state != ContainerAbsent {
			existing = append(existing, e)
		}
	}
	if len(existing) > 1 {
		return Engine{}, errors.New("wtree-caddy exists in multiple container engines; set WTREE_CONTAINER_ENGINE and resolve the duplicate")
	}
	if len(existing) == 1 {
		return existing[0], nil
	}
	if len(available) > 0 {
		return available[0], nil
	}
	return Engine{}, fmt.Errorf("no usable container engine; install and start Docker or Podman, or set WTREE_CONTAINER_ENGINE: %s", strings.Join(failures, "; "))
}

func (e Engine) State(ctx context.Context) (ContainerState, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := e.Runner.Run(ctx, e.Binary, "ps", "-a", "--filter", "name=^/"+containerName+"$", "--format", "{{.Names}}")
	if err != nil {
		return ContainerAbsent, fmt.Errorf("discover %s: %w: %s", containerName, err, strings.TrimSpace(string(out)))
	}
	found := false
	for _, name := range strings.Fields(string(out)) {
		if name == containerName {
			found = true
		}
	}
	if !found {
		return ContainerAbsent, nil
	}
	out, err = e.Runner.Run(ctx, e.Binary, "inspect", "--format", "{{json .}}", containerName)
	if err != nil {
		return ContainerAbsent, fmt.Errorf("inspect %s: %w: %s", containerName, err, strings.TrimSpace(string(out)))
	}
	var info struct {
		Config struct {
			Image  string
			Labels map[string]string
		}
		HostConfig struct{ NetworkMode string }
		State      struct{ Running bool }
	}
	if err = json.Unmarshal(out, &info); err != nil {
		return ContainerAbsent, fmt.Errorf("inspect %s: invalid response: %w", containerName, err)
	}
	if info.Config.Labels["dev.wtree.managed"] != "true" || info.HostConfig.NetworkMode != "host" || info.Config.Image != caddyImage {
		return ContainerAbsent, fmt.Errorf("container %s is not a compatible wtree-managed host-network Caddy container; inspect it before removing it", containerName)
	}
	if info.State.Running {
		return ContainerRunning, nil
	}
	return ContainerStopped, nil
}

func (e Engine) CheckMounts(ctx context.Context, p Paths, state ContainerState) error {
	if state == ContainerAbsent {
		return nil
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := e.Runner.Run(ctx, e.Binary, "inspect", "--format", "{{json .}}", containerName)
	if err != nil {
		return fmt.Errorf("inspect container mounts: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var info struct {
		Mounts []struct {
			Source, Destination string
			RW                  bool
		}
	}
	if err = json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("inspect container mounts: %w", err)
	}
	for destination, source := range map[string]string{"/etc/wtree": p.Dir, "/tls/fullchain.pem": p.Cert, "/tls/privkey.pem": p.Key} {
		matched := false
		for _, mount := range info.Mounts {
			if mount.Destination != destination || mount.RW {
				continue
			}
			a, errA := os.Stat(source)
			b, errB := os.Stat(mount.Source)
			if errA == nil && errB == nil && os.SameFile(a, b) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("existing %s container has different or writable %s mount; use the original WTREE_CONFIG_DIR and TLS paths or remove the container after stopping its routes", containerName, destination)
		}
	}
	return nil
}

func (e Engine) run(ctx context.Context, args ...string) error {
	out, err := e.Runner.Run(ctx, e.Binary, args...)
	if err != nil {
		message := fmt.Errorf("%s %s: %w: %s", e.Binary, args[0], err, strings.TrimSpace(string(out)))
		if e.isPodman() && strings.Contains(strings.ToLower(message.Error()), "permission denied") {
			return fmt.Errorf("%w; check file permissions and SELinux labels; if appropriate, explicitly set WTREE_SELINUX_RELABEL=1 to let Podman relabel bind sources (see README)", message)
		}
		return message
	}
	return nil
}

func (e Engine) isPodman() bool {
	return filepath.Base(e.Binary) == "podman"
}

func (e Engine) bindMode() string {
	if e.isPodman() && os.Getenv("WTREE_SELINUX_RELABEL") == "1" {
		return "ro,z"
	}
	return "ro"
}

func (e *Engine) Start(ctx context.Context, p Paths) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fmt.Errorf("generate container ownership token: %w", err)
	}
	e.creationID = hex.EncodeToString(token[:])
	mode := e.bindMode()
	args := []string{"run", "--detach", "--name", containerName, "--network=host",
		"--label", "dev.wtree.managed=true", "--label", "dev.wtree.creation=" + e.creationID}
	if e.isPodman() {
		args = append(args, "--label", "dev.wtree.selinux-relabel="+fmt.Sprint(mode == "ro,z"))
	}
	args = append(args, "--volume", p.Dir+":/etc/wtree:"+mode,
		"--volume", p.Cert+":/tls/fullchain.pem:"+mode, "--volume", p.Key+":/tls/privkey.pem:"+mode,
		"--volume", "wtree-caddy-data:/data", caddyImage,
		"caddy", "run", "--config", "/etc/wtree/Caddyfile", "--adapter", "caddyfile")
	return e.run(ctx, args...)
}

func (e Engine) Validate(ctx context.Context) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return e.run(ctx, "exec", containerName, "caddy", "validate", "--config", "/etc/wtree/Caddyfile", "--adapter", "caddyfile")
}

func (e Engine) Reload(ctx context.Context) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return e.run(ctx, "exec", containerName, "caddy", "reload", "--config", "/etc/wtree/Caddyfile", "--adapter", "caddyfile")
}

func (e Engine) ValidateCandidate(ctx context.Context, p Paths, state ContainerState) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if state == ContainerRunning {
		return e.Validate(ctx)
	}
	mode := e.bindMode()
	return e.run(ctx, "run", "--rm", "--network=none", "--entrypoint", "caddy",
		"--volume", p.Dir+":/etc/wtree:"+mode, "--volume", p.Cert+":/tls/fullchain.pem:"+mode, "--volume", p.Key+":/tls/privkey.pem:"+mode,
		caddyImage, "validate", "--config", "/etc/wtree/Caddyfile", "--adapter", "caddyfile")
}

func (e Engine) StartExisting(ctx context.Context) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if e.isPodman() && os.Getenv("WTREE_SELINUX_RELABEL") != "1" {
		out, err := e.Runner.Run(ctx, e.Binary, "inspect", "--format", "{{json .}}", containerName)
		if err != nil {
			return fmt.Errorf("inspect container before restart: %w: %s", err, strings.TrimSpace(string(out)))
		}
		var info struct {
			Config struct{ Labels map[string]string }
		}
		if err := json.Unmarshal(out, &info); err != nil {
			return fmt.Errorf("inspect container before restart: %w", err)
		}
		if info.Config.Labels["dev.wtree.selinux-relabel"] != "false" {
			return errors.New("Podman container may relabel bind sources on restart; review the SELinux risks in README and set WTREE_SELINUX_RELABEL=1 to consent, or replace the container after stopping its routes")
		}
	}
	return e.run(ctx, "start", containerName)
}
func (e Engine) Stop(ctx context.Context) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	state, err := e.State(ctx)
	if err != nil {
		return err
	}
	if state != ContainerRunning {
		return nil
	}
	return e.run(ctx, "stop", containerName)
}
func (e Engine) Remove(ctx context.Context) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	state, err := e.State(ctx)
	if err != nil {
		return err
	}
	if state == ContainerAbsent {
		return nil
	}
	out, err := e.Runner.Run(ctx, e.Binary, "inspect", "--format", "{{json .}}", containerName)
	if err != nil {
		return fmt.Errorf("inspect container before removal: %w", err)
	}
	var info struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	if err = json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("inspect container before removal: %w", err)
	}
	if e.creationID == "" || info.ID == "" || info.Config.Labels["dev.wtree.creation"] != e.creationID {
		return errors.New("refusing to remove a container created by another invocation")
	}
	return e.run(ctx, "rm", "-f", info.ID)
}

// EngineTimeout bounds every container-engine call. WTREE_TIMEOUT overrides it
// in seconds; invalid or missing values fall back to the 30s default.
func EngineTimeout() time.Duration {
	if v := os.Getenv("WTREE_TIMEOUT"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 30 * time.Second
}

// bounded derives a working context whose deadline is no later than the
// caller's existing deadline and EngineTimeout.
func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, hasDeadline := ctx.Deadline()
	max := time.Now().Add(EngineTimeout())
	if hasDeadline && deadline.Before(max) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, max)
}
