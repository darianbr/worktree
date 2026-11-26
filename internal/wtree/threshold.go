package wtree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// SudoRunner runs a privileged command. The production implementation shells out
// to sudo with the terminal wired through so the password prompt reaches the
// user directly; tests inject a recorder.
type SudoRunner func(args ...string) error

// prodSudo runs sudo with its I/O streamed straight to the user's terminal so
// the password prompt is interactive and never captured by wtree.
func prodSudo(args ...string) error {
	cmd := exec.Command("sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

const thresholdMarkerFile = "port-threshold"

type thresholdMarker struct {
	Original int    `json:"original"`
	SetAt    string `json:"set_at"`
}

func markerPath(p Paths) string { return filepath.Join(p.Dir, thresholdMarkerFile) }

// CurrentThreshold reads the kernel's unprivileged-port start. On non-Linux it
// returns (0, nil), meaning there is nothing to manage.
func CurrentThreshold() (int, error) {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_unprivileged_port_start")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read ip_unprivileged_port_start: %w", err)
	}
	var v int
	if _, err := fmt.Sscanf(string(data), "%d", &v); err != nil {
		return 0, fmt.Errorf("parse ip_unprivileged_port_start: %w", err)
	}
	return v, nil
}

func readMarker(p Paths) (thresholdMarker, bool) {
	data, err := os.ReadFile(markerPath(p))
	if err != nil {
		return thresholdMarker{}, false
	}
	var m thresholdMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return thresholdMarker{}, false
	}
	return m, true
}

func writeMarker(p Paths, original int) error {
	data, err := json.Marshal(thresholdMarker{Original: original, SetAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return writeFileAtomic(markerPath(p), data, 0600)
}

// EnsureThreshold443 makes sure the unprivileged-port threshold allows binding
// 443 as an unprivileged user. It is a no-op when the threshold already permits
// it. Otherwise it lowers the threshold once (via sudo) and records the original
// value in a marker so RestoreThreshold can put it back — and only then.
func EnsureThreshold443(p Paths, current func() (int, error), sudo SudoRunner) error {
	cur, err := current()
	if err != nil {
		return err
	}
	if cur <= 443 {
		// Already permissive. A marker left by a previous session is stale once
		// the kernel has reverted to its original value; drop it so restore does
		// not act on a ghost.
		if m, ok := readMarker(p); ok && m.Original == cur {
			os.Remove(markerPath(p))
		}
		return nil
	}
	fmt.Fprintln(os.Stderr, "port 443 requires lowering the unprivileged-port threshold; sudo password required")
	if err := sudo("sysctl", "-w", "net.ipv4.ip_unprivileged_port_start=443"); err != nil {
		return fmt.Errorf("lower port threshold: %w", err)
	}
	return writeMarker(p, cur)
}

// RestoreThreshold returns the unprivileged-port threshold to the value wtree
// recorded when it lowered it. It only acts on a threshold wtree itself changed
// (a marker is present); a user-owned threshold is never touched.
func RestoreThreshold(p Paths, current func() (int, error), sudo SudoRunner) error {
	m, ok := readMarker(p)
	if !ok {
		return nil
	}
	cur, err := current()
	if err != nil {
		return err
	}
	if cur == m.Original {
		// Already back at the original (e.g. a reboot reverted it); just drop the marker.
		return os.Remove(markerPath(p))
	}
	if err := sudo("sysctl", "-w", fmt.Sprintf("net.ipv4.ip_unprivileged_port_start=%d", m.Original)); err != nil {
		fmt.Fprintf(os.Stderr, "could not restore the port threshold; run: sudo sysctl -w net.ipv4.ip_unprivileged_port_start=%d\n", m.Original)
		return fmt.Errorf("restore port threshold: %w", err)
	}
	return os.Remove(markerPath(p))
}
