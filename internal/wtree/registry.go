package wtree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/gofrs/flock"
)

type Route struct {
	Slug      string `json:"slug"`
	Port      uint16 `json:"port"`
	Worktree  string `json:"worktree"`
}

type Registry struct {
	path string
	lock *flock.Flock
}
type registryFile struct {
	Version int     `json:"version"`
	Routes  []Route `json:"routes"`
}

func OpenRegistry(path string) (*Registry, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return &Registry{path: path, lock: flock.New(path + ".lock")}, nil
}

func (r *Registry) read() ([]Route, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Route{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("read route registry: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("unsupported route registry version %d", f.Version)
	}
	return f.Routes, nil
}

// Transaction holds the interprocess lock across reading, applying, and committing.
// The callback must not invoke another Registry method while holding the lock.
func (r *Registry) Transaction(fn func([]Route) ([]Route, error)) error {
	if err := r.lock.Lock(); err != nil {
		return err
	}
	defer r.lock.Unlock()
	routes, err := r.read()
	if err != nil {
		return err
	}
	routes, err = fn(routes)
	if err != nil {
		return err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Slug < routes[j].Slug })
	data, err := json.MarshalIndent(registryFile{Version: 1, Routes: routes}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".routes-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), r.path)
}

func (r *Registry) Register(route Route) error {
	return r.Transaction(func(routes []Route) ([]Route, error) {
		for _, existing := range routes {
			if existing.Slug == route.Slug {
				return nil, fmt.Errorf("slug %q is already active; use --slug <alternative>", route.Slug)
			}
		}
		return append(routes, route), nil
	})
}

func (r *Registry) Remove(slug string) (bool, error) {
	removed := false
	err := r.Transaction(func(routes []Route) ([]Route, error) {
		for i, route := range routes {
			if route.Slug == slug {
				removed = true
				return append(routes[:i], routes[i+1:]...), nil
			}
		}
		return routes, nil
	})
	return removed, err
}

func (r *Registry) List() ([]Route, error) {
	if err := r.lock.Lock(); err != nil {
		return nil, err
	}
	defer r.lock.Unlock()
	routes, err := r.read()
	if err != nil {
		return nil, err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Slug < routes[j].Slug })
	return routes, nil
}