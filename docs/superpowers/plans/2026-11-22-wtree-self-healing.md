# wtree Self-Healing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make wtree converge to correct state after any failure (Caddy crash, process kill, reboot), with bounded operations, truthful status, auto-provisioned TLS, and per-session port-443 handling.

**Architecture:** Keep the existing split — host-side binary owns records (`~/.config/wtree/`), disposable Caddy container serves them. Add a `Reconcile` step (pure function of registry + container state + on-disk Caddyfile) that runs at the start of every command; bound every engine call and lock acquisition with deadlines; generate local CA + wildcard cert when none is valid; lower/restore the unprivileged-port threshold per session with a marker file.

**Tech Stack:** Go 1.26, `gofrs/flock v0.13.0` (`TryLockContext`), stdlib `crypto/x509` for cert generation, podman/docker via existing `CommandRunner`.

**Spec:** `docs/superpowers/specs/2026-11-22-wtree-self-healing-design.md`

## Global Constraints

- CLI contract unchanged: `up --port <1..65535> [--slug]`, `down [--slug]`, `status`, `version`.
- Records stay on the host in the config dir; the container remains disposable. Never move state into the container.
- Pinned image `docker.io/library/caddy:2.11.9`; rootless podman preferred; `--network=host`.
- macOS stays fail-closed (`selectEngine` darwin error unchanged).
- No new dependencies beyond what `go.mod` already has (flock v0.13.0 is present).
- Error messages stay in wtree's existing terse voice: `wtree: <what failed> (<one-line hint>)`.
- Every container-engine call and lock acquisition must have a deadline; nothing may block indefinitely.
- No persistent system configuration is written (no `/etc/sysctl.d` files, no setcap).

## Review Focus

Failure modes the spec implies but no unit test exercises end-to-end; each line's test is pinned into the owning task:

1. **Host reboot with active routes** — threshold reverts to 1024, stale marker present; next `up` must prompt sudo once and converge (Task 6, marker-staleness test).
2. **Caddy killed mid-session** (`podman kill wtree-caddy`) — next `status` must self-heal to serving with zero user action (Task 8, integration test).
3. **wtree process SIGKILLed while holding the registry lock** — flock releases on process death; next command must not hang and must converge (Task 2, held-lock timeout test + Task 8 integration).
4. **User-owned threshold** (user set 443 themselves, no marker) — wtree must never restore/overwrite it (Task 6, no-marker test).
5. **CA deleted but leaf still valid** — provisioning must not regenerate the CA or re-show the trust hint (Task 5, idempotency test).

---

### Task 1: Bounded engine operations

**Files:**
- Modify: `internal/wtree/engine.go` (all public `Engine` methods)
- Test: `internal/wtree/engine_test.go`

**Interfaces:**
- Consumes: existing `CommandRunner`, `Engine`.
- Produces: `func EngineTimeout() time.Duration` — reads `WTREE_TIMEOUT` (seconds, integer); default 30s; invalid/missing → 30s. Every public Engine method (`State`, `CheckMounts`, `Start`, `StartExisting`, `Validate`, `ValidateCandidate`, `Reload`, `Stop`, `Remove`) internally derives its working context from the passed ctx via a deadline no longer than `EngineTimeout()`.

- [ ] **Step 1: Write the failing test** in `engine_test.go`

```go
func TestEngineCallsCarryDeadline(t *testing.T) {
	f := &scriptedRunner{responses: map[string]string{"podman ps": ""}}
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
```

Extend `scriptedRunner` with an optional `capture func(ctx context.Context, call string)` field invoked at the start of `Run` (no behavior change when nil).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wtree/ -run 'TestEngineCallsCarryDeadline|TestEngineTimeoutOverride' -v`
Expected: FAIL — "engine call context has no deadline" / undefined `EngineTimeout`.

- [ ] **Step 3: Implement** in `engine.go`

Add `EngineTimeout()` (parse `WTREE_TIMEOUT` as integer seconds; default 30). Add a small helper `bounded(ctx context.Context) (context.Context, context.CancelFunc)` that returns `context.WithTimeout(ctx, EngineTimeout())` but never extends an already-sooner deadline from the caller. Wrap each public method body: derive bounded ctx at entry, `defer cancel()`, use it for all `Runner.Run` calls. `ExecRunner` already uses `exec.CommandContext`, so cancellation propagates to the podman process.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/wtree/ -v`
Expected: PASS (all existing engine tests still pass — they use `context.Background()` and complete instantly).

- [ ] **Step 5: Commit**

```bash
git add internal/wtree/engine.go internal/wtree/engine_test.go
git commit -m "Bound all container-engine calls with a 30s deadline (WTREE_TIMEOUT)"
```

---

### Task 2: Bounded registry lock acquisition

**Files:**
- Modify: `internal/wtree/registry.go`
- Modify: `internal/wtree/cli.go` (call sites of `Transaction`/`List`)
- Test: `internal/wtree/registry_test.go`

**Interfaces:**
- Consumes: `gofrs/flock v0.13.0` `TryLockContext(ctx, retryDelay)`.
- Produces:
  - `func (r *Registry) Transaction(ctx context.Context, fn func([]Route) ([]Route, error)) error`
  - `func (r *Registry) List(ctx context.Context) ([]Route, error)`
  - `func LockTimeout() time.Duration` — reads `WTREE_LOCK_TIMEOUT` (seconds); default 5s.
  - On deadline expiry the error is: `registry lock held by another wtree process and appears stuck; it will time out on its own — retry shortly`.

- [ ] **Step 1: Write the failing test** in `registry_test.go`

```go
func TestRegistryLockTimesOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	a, err := OpenRegistry(path)
	if err != nil { t.Fatal(err) }
	b, err := OpenRegistry(path)
	if err != nil { t.Fatal(err) }

	release := make(chan struct{})
	go func() {
		_ = a.Transaction(context.Background(), func(routes []Route) ([]Route, error) {
			<-release
			return routes, nil
		})
	}()
	time.Sleep(100 * time.Millisecond) // let goroutine a acquire

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
	close(release)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wtree/ -run TestRegistryLockTimesOut -v`
Expected: FAIL — compile error (Transaction signature mismatch).

- [ ] **Step 3: Implement** in `registry.go`

Thread `ctx` through `transaction()` and `List()`. Replace `r.lock.Lock()` with: build a deadline ctx (`context.WithTimeout(ctx, LockTimeout())`), call `TryLockContext(deadlineCtx, 50*time.Millisecond)`; if it returns `(false, nil)` (deadline hit) return the "appears stuck" error; if `(true, nil)` proceed as today with `defer r.lock.Unlock()`. Update all call sites in `cli.go` (`deps.Registry.List(ctx)`, `addRoute`/`removeRoute` pass their ctx into `Transaction`).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/wtree/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wtree/registry.go internal/wtree/registry_test.go internal/wtree/cli.go
git commit -m "Fail fast (5s, WTREE_LOCK_TIMEOUT) instead of blocking on the registry lock"
```

---

### Task 3: Reconcile — converge state on every command

**Files:**
- Create: `internal/wtree/reconcile.go`
- Test: `internal/wtree/reconcile_test.go`

**Interfaces:**
- Consumes: `Dependencies`, `LifecycleEngine`, `Registry.List(ctx)`, `RenderCaddyfile([]Route)`, `atomicConfig`.
- Produces:
  - `type ProxyState int` with `const (ProxyDown ProxyState = iota; ProxyServing)`
  - `func Reconcile(ctx context.Context, d Dependencies) (ProxyState, error)`

Behavior table (the whole contract):

| Registry | Container | Action | Returns |
|---|---|---|---|
| routes exist | absent/stopped | write derived Caddyfile if missing/stale, then `Start`/`StartExisting` | `ProxyServing, nil` |
| routes exist | running | if on-disk Caddyfile ≠ derived render: rewrite + `Reload`; else no-op | `ProxyServing, nil` |
| routes exist | (any) but start/reload fails | leave state as-is | `ProxyDown, err` |
| no routes | running | `Stop` | `ProxyDown, nil` |
| no routes | absent/stopped | nothing | `ProxyDown, nil`

- [ ] **Step 1: Write the failing tests** in `reconcile_test.go` — table-driven over the five rows above using a scripted `LifecycleEngine` (record which methods were called) and a real temp-dir registry. Assert: (a) returned state, (b) exact set of engine calls per row, (c) on the "start fails" row the error is returned and no further calls are made.

```go
func TestReconcileTable(t *testing.T) {
	// rows: name, seedRoutes []Route, engineState ContainerState,
	//       startErr error, want ProxyState, wantErr bool, wantCalls []string
	rows := []struct{ ... }{
		{"routes+absent starts", 1, ContainerAbsent, nil, ProxyServing, false, []string{"Start"}},
		{"routes+running stale config reloads", 1, ContainerRunning, nil, ProxyServing, false, []string{"Reload"}},
		{"routes+running fresh config no-op", 1, ContainerRunning, nil, ProxyServing, false, nil},
		{"routes+absent start fails", 1, ContainerAbsent, errors.New("boom"), ProxyDown, true, []string{"Start"}},
		{"no routes+running stops", 0, ContainerRunning, nil, ProxyDown, false, []string{"Stop"}},
	}
	// ...
}
```

For the "stale config" row, pre-write a Caddyfile that differs from `RenderCaddyfile(routes)`; for "fresh", write exactly the derived bytes.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/wtree/ -run TestReconcileTable -v`
Expected: FAIL — undefined `Reconcile`.

- [ ] **Step 3: Implement** in `reconcile.go`

`Reconcile`: `routes, _ := d.Registry.List(ctx)`; get engine via `d.Engine(ctx)`; `state, _ := engine.State(ctx)`; branch per the table. "Stale" comparison is byte-equality of `os.ReadFile(d.Paths.Config)` vs `RenderCaddyfile(routes)` output (missing file counts as stale). Use `atomicConfig` for rewrites. Do NOT take the registry lock around engine work (List already does its own brief lock; the registry docs forbid nested Registry calls).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/wtree/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wtree/reconcile.go internal/wtree/reconcile_test.go
git commit -m "Add Reconcile: converge container state to the registry on every command"
```

---

### Task 4: Truthful status with one-line hints

**Files:**
- Modify: `internal/wtree/cli.go` (status path in `Run`)
- Modify: `internal/wtree/reconcile.go` (add hint classifier)
- Test: `internal/wtree/cli_test.go`

**Interfaces:**
- Consumes: `Reconcile(ctx, d) (ProxyState, error)` from Task 3.
- Produces:
  - `func StaleHint(err error) string` — classifies the reconcile error into one hint: contains "bind" or "address already in use" → `run: sudo sysctl -w net.ipv4.ip_unprivileged_port_start=443`; contains "pull" or "manifest" → `check network access to docker.io/library/caddy:2.11.9`; contains "permission denied" → the existing SELinux relabel note; anything else → `inspect: podman logs wtree-caddy`.
  - `status` output format (one line per route, tab-separated):
    `https://<slug>.wtree.dev\t127.0.0.1:<port>\t<worktree>\t<serving|stale (proxy down)>`
    plus, when any route is stale, one trailing hint line: `hint: <StaleHint(err)>`.
  - `Run` returns 1 when any route is stale, 0 otherwise (empty registry → 0, no output).

- [ ] **Step 1: Write the failing tests** in `cli_test.go`

```go
func TestStatusReportsStaleWithHint(t *testing.T) {
	// deps: real temp registry seeded with one route; scripted engine whose
	// State returns ContainerAbsent and Start returns errors.New("bind: address already in use")
	code := Run(context.Background(), []string{"status"}, &out, &errOut, d)
	if code != 1 { t.Fatalf("exit = %d, want 1", code) }
	if !strings.Contains(out.String(), "stale (proxy down)") { t.Fatalf("missing stale marker: %s", out.String()) }
	if !strings.Contains(out.String(), "sudo sysctl -w net.ipv4.ip_unprivileged_port_start=443") {
		t.Fatalf("missing port hint: %s", out.String())
	}
}

func TestStatusServingExitsZero(t *testing.T) {
	// engine State returns ContainerRunning, Caddyfile matches derived render
	code := Run(context.Background(), []string{"status"}, &out, &errOut, d)
	if code != 0 || !strings.Contains(out.String(), "serving") { t.Fatalf("code=%d out=%s", code, out.String()) }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/wtree/ -run 'TestStatusReportsStaleWithHint|TestStatusServingExitsZero' -v`
Expected: FAIL — status still prints registry-only lines, exit 0.

- [ ] **Step 3: Implement** in `cli.go` + `reconcile.go`

In the `status` branch of `Run`: call `Reconcile(ctx, deps)`; print each route with its state column (`serving` when `ProxyServing`, else `stale (proxy down)`); on `ProxyDown` with routes present, append the hint line from `StaleHint(err)` and return 1.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/wtree/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wtree/cli.go internal/wtree/cli_test.go internal/wtree/reconcile.go
git commit -m "status reports real proxy state with a one-line fix hint; exit 1 when stale"
```

---

### Task 5: TLS auto-provisioning (local CA + wildcard leaf)

**Files:**
- Create: `internal/wtree/tls.go`
- Modify: `internal/wtree/app.go` (`addRoute`: call provision before `validateCertificate`)
- Test: `internal/wtree/tls_test.go`

**Interfaces:**
- Consumes: `Paths`, existing `validateCertificate`.
- Produces:
  - `func ProvisionTLS(p Paths, hint io.Writer) (generatedCA bool, err error)`
  - Files under `p.Dir/tls/`: `ca.key` (0600), `ca.pem` (0644), `fullchain.pem` (0600), `privkey.pem` (0600); hint marker `tls/ca.trusted-hint-shown` (0600).
  - Behavior: if a valid leaf already exists at `p.Cert`/`p.Key` (per `validateCertificate`) → no-op, return `(false, nil)`. Otherwise: load existing CA if present, else generate an ECDSA P-256 CA (10-year validity, CN `wtree local CA`, IsCA, keyUsage CertSign); issue a `*.wtree.dev` wildcard leaf (1-year validity, SAN `*.wtree.dev` + `wtree.dev`) signed by the CA; write all files atomically. When a NEW CA is generated and the marker file does not exist: write the marker and print exactly one line to `hint`:
    `trust the local CA once so browsers accept wtree URLs: <os-specific command>` (Linux: `sudo cp <ca.pem> /usr/local/share/ca-certificates/wtree.crt && sudo update-ca-certificates`; macOS: `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain <ca.pem>`).
  - Leaf regeneration (CA reused) never re-shows the hint.

- [ ] **Step 1: Write the failing tests** in `tls_test.go`

```go
func TestProvisionGeneratesWildcard(t *testing.T) {
	p := testPaths(t) // temp dir
	var hint bytes.Buffer
	generated, err := ProvisionTLS(p, &hint)
	if err != nil || !generated { t.Fatalf("generated=%v err=%v", generated, err) }
	if err := validateCertificate(p); err != nil { t.Fatalf("generated cert fails validation: %v", err) }
	// leaf is ~1 year, CA is ~10 years
	// hint contains exactly one line mentioning the CA path
}

func TestProvisionIdempotent(t *testing.T) {
	p := testPaths(t); var h1, h2 bytes.Buffer
	if _, _ = ProvisionTLS(p, &h1); _, err := ProvisionTLS(p, &h2); err != nil { t.Fatal(err) }
	if h2.Len() != 0 { t.Fatalf("second call printed hint: %q", h2.String()) }
	// ca.pem bytes unchanged between calls
}

func TestProvisionReusesCAForExpiredLeaf(t *testing.T) {
	p := testPaths(t); var h1 bytes.Buffer
	if _, _ = ProvisionTLS(p, &h1); err := os.Remove(p.Cert); err != nil { t.Fatal(err) }
	caBefore, _ := os.ReadFile(filepath.Join(p.Dir, "tls", "ca.pem"))
	var h2 bytes.Buffer
	if generated, _ := ProvisionTLS(p, &h2); generated { t.Fatal("regenerated CA when only leaf was missing") }
	caAfter, _ := os.ReadFile(filepath.Join(p.Dir, "tls", "ca.pem"))
	if !bytes.Equal(caBefore, caAfter) { t.Fatal("CA changed on leaf regeneration") }
	if h2.Len() != 0 { t.Fatalf("hint re-shown: %q", h2.String()) }
}

func TestProvisionRespectsExistingValidCert(t *testing.T) {
	// seed p.Cert/p.Key with a valid *.wtree.dev pair (generate inline), no CA files
	generated, err := ProvisionTLS(p, &hint)
	if generated || err != nil { t.Fatalf("must not touch user-provisioned certs: %v %v", generated, err) }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/wtree/ -run TestProvision -v`
Expected: FAIL — undefined `ProvisionTLS`.

- [ ] **Step 3: Implement** in `tls.go`

Use stdlib only: `crypto/ecdsa`, `crypto/x509`, `math/big`, `encoding/pem`. CA key persisted as PEM PKCS8. Leaf template: `DNSNames: ["*.wtree.dev", "wtree.dev"]`, `KeyUsage: DigitalSignature|KeyEncipherment`, `ExtKeyUsage: ServerAuth`, `NotAfter = now + 365d`. Reuse the atomic-write pattern from `atomicConfig` (factor a small `writeFileAtomic(path string, data []byte, mode os.FileMode) error` helper in tls.go and use it for all five files).

- [ ] **Step 4: Wire into addRoute** in `app.go`

In `addRoute`, before `validateCertificate(d.Paths)`: call `ProvisionTLS(d.Paths, <stderr writer>)`. `Run` must pass its `stderr` writer through — extend the `Dependencies` struct with `Stderr io.Writer` (set from `Run`'s parameter in `DefaultDependencies` callers; update `cli_test.go` fixtures).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/wtree/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/wtree/tls.go internal/wtree/tls_test.go internal/wtree/app.go internal/wtree/cli.go internal/wtree/cli_test.go
git commit -m "Auto-provision local CA and *.wtree.dev wildcard cert on first use"
```

---

### Task 6: Per-session port-443 threshold management

**Files:**
- Create: `internal/wtree/threshold.go`
- Test: `internal/wtree/threshold_test.go`

**Interfaces:**
- Consumes: `Paths`.
- Produces:
  - `type SudoRunner func(args ...string) error` (injected; production impl shells out to `sudo` with the given args, streaming its own terminal I/O).
  - `func CurrentThreshold() (int, error)` — reads `/proc/sys/net/ipv4/ip_unprivileged_port_start`; on non-Linux returns `(0, nil)` meaning "no management needed".
  - `func EnsureThreshold443(p Paths, current func() (int, error), sudo SudoRunner) error` — if `current() <= 443`: no-op (and if a marker exists whose recorded original equals the current value, delete the stale marker — the reboot case). If `> 443`: print one line (`port 443 requires lowering the unprivileged-port threshold; sudo password required`), call `sudo("sysctl", "-w", "net.ipv4.ip_unprivileged_port_start=443")`, then write marker `<p.Dir>/port-threshold` (0600, JSON: `{"original": <int>, "set_at": <RFC3339>}`).
  - `func RestoreThreshold(p Paths, current func() (int, error), sudo SudoRunner) error` — if no marker: nil. If marker's original == current(): delete marker, nil (already restored, e.g. by reboot). Else call `sudo("sysctl", "-w", "net.ipv4.ip_unprivileged_port_start=<original>")`; on success delete marker; on failure return the error AND print the one-line manual command.
  - Marker path constant: `port-threshold` directly under `p.Dir`.

- [ ] **Step 1: Write the failing tests** in `threshold_test.go` — inject fake `current` and record `sudo` calls:

```go
func TestEnsureLowerAndRecord(t *testing.T) {
	p := testPaths(t); var calls [][]string
	sudo := func(args ...string) error { calls = append(calls, args); return nil }
	if err := EnsureThreshold443(p, func() (int, error) { return 1024, nil }, sudo); err != nil { t.Fatal(err) }
	// marker exists with original=1024; one sudo call with the sysctl args
}

func TestEnsureNoOpWhenAlreadyLow(t *testing.T) {
	// current=443, no marker → no sudo call, no marker written
}

func TestEnsureDiscardsStaleMarkerAfterReboot(t *testing.T) {
	// pre-write marker original=1024; current()=1024 (kernel reverted)
	// → Ensure succeeds with zero sudo calls and deletes the marker
}

func TestRestoreOnlyOwnsWhatItLowered(t *testing.T) {
	// no marker, current=443 (user-owned) → Restore is a no-op, no sudo call
}

func TestRestorePrintsManualCommandOnFailure(t *testing.T) {
	// marker original=1024, current=443, sudo returns error
	// → error returned, stderr contains "sudo sysctl -w net.ipv4.ip_unprivileged_port_start=1024", marker still present
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/wtree/ -run 'TestEnsure|TestRestore' -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement** in `threshold.go` per the interface above. Marker read/write is plain JSON + atomic write (reuse `writeFileAtomic` from Task 5).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/wtree/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wtree/threshold.go internal/wtree/threshold_test.go
git commit -m "Manage the unprivileged-port threshold per session with a marker file"
```

---

### Task 7: Stop the proxy when the last route is removed

**Files:**
- Modify: `internal/wtree/app.go` (`activate`)
- Test: `internal/wtree/app_test.go` (create if absent; extend existing app tests)

**Interfaces:**
- Consumes: existing `activate` rollback structure, `LifecycleEngine`.
- Produces: when the applied route set is **empty**: remove the on-disk Caddyfile (if present), and transition the container to stopped — `Stop()` when running, `Remove()` only if this invocation created it (absent→started case, preserving today's rollback semantics). When non-empty: unchanged. Rollback for the empty case: if the commit fails after stopping, restart the previous Caddyfile + container (extend the existing `rollback` closure to handle the stopped-after-running transition).

- [ ] **Step 1: Write the failing test**

```go
func TestRemoveLastRouteStopsContainer(t *testing.T) {
	// scripted engine: State → ContainerRunning; record calls
	// registry seeded with one route; Caddyfile present and matching
	err := removeRoute(ctx, d, "slug", "")
	// want: err nil; engine calls include Stop (not Reload); Caddyfile removed from disk
}

func TestRemoveLastRouteRollbackRestarts(t *testing.T) {
	// same setup but make the registry commit fail after activate succeeds
	// want: container restarted with previous Caddyfile restored
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wtree/ -run 'TestRemoveLastRoute' -v`
Expected: FAIL — Reload is called, container left running.

- [ ] **Step 3: Implement** in `activate`: branch on `len(routes) == 0` after validation; adjust the `rollback` closure's state switch to cover "was running, now stopped → restore by StartExisting/Start + rewrite old Caddyfile".

- [ ] **Step 4: Run tests**

Run: `go test ./internal/wtree/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wtree/app.go internal/wtree/app_test.go
git commit -m "Stop the Caddy proxy when the last route is removed (443 bound only while active)"
```

---

### Task 8: Wire it together + integration proof

**Files:**
- Modify: `cmd/wtree/main.go` (signal-aware root context)
- Modify: `internal/wtree/cli.go` (`Run`: reconcile on up/down/status; threshold ensure on up; threshold restore when a down empties the registry; production `SudoRunner`)
- Modify: `internal/wtree/podman_integration_test.go` (new integration cases, gated by `WTREE_PODMAN_INTEGRATION=1`)

**Interfaces:**
- Consumes: everything from Tasks 1–7.
- Produces: the end-to-end behaviors pinned in Review Focus lines 1–3.

- [ ] **Step 1: Write the failing integration tests** (extend `podman_integration_test.go`, same env gate and isolated-name pattern as the existing test):

```go
// TestSelfHealAfterContainerKill: up a route; podman kill wtree-caddy;
// run status → expect exit 0, "serving" (reconcile restarted the proxy).
// TestRebootMarkerStaleness: write a marker with original=<current threshold>;
// run up → succeeds, marker gone or re-written correctly, no second sudo needed
// (run with a SudoRunner that fails if invoked — proves the stale path is silent).
// TestDownStopsContainerAndRestoresThreshold: set threshold via the real sysctl
// only if the test environment allows (skip otherwise); up; down; assert
// container absent/stopped and threshold back to original.
```

- [ ] **Step 2: Run to verify they fail**

Run: `WTREE_PODMAN_INTEGRATION=1 go test ./internal/wtree/ -run 'TestSelfHeal|TestRebootMarker|TestDownStops' -v`
Expected: FAIL — status shows stale / container lingers.

- [ ] **Step 3: Implement the wiring** in `cli.go` + `main.go`

- `main.go`: `ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM); defer stop()`; pass ctx to `Run`.
- `up`: after flag parsing and git resolution — `EnsureThreshold443(paths, CurrentThreshold, prodSudo)` (Linux only; no-op elsewhere), then `Reconcile`, then existing `addRoute` (which now provisions TLS first).
- `down`: `Reconcile` first, then `removeRoute`; if the registry is now empty → `RestoreThreshold`.
- `status`: `Reconcile` + truthful output (Task 4 already did the printing; ensure reconcile runs here too — it does via Task 4's path).
- `prodSudo`: `exec.Command("sudo", args...)` with `Stdin/Stdout/Stderr = os.*` so the password prompt goes straight to the user's terminal.

- [ ] **Step 4: Run full suite + integration**

Run: `go test ./... && WTREE_PODMAN_INTEGRATION=1 go test ./internal/wtree/ -run Integration -v`
Expected: PASS.

- [ ] **Step 5: Manual smoke (document in commit body)**

Fresh temp config dir, no certs: `wtree up --port 8000 --slug smoke` → one trust-hint line + URL; `curl --resolve smoke.wtree.dev:443:127.0.0.1 https://smoke.wtree.dev/` works after trusting the CA; `podman kill wtree-caddy`; `wtree status` → serving again; `wtree down` → container stopped, threshold restored.

- [ ] **Step 6: Commit**

```bash
git add cmd/wtree/main.go internal/wtree/cli.go internal/wtree/podman_integration_test.go
git commit -m "Wire reconcile, TLS provisioning, and per-session threshold into the CLI"
```
