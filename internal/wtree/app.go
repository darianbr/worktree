package wtree

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Git interface {
	Worktree(context.Context) (root, branch string, err error)
}

type ContainerState int

const (
	ContainerAbsent ContainerState = iota
	ContainerStopped
	ContainerRunning
)

type LifecycleEngine interface {
	State(context.Context) (ContainerState, error)
	CheckMounts(context.Context, Paths, ContainerState) error
	Start(context.Context, Paths) error
	StartExisting(context.Context) error
	ValidateCandidate(context.Context, Paths, ContainerState) error
	Reload(context.Context) error
	Stop(context.Context) error
	Remove(context.Context) error
}

type Dependencies struct {
	Paths    Paths
	Registry *Registry
	Git      Git
	Engine   func(context.Context) (LifecycleEngine, error)
	Stderr   io.Writer
}

func validateCertificate(p Paths) error {
	pair, err := tls.LoadX509KeyPair(p.Cert, p.Key)
	if err != nil {
		return fmt.Errorf("load TLS certificate and matching key (provision WTREE_CERT_FILE and WTREE_KEY_FILE): %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse TLS certificate: %w", err)
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("TLS certificate is not currently valid (valid %s to %s)", cert.NotBefore, cert.NotAfter)
	}
	wildcard := false
	for _, name := range cert.DNSNames {
		if strings.EqualFold(name, "*.wtree.dev") {
			wildcard = true
			break
		}
	}
	if !wildcard {
		return errors.New("TLS certificate must contain the *.wtree.dev wildcard DNS SAN")
	}
	if err := cert.VerifyHostname("example.wtree.dev"); err != nil {
		return fmt.Errorf("TLS certificate does not cover *.wtree.dev: %w", err)
	}
	return nil
}

func atomicConfig(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".Caddyfile-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// activate installs the candidate while the registry lock is held. Its rollback
// restores both the file and live container, also if the subsequent commit fails.
func activate(ctx context.Context, d Dependencies, routes []Route) (func() error, error) {
	config, err := RenderCaddyfile(routes)
	if err != nil {
		return nil, err
	}
	if d.Engine == nil {
		return nil, errors.New("container engine is not configured")
	}
	engine, err := d.Engine(ctx)
	if err != nil {
		return nil, err
	}
	state, err := engine.State(ctx)
	if err != nil {
		return nil, err
	}
	if err := engine.CheckMounts(ctx, d.Paths, state); err != nil {
		return nil, err
	}
	old, err := os.ReadFile(d.Paths.Config)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read previous Caddyfile: %w", err)
	}
	if err := atomicConfig(d.Paths.Config, config); err != nil {
		return nil, fmt.Errorf("write candidate Caddyfile: %w", err)
	}
	rollback := func() error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var errs []error
		if existed {
			if err := atomicConfig(d.Paths.Config, old); err != nil {
				errs = append(errs, fmt.Errorf("restore Caddyfile: %w", err))
			}
		} else if err := os.Remove(d.Paths.Config); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove candidate Caddyfile: %w", err))
		}
		switch state {
		case ContainerAbsent:
			if err := engine.Remove(cleanup); err != nil {
				errs = append(errs, fmt.Errorf("remove new container: %w", err))
			}
		case ContainerStopped:
			if err := engine.Stop(cleanup); err != nil {
				errs = append(errs, fmt.Errorf("stop restarted container: %w", err))
			}
		case ContainerRunning:
			if existed && len(errs) == 0 {
				if err := engine.Reload(cleanup); err != nil {
					errs = append(errs, fmt.Errorf("reload previous Caddyfile: %w", err))
				}
			} else if !existed {
				errs = append(errs, errors.New("cannot restore running Caddy: previous Caddyfile was absent"))
			}
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (func() error, error) {
		if undo := rollback(); undo != nil {
			err = errors.Join(err, fmt.Errorf("compensation failed: %w", undo))
		}
		return nil, err
	}
	if err = engine.ValidateCandidate(ctx, d.Paths, state); err != nil {
		return fail(fmt.Errorf("validate candidate Caddyfile: %w", err))
	}
	switch state {
	case ContainerAbsent:
		err = engine.Start(ctx, d.Paths)
	case ContainerStopped:
		err = engine.StartExisting(ctx)
	case ContainerRunning:
	default:
		return fail(errors.New("unknown container state"))
	}
	if err != nil {
		return fail(fmt.Errorf("start Caddy (check port 443 and host networking): %w", err))
	}
	if state != ContainerRunning {
		started, stateErr := engine.State(ctx)
		if stateErr != nil {
			return fail(fmt.Errorf("check Caddy startup: %w", stateErr))
		}
		if started != ContainerRunning {
			return fail(errors.New("Caddy exited during startup; check port 443, container logs, and host networking"))
		}
	}
	if state == ContainerRunning {
		if err = engine.Reload(ctx); err != nil {
			return fail(fmt.Errorf("reload Caddy: %w", err))
		}
	}
	return rollback, nil
}

func addRoute(ctx context.Context, d Dependencies, route Route) error {
	return d.Registry.transaction(ctx, func(routes []Route) ([]Route, func() error, error) {
		for _, r := range routes {
			if r.Slug == route.Slug {
				return nil, nil, fmt.Errorf("slug %q is already active; use --slug <value>", route.Slug)
			}
		}
		if _, err := ProvisionTLS(d.Paths, d.Stderr); err != nil {
			return nil, nil, fmt.Errorf("provision TLS certificate: %w", err)
		}
		if err := validateCertificate(d.Paths); err != nil {
			return nil, nil, err
		}
		next := append(append([]Route(nil), routes...), route)
		undo, err := activate(ctx, d, next)
		return next, undo, err
	})
}

func removeRoute(ctx context.Context, d Dependencies, slug, root string) (bool, error) {
	removed := false
	err := d.Registry.transaction(ctx, func(routes []Route) ([]Route, func() error, error) {
		index := -1
		for i, r := range routes {
			if (slug != "" && r.Slug == slug) || (slug == "" && r.Worktree == root) {
				if index != -1 {
					return nil, nil, errors.New("multiple routes belong to this worktree; select one with --slug <value>")
				}
				index = i
			}
		}
		if index == -1 {
			return nil, nil, errNoChange
		}
		next := append(append([]Route(nil), routes[:index]...), routes[index+1:]...)
		undo, err := activate(ctx, d, next)
		if err == nil {
			removed = true
		}
		return next, undo, err
	})
	return removed && err == nil, err
}
