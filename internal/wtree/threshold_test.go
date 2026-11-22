package wtree

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func writeTestMarker(t *testing.T, p Paths, original int) {
	t.Helper()
	data, _ := json.Marshal(thresholdMarker{Original: original, SetAt: "2026-01-01T00:00:00Z"})
	if err := writeFileAtomic(markerPath(p), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestMarker(t *testing.T, p Paths) (thresholdMarker, bool) {
	t.Helper()
	data, err := os.ReadFile(markerPath(p))
	if err != nil {
		return thresholdMarker{}, false
	}
	var m thresholdMarker
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m, true
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

func TestEnsureLowerAndRecord(t *testing.T) {
	p := testPaths(t)
	var calls [][]string
	sudo := func(args ...string) error { calls = append(calls, args); return nil }
	if err := EnsureThreshold443(p, func() (int, error) { return 1024, nil }, sudo); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "ip_unprivileged_port_start=443") {
		t.Fatalf("sudo calls = %v", calls)
	}
	m, ok := readTestMarker(t, p)
	if !ok || m.Original != 1024 {
		t.Fatalf("marker = %+v ok=%v", m, ok)
	}
}

func TestEnsureNoOpWhenAlreadyLow(t *testing.T) {
	p := testPaths(t)
	var calls [][]string
	sudo := func(args ...string) error { calls = append(calls, args); return nil }
	if err := EnsureThreshold443(p, func() (int, error) { return 443, nil }, sudo); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("unexpected sudo calls: %v", calls)
	}
	if _, ok := readTestMarker(t, p); ok {
		t.Fatal("wrote a marker for a no-op")
	}
}

func TestEnsureRebootLowersOnceAndReconverges(t *testing.T) {
	// Reboot: the kernel reverted the threshold to its original (1024) and a
	// stale marker from the previous session is still on disk. The next up must
	// lower once (sudo) and re-record the marker so the proxy can bind 443.
	p := testPaths(t)
	writeTestMarker(t, p, 1024)
	var calls [][]string
	sudo := func(args ...string) error { calls = append(calls, args); return nil }
	if err := EnsureThreshold443(p, func() (int, error) { return 1024, nil }, sudo); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "ip_unprivileged_port_start=443") {
		t.Fatalf("expected one lowering after reboot, got %v", calls)
	}
	m, ok := readTestMarker(t, p)
	if !ok || m.Original != 1024 {
		t.Fatalf("marker not re-recorded: %+v ok=%v", m, ok)
	}
}

func TestRestoreOnlyOwnsWhatItLowered(t *testing.T) {
	p := testPaths(t)
	var calls [][]string
	sudo := func(args ...string) error { calls = append(calls, args); return nil }
	// No marker (user-owned threshold at 443): restore must not touch it.
	if err := RestoreThreshold(p, func() (int, error) { return 443, nil }, sudo); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("restore touched a user-owned threshold: %v", calls)
	}
}

func TestRestorePrintsManualCommandOnFailure(t *testing.T) {
	p := testPaths(t)
	writeTestMarker(t, p, 1024)
	sudo := func(args ...string) error { return errSudo }
	out := captureStderr(t, func() {
		err := RestoreThreshold(p, func() (int, error) { return 443, nil }, sudo)
		if err == nil {
			t.Fatal("expected an error when sudo fails")
		}
	})
	if !strings.Contains(out, "sudo sysctl -w net.ipv4.ip_unprivileged_port_start=1024") {
		t.Fatalf("missing manual command: %q", out)
	}
	if _, ok := readTestMarker(t, p); !ok {
		t.Fatal("marker removed despite failed restore")
	}
}

func TestRestoreDeletesMarkerWhenAlreadyAtOriginal(t *testing.T) {
	p := testPaths(t)
	writeTestMarker(t, p, 1024)
	var calls [][]string
	sudo := func(args ...string) error { calls = append(calls, args); return nil }
	// Reboot already reverted the threshold to the original; just drop the marker.
	if err := RestoreThreshold(p, func() (int, error) { return 1024, nil }, sudo); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("unexpected sudo calls: %v", calls)
	}
	if _, ok := readTestMarker(t, p); ok {
		t.Fatal("marker not removed")
	}
}

var errSudo = os.ErrPermission

func TestCurrentThresholdSane(t *testing.T) {
	// On this host (Linux) assert it returns without error and a sane value;
	// the non-Linux (0, nil) contract is covered by the code path.
	v, err := CurrentThreshold()
	if err != nil {
		t.Fatalf("CurrentThreshold: %v", err)
	}
	if v < 1 || v > 65535 {
		t.Fatalf("implausible threshold %d", v)
	}
}
