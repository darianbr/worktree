package wtree

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistryPersistenceAndRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "routes.json")
	r, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if routes, err := r.List(context.Background()); err != nil || len(routes) != 0 {
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
	routes, err := r.List(context.Background())
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

func TestRegistrySameInstanceSerializesTransactions(t *testing.T) {
	r, err := OpenRegistry(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 2)
	go func() {
		done <- r.Transaction(context.Background(), func(routes []Route) ([]Route, error) {
			close(entered)
			<-release
			return append(routes, Route{Slug: "first", Port: 3000}), nil
		})
	}()
	<-entered
	second := make(chan struct{})
	go func() {
		done <- r.Transaction(context.Background(), func(routes []Route) ([]Route, error) {
			close(second)
			return append(routes, Route{Slug: "second", Port: 3001}), nil
		})
	}()
	// The lock is acquired by polling (50ms interval), so allow one poll
	// cycle plus margin before concluding mutual exclusion held.
	select {
	case <-second:
		close(release)
		<-done
		<-done
		t.Fatal("second callback entered while first held the lock")
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	routes, err := r.List(context.Background())
	if err != nil || len(routes) != 2 {
		t.Fatalf("lost update: %v %v", routes, err)
	}
}

// TestRegistryLockTimesOut proves a stuck lock holder fails the waiter fast
// instead of blocking it. The holder goroutine is joined in t.Cleanup so the
// TempDir removal never races the lockfile.
func TestRegistryLockTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	a, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	releaseCh := make(chan struct{})
	releaseOnce := sync.Once{}
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Transaction(context.Background(), func(routes []Route) ([]Route, error) {
			close(acquired)
			<-releaseCh
			return routes, nil
		})
	}()
	<-acquired
	t.Cleanup(func() {
		release()
		<-done
	})

	t.Setenv("WTREE_LOCK_TIMEOUT", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err = b.Transaction(ctx, func(routes []Route) ([]Route, error) { return routes, nil })
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected lock timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "appears stuck") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("lock acquisition did not fail fast: %v", elapsed)
	}
}
