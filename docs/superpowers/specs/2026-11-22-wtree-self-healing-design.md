# wtree Self-Healing Design

Date: 2026-11-22
Status: proposed (pending approval)

## Intent and why

wtree's job is to be invisible: `make start` gives you a trusted HTTPS URL for a
worktree, and you forget it exists. Today the core loop works, but the system
violates the design philosophy in four places:

1. **Unbounded operations.** `main.go` passes `context.Background()` into every
   podman call; the registry flock has no acquisition deadline. A stalled
   container daemon hangs `wtree up` forever while holding the lock — and then
   every future `wtree` command blocks on that lock. Deadlock by design gap.
2. **No reconciliation.** If the Caddy container crashes or the host reboots,
   routes are dead but `status` still lists them as live (it reads only the
   registry). Nothing converges until the user happens to run `up`.
3. **Manual cert provisioning.** A fresh consumer hits "provision
   WTREE_CERT_FILE and WTREE_KEY_FILE" and must figure out how to make a
   `*.wtree.dev` wildcard cert. The happy path has ceremony.
4. **Manual port 443.** Rootless binding of 443 requires lowering
   `net.ipv4.ip_unprivileged_port_start`, done by hand per session.

The user's requirements, in their words: stability above all — no crashed
services, no records out of sync, no timeouts or deadlocks; "get out of my way,
if I notice it, it failed"; and for port 443 specifically, **security takes
priority over convenience** — permanently lowering the threshold is not
acceptable, and one sudo password after a PC crash/reboot is an accepted cost.

## Goals

- G1: No wtree operation can hang indefinitely. Every container-engine call and
  every lock acquisition has a deadline; on expiry the command fails with a
  clear, actionable error.
- G2: After any failure mode — Caddy crash, wtree process kill, host reboot —
  the next `wtree` invocation converges to the correct state automatically. No
  manual repair steps exist for any reachable bad state.
- G3: `status` always reports truth (never lists a dead route as serving).
- G4: First-run TLS is automatic: wtree generates its own local CA and
   `*.wtree.dev` wildcard cert; the user performs at most one trust-store step,
   shown once with the exact command for their OS.
- G5: Port 443 is bindable only while routes are active. The threshold is
  lowered per session (one sudo when needed) and restored when the last route
  is removed. No persistent system configuration is written.

## Non-goals

- No host-side 443 forwarder daemon / `setcap` approach (considered, rejected
  for v1 — see Approaches).
- No persistent sysctl config file (`/etc/sysctl.d/...`) — rejected on security
  grounds by the user.
- No changes to macOS behavior (still fail-closed on darwin).
- No LAN exposure; loopback-only isolation is unchanged.
- No auto-update of the wtree binary itself (the Makefiles module owns that,
  explicitly).

## Anti-goals (what would make this a failure)

- Any bad state whose repair requires more than one copy-pasteable command.
- More than one sudo prompt per boot cycle.
- Port 443 bound while no routes are active.
- A new long-running host process that can itself crash.

## Constraints

- Rootless podman (preferred) or docker, `--network=host`, pinned image
  `docker.io/library/caddy:2.11.9`.
- Records live on the host (`~/.config/wtree/`); the container is disposable.
  This separation is load-bearing for G2 and must be preserved.
- `gofrs/flock v0.13.0` (already a dependency) provides `SetDeadline`/`TryLock`.
- CLI contract is stable: `up --port [--slug]`, `down [--slug]`, `status`,
  `version`; the Makefiles module targets (`up down status clean install help`)
  are unchanged.
- The Make module must keep working under GNU Make 3.81 / Bash 3.2 (macOS).

## Approaches considered

**A — Self-healing + bounded operations (chosen).** Keep the architecture
(host-side binary, disposable container, registry as source of truth); close
the four gaps with timeouts, reconcile-on-every-command, truthful status, and
auto-provisioned certs.

**B — Ephemeral proxy per session.** Start Caddy on first `up`, stop on last
`down`. Rejected: adds start/stop failure moments, fixes none of the four gaps,
and a mid-session crash kills the user's URL. (Note: B's "stop on last down"
is adopted as one behavior of A — see D6 — because it shrinks the 443 exposure
window, which serves G5.)

**C — wtree bundled into the Caddy image.** Rejected: bootstrap problem (the
host binary is what starts the container) and it would move the records into
the disposable component, raising recovery cost.

### Port 443 sub-approaches

1. **Persistent sysctl file** (`/etc/sysctl.d/99-wtree.conf`): zero steps after
   reboot, but permanently lowers the host-wide threshold. Rejected by user:
   security priority.
2. **setcap forwarder**: `cap_net_bind_service` on the wtree binary, which
   listens on 443 and forwards to Caddy on a high port. Survives all crash
   modes with no steps, but requires a new long-running host daemon — a new
   crash surface. Rejected for v1; remains the "most correct" future option.
3. **Per-session sysctl with self-healing restore (chosen).** Lower only when
   needed, restore on last route removal, kernel reverts on reboot. One sudo
   after a PC crash/reboot — explicitly accepted by the user.

## Design

### D1 — Bounded operations

- Every `Engine` call (`State`, `CheckMounts`, `Start`, `StartExisting`,
  `Validate`, `ValidateCandidate`, `Reload`, `Stop`, `Remove`) runs under a
  context deadline. Default 30s; overridable via `WTREE_TIMEOUT` (seconds) for
  slow hosts. On timeout: fail with an error naming the operation and the
  deadline, e.g. `wtree: start Caddy timed out after 30s (podman may be stuck;
  check 'podman ps' and try again)`.
- Registry lock acquisition uses `flock.SetDeadline(now + 5s)` (override via
  `WTREE_LOCK_TIMEOUT`). On deadline: fail fast with
  `wtree: another wtree process holds the registry lock and appears stuck; it
  will time out on its own — retry shortly`. Never block indefinitely.
- The existing 30s rollback context in `activate()` stays.

### D2 — Reconcile on every command

A single `reconcile(ctx, deps)` step runs at the start of `up`, `down`, and
`status`, after lock acquisition, before any other action:

| Registry | Container | Action |
|---|---|---|
| routes exist | absent or stopped | start from the derived Caddyfile (existing `activate` path) |
| routes exist | running | verify on-disk Caddyfile matches the registry-derived render; if not, rewrite + reload (heals manual edits/corruption) |
| no routes | running | stop the container |
| no routes | absent/stopped | nothing |

Consequences: every command is idempotent and convergent; a crash of any kind
is repaired by the next invocation with zero user action; `status` becomes a
safe "check and mend" entry point. Reconcile failures (e.g. 443 not bindable)
do not corrupt state — they surface as a stale status (D3) plus the one-line
fix hint.

Idempotency argument (builder's call, resolving review question): reconcile is
a pure function of (registry, container state, on-disk Caddyfile) → actions,
and every action is individually idempotent — atomic Caddyfile rewrite derives
from the registry (same input, same bytes); `podman start` on a running
container is a no-op; reload with an unchanged config is a no-op; `Stop()`
already checks state first. A crash mid-reconcile (e.g. Caddyfile rewritten,
container not yet started) leaves a state the next reconcile converges from:
routes exist + container stopped → start from the derived Caddyfile. There is
no feedback loop that flips state back, so repeated invocations cannot
oscillate. No progress-tracking state is needed.

### D3 — Truthful status

`status` runs reconcile first, then prints each route with its real state:

```
https://jira-1234.wtree.dev   127.0.0.1:8000   /path/to/worktree   serving
https://old.wtree.dev         127.0.0.1:8001   /path/to/other      stale (proxy down)
```

Exit code 0 when all routes are serving; 1 when any route is stale (so scripts
can detect). A stale line carries a one-line hint naming the single fix
(e.g. `run: sudo sysctl -w net.ipv4.ip_unprivileged_port_start=443`). This is
the only case where wtree surfaces a problem, and the fix is always one command
— cheaper than the break.

Hint selection (builder's call, resolving review question): reconcile already
classifies the failure it hit — it knows whether the container start failed
and why (the podman error string). Map that to a small fixed set of hints:
port-443 bind failure → the sysctl command; image pull failure → "check
network / image availability"; mount/permission failure → the SELinux relabel
note (existing voice); anything else → `podman logs wtree-caddy` as the single
next step. No new diagnostics are run beyond what reconcile already does —
the hint is a classification of an error we already have, not a probe.

### D4 — Cert auto-provisioning

- Layout under the config dir (`~/.config/wtree/tls/` unless overridden):
  `ca.key` (0600), `ca.pem`, `fullchain.pem`, `privkey.pem`.
- Before validation, a `provision` step runs: if no valid leaf cert exists at
  the configured paths, generate (or reuse) a local CA and issue a
  `*.wtree.dev` wildcard leaf signed by it. The CA is long-lived (10 years);
  the leaf is 1 year — expiry is not user-visible because provision regenerates
  the leaf from the stored CA whenever validation finds it expired or missing.
- Existing valid certs (user-provisioned, or `WTREE_CERT_FILE`/`WTREE_KEY_FILE`
  overrides) are respected and never overwritten; wtree only generates when
  there is nothing valid to use.
- One-time trust hint: when wtree generates the CA for the first time it prints
  exactly one line with the OS-specific command to trust `ca.pem` (Linux: copy
  into `/usr/local/share/ca-certificates/` + `update-ca-certificates`; macOS:
  add to the login keychain). "First time" is tracked by a marker file
  (`tls/ca.trusted-hint-shown`, 0600) written after the hint is printed — not
  by cert-file existence, so a leaf regeneration (CA reused) never re-shows it.
  If the CA itself is deleted and regenerated, the marker is gone with it and
  the hint shows again, which is correct: a new CA needs re-trusting.
- `validateCertificate` remains the gate; it now runs after provision.

### D5 — Port 443, per session (security-first)

- On any path that starts the container (first `up`, or reconcile-start), check
  the effective threshold (`/proc/sys/net/ipv4/ip_unprivileged_port_start` on
  Linux). If it is > 443:
  - wtree prints one line and invokes `sudo sysctl -w
    net.ipv4.ip_unprivileged_port_start=443` interactively (the user types the
    password in their own terminal; wtree never handles it).
  - On success, record the original value in a marker file
    (`~/.config/wtree/port-threshold`, 0600: original value + timestamp).
- If the threshold is already ≤ 443 (user set it themselves), wtree does not
  touch it and writes no marker — it will never restore a user-owned setting.
- Restore: when the last route is removed (registry becomes empty) and a marker
  exists, restore the recorded original value. Ordering (builder's call,
  resolving review question): the registry commit happens first — `down` must
  succeed even if restore fails. Restore then runs best-effort; on failure the
  command still reports success but prints the one-line manual restore command.
  The marker is deleted only on successful restore, so any later wtree command
  (reconcile sees: no routes + marker present) retries the restore. Converges
  without user action once sudo is available again; worst case is the accepted
  one-command manual restore.
- Crash analysis (all converge):
  - Caddy crash: threshold still lowered, marker present → next command
    reconciles and restarts Caddy. No user action.
  - wtree killed: same as above; the marker survives in the config dir.
  - PC crash/reboot: kernel reverts the threshold; the stale marker is detected
    (current value already equals the recorded original) and discarded; the
    next `up` prompts for sudo once. Accepted cost.
- Exposure window: with D6, port 443 is bound only while routes are active.

### D6 — Container stops when the last route is removed (builder's call)

Today the container lingers after the final `down` (it had to be cleaned up by
hand in the e2e test). Change: when the registry becomes empty, stop the
container (keep the config dir and volume for fast restart). This shrinks the
443 exposure window to exactly the active-routes period (serves G5/security)
and removes lingering state. Cost: the next `up` pays a ~1–2s container start
(image already cached) — invisible in practice.

## What is left to the builder

- Exact timeout defaults tuning and `WTREE_TIMEOUT`/`WTREE_LOCK_TIMEOUT`
  naming if a better fit emerges.
- Error message wording (must stay in wtree's existing terse voice).
- Test strategy: extend `podman_integration_test.go` for reconcile cases
  (crashed container, stale marker, threshold restore) and unit tests for the
  provision logic with temp CAs.
- Marker file schema details (keep it trivially parseable; it is an
  implementation detail, not a contract).
