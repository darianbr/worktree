package wtree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ProxyState reports whether the Caddy proxy is serving after reconciliation.
type ProxyState int

const (
	ProxyDown ProxyState = iota
	ProxyServing
)

func stateLabel(s ProxyState) string {
	if s == ProxyServing {
		return "serving"
	}
	return "stale (proxy down)"
}

// StaleHint maps a reconcile failure to the single most likely fix, so status
// can point at the remedy instead of dumping raw engine errors.
func StaleHint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "bind") || strings.Contains(msg, "address already in use"):
		return "run: sudo sysctl -w net.ipv4.ip_unprivileged_port_start=443"
	case strings.Contains(msg, "pull") || strings.Contains(msg, "manifest"):
		return "check network access to docker.io/library/caddy:2.11.9"
	case strings.Contains(msg, "permission denied"):
		return "SELinux may block the bind mounts; relabel with chcon -Rt svirt_sandbox_file_t ~/.config/wtree"
	default:
		return "inspect: podman logs wtree-caddy"
	}
}

// Reconcile converges the container and on-disk Caddyfile to what the registry
// says should be true. It runs at the start of every command so that any
// failure mode (crash, kill, reboot, stale config) heals on the next action.
// It does not take the registry lock around engine work: List takes its own
// brief lock, and Registry calls must not nest.
func Reconcile(ctx context.Context, d Dependencies) (ProxyState, error) {
	routes, err := d.Registry.List(ctx)
	if err != nil {
		return ProxyDown, err
	}
	if len(routes) == 0 {
		// Nothing to serve. Without an engine configured there is nothing to
		// reconcile (status works engine-less); with one, stop a stray proxy so
		// port 443 is only bound while routes exist.
		if d.Engine == nil {
			return ProxyDown, nil
		}
		engine, err := d.Engine(ctx)
		if err != nil {
			return ProxyDown, err
		}
		state, err := engine.State(ctx)
		if err != nil {
			return ProxyDown, err
		}
		if state == ContainerRunning {
			if err := engine.Stop(ctx); err != nil {
				return ProxyDown, fmt.Errorf("stop Caddy: %w", err)
			}
		}
		return ProxyDown, nil
	}
	if d.Engine == nil {
		return ProxyDown, errors.New("container engine is not configured")
	}
	engine, err := d.Engine(ctx)
	if err != nil {
		return ProxyDown, err
	}
	state, err := engine.State(ctx)
	if err != nil {
		return ProxyDown, err
	}
	if err := engine.CheckMounts(ctx, d.Paths, state); err != nil {
		return ProxyDown, err
	}
	config, err := RenderCaddyfile(routes)
	if err != nil {
		return ProxyDown, err
	}
	current, err := os.ReadFile(d.Paths.Config)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ProxyDown, fmt.Errorf("read Caddyfile: %w", err)
	}
	stale := !bytes.Equal(current, config)
	switch state {
	case ContainerRunning:
		if stale {
			if err := atomicConfig(d.Paths.Config, config); err != nil {
				return ProxyDown, fmt.Errorf("write Caddyfile: %w", err)
			}
			if err := engine.Reload(ctx); err != nil {
				return ProxyDown, fmt.Errorf("reload Caddy: %w", err)
			}
		}
	case ContainerStopped:
		if stale {
			if err := atomicConfig(d.Paths.Config, config); err != nil {
				return ProxyDown, fmt.Errorf("write Caddyfile: %w", err)
			}
		}
		if err := engine.StartExisting(ctx); err != nil {
			return ProxyDown, fmt.Errorf("start Caddy (check port 443 and host networking): %w", err)
		}
	default: // ContainerAbsent
		if stale {
			if err := atomicConfig(d.Paths.Config, config); err != nil {
				return ProxyDown, fmt.Errorf("write Caddyfile: %w", err)
			}
		}
		if err := engine.Start(ctx, d.Paths); err != nil {
			return ProxyDown, fmt.Errorf("start Caddy (check port 443 and host networking): %w", err)
		}
	}
	return ProxyServing, nil
}
