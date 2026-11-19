# wtree.dev Local Development Proxy — Design

**Status:** Draft for approval  
**Date:** 2026-11-19

## Intent

Give company engineers a low-friction way to open parallel Git worktree dev servers at stable HTTPS URLs, without remembering ports. The primary workflow should fit existing project commands: an engineer runs `make start`, sees a URL such as `https://jira-12345.wtree.dev`, and can work. A corresponding stop action removes the route.

V1 is a local developer tool, not a public SaaS. Each engineer runs their own proxy and routes on their own machine. The `*.wtree.dev` DNS record already resolves to `127.0.0.1`.

## Goals

- One `wtree` CLI shared by project Make targets; explicit route lifecycle (`up`/`down`), not a process wrapper that owns the dev server.
- A reusable `wtree` module in the shared makefiles repository so projects can call `make start` without requiring a separate CLI installation step.
- One persistent Caddy container per developer machine, started automatically by the first `wtree up`/`make start`.
- Route a hostname to a port on the same host, including servers bound only to `127.0.0.1`.
- Publicly trusted HTTPS without a per-machine CA installation or keychain import. Use one shared wildcard certificate for `*.wtree.dev`; developers do not receive Porkbun API credentials.
- macOS and Linux support in v1. Keep the CLI and state format portable enough to support Windows in v2.
- Derive short, DNS-safe slugs from common ticket-bearing branch names, with an explicit override.

## Non-goals for v1

- Public remote access, sharing links, tunnels, user accounts, or a central control plane.
- Editing wildcard address records in Porkbun, public remote access, or a built-in secret-management service.
- Starting or supervising arbitrary dev-server processes; project Make targets retain that responsibility.
- LAN exposure of dev servers. Bind Caddy's published listener to loopback, and do not require app servers to bind to all interfaces.
- Windows support in v1 (required for v2).
- Additional browser-origin filtering or wtree authentication beyond binding Caddy to loopback; v1 treats the proxy like other local dev servers.

## User workflow

1. A project imports the shared makefiles wtree module. `make start` declares the wtree CLI as a prerequisite, starts the project's dev server on a known port, and calls `wtree up --port <port>`; the developer may pass `--slug <slug>` to override inference. The target prints the resulting HTTPS URL.
2. `wtree` detects the current Git branch and derives a slug. For a branch containing a ticket key such as `feat/JIRA-12345-this-is-a-new-feature`, use the lowercased key (`jira-12345`). Without a ticket key, sanitize and shorten the branch name. Reject invalid or conflicting slugs with a clear error and an override hint (for example, `use --slug xyz`).
3. `wtree` ensures the per-machine Caddy container is running, adds the host-to-port route only if the slug is unused, reloads Caddy, and reports the URL. If the slug is already active—even from this same worktree—`wtree up` fails like a dev server reporting an occupied port. Stop it with `wtree down` before starting it again; use `--slug` to choose another hostname when needed.
4. The project stop target calls `wtree down` for that slug before stopping the dev server. `wtree status` lists active routes. Stopping a dev server outside the target may leave a stale route until `down`; requests then return Caddy's normal upstream error.
5. On first invocation, the makefiles prerequisite checks for the pinned wtree executable in `.makefiles/tools/`; if missing, it downloads the matching macOS/Linux release artifact, verifies its published checksum, and installs it atomically, analogous to the Composer binary target. The shared wildcard certificate and private key must be provisioned separately through an approved company channel before first use. `make start` checks for them at the documented per-user location and gives a concise setup error if absent. Normal starts do not prompt for certificate trust/import. A one-time privileged setup may be needed to bind port 443, depending on OS and container engine.

## Architecture

- **CLI:** a small Go executable named `wtree`. Go is the implementation choice to produce release binaries for macOS/Linux now and Windows later. It invokes the installed Docker or Podman CLI rather than embedding a container-engine API. The shared makefiles module provisions the version-pinned platform binary into `.makefiles/tools/` only when a dependent target needs it, following the Composer module's cached executable prerequisite pattern; engineers do not need Go or a separate package manager.
- **Proxy:** one official Caddy image/container per machine, using host networking so it can proxy to `127.0.0.1:<port>` without changing dev-server bind addresses. Caddy listens on `127.0.0.1:443`; no public listener is intended. Persist Caddy's data and route state across container restarts.
- **Routes:** `wtree` keeps a small per-user route registry, renders the Caddy configuration from that registry, validates it, and reloads Caddy. Updates should be serialized and written atomically so concurrent worktree starts do not clobber each other. The Caddy admin endpoint, if used for reloads, remains bound to loopback and is never exposed to the LAN.
- **TLS:** load the shared, publicly trusted `*.wtree.dev` certificate and private key into Caddy as read-only files. The certificate is obtained directly from Let's Encrypt using DNS-01; Porkbun is the DNS API used to publish the temporary challenge TXT record. One designated renewal process holds the restricted Porkbun credentials and renews the wildcard certificate. Do not issue wildcard certificates independently on every laptop, distribute Porkbun API credentials, commit certificate material, or bake it into the image. Certificate/key delivery to developer machines is outside the wtree service; the company must use an existing approved channel. Shared-key exposure is an accepted internal-tool tradeoff; rotate and redistribute if compromised.
- **Platform/runtime assumption:** Linux supports host networking with Docker or Podman. On macOS, v1 assumes Docker Desktop with its host-networking feature enabled (Docker Desktop 4.34+); Podman Machine on macOS is not part of the v1 support promise unless a later validation proves the same loopback behavior. Port 443 binding may need one-time privileged setup, but routine `make start` should not need elevated privileges.

## Approaches considered

1. **Caddy container on host networking — selected.** Preserves loopback-only upstreams and Caddy's container packaging. Linux supports host networking directly; macOS uses Docker Desktop host networking. Tradeoff: requires a one-time Docker Desktop setting on Mac and engine-specific bootstrap checks.
2. **Caddy installed natively on each host.** Easiest access to localhost and native trust stores, but conflicts with the team's container-first preference and requires separate Caddy installation/update instructions.
3. **Bridged Caddy container plus dev servers bound to all interfaces or a host forwarding helper.** Avoids host networking, but either exposes dev-server ports to the LAN (against the local-only goal) or introduces another moving part. Not selected.

For TLS, use one publicly trusted wildcard certificate renewed centrally with Let's Encrypt DNS-01 and distribute it through the company's existing secure channel. Per-laptop DNS-01 would spread DNS credentials and repeat issuance for the same wildcard; Caddy's local CA would require the browser trust setup the team wants to avoid. The shared key is a deliberate simplicity tradeoff.

## Constraints and calls

- The process must remain local-only even though `*.wtree.dev` is in public DNS; DNS resolving to loopback does not make the developer's service publicly reachable.
- Public sharing later will require a separate gateway/tunnel design and public TLS termination. Do not design or implement that path in v1.
- Certificate delivery is not implemented by wtree; the existing company secure distribution method and target per-user file location must be confirmed. Keep the DNS API credentials only with the designated renewal process.
- Host port 443 setup differs by OS and container engine. Keep OS-specific bootstrap code behind the CLI; document supported combinations plainly rather than adding a daemon or account system.
- Add a reusable wtree module to `github.com/darianbr/makefiles` and an example consumer. Project Make targets provide the port and invoke `wtree`; different dev servers remain project-owned.
- Caddy is bound only to loopback. V1 adds no wtree auth or browser-origin filtering; dev applications retain their normal CORS/CSRF behavior.
- If any worktree resolves to a slug that is already active, the registration fails clearly and does not replace the existing route or silently invent a suffix. This also applies to a repeated `up` from the same worktree; first run `down`, analogous to freeing a port before restarting a dev server.

## Open to the builder

Choose the simplest Caddy config representation and reload mechanism that preserves all active routes across concurrent calls and restarts. Define exact slug fallback/truncation rules and command output/error text. Validate the selected Docker Desktop host-network behavior on macOS and Docker/Podman loopback access on Linux before treating the platform matrix as complete. Confirm the company-approved certificate/key distribution channel and per-user location before implementing the first-run check. Define release artifact naming and checksum publication so the makefiles module can provision the right binary safely.
