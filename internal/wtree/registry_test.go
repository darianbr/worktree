package wtree

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestRegistryPersistenceAndRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "routes.json")
	r, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if routes, err := r.List(); err != nil || len(routes) != 0 {
		t.Fatalf("empty registry: %v %v", routes, err)
	}
	for _, route := range []Route{{Slug: "zebra", Port: 3001, Worktree: "/one"}, {Slug: "apple", Port: 3002, Worktree: "/two"}} {
		if err := r.Register(route); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(Route{Slug: "apple", Port: 1234}); err == nil {
		t.Fatal("duplicate accepted")
	}
	r, err = OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := r.List()
	if err != nil || len(routes) != 2 || routes[0].Slug != "apple" || routes[0].Port != 3002 {
		t.Fatalf("persisted routes: %v %v", routes, err)
	}
	removed, err := r.Remove("apple")
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	removed, err = r.Remove("apple")
	if err != nil || removed {
		t.Fatalf("repeat remove: %v %v", removed, err)
	}
}

func TestRegistryConcurrentDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := OpenRegistry(path)
			if err == nil {
				err = r.Register(Route{Slug: "same", Port: 3001})
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var ok int
	for err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d registrations succeeded, want exactly one", ok)
	}
}