# wtree.dev Local Development Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Give engineers a one-command, local-only HTTPS URL for a dev server running in a Git worktree, with Caddy and a reusable shared Make module.

**Architecture:** A Go `wtree` CLI keeps a per-user route registry and drives one Caddy container on host networking. A shared makefiles module lazily downloads a version-pinned CLI binary into the project’s `.makefiles/tools/` cache and exposes `up`, `down`, and `status` targets.

**Tech Stack:** Go 1.26, Caddy official container image, Docker/Podman CLI, GNU Make, POSIX shell, GitHub Actions.

**Spec:** [../specs/2026-11-19-wtree-design.md](../specs/2026-11-19-wtree-design.md)

## Global Constraints

- V1 supports macOS and Linux; Windows is explicitly deferred to v2.
- Caddy uses host networking and listens only on `127.0.0.1:443`; dev servers remain on host loopback. Do not bind a listener or automatic HTTP redirect on port 80 or any non-loopback interface.
- macOS requires Docker Desktop 4.34+ with host networking enabled; Linux supports Docker or Podman.
- TLS uses a shared public certificate for `*.wtree.dev`; cert/key are user-provisioned, mounted read-only, and never committed or baked into an image.
- `wtree up` derives `jira-12345` from `feat/JIRA-12345-this-is-a-new-feature`; branch fallback is normalized to a DNS-safe slug; explicit `--slug` overrides inference.
- Any already-active slug causes `up` to fail with a `--slug` hint; it never replaces or auto-renames an active route, including repeated `up` from the same worktree.
- The Make module lazily provisions the matching CLI binary when a wtree target needs it; engineers need no separate Go install.
- The existing company process distributes the TLS files; wtree does not add a vault, account system, DNS API access, public sharing, or browser-origin filtering.

## Review Focus

- Ticket and fallback branch names containing slashes, uppercase, spaces, punctuation, or long text must produce a valid bounded DNS label or a clear error; pinned by slug tests in Task 1.
- Duplicate slugs, including a repeated `up` from the same worktree, must fail without changing the active route; pinned by registry and CLI tests in Tasks 1 and 3.
- Ports `0`, values above `65535`, and non-numeric input must be rejected before shell/container invocation; pinned by CLI tests in Task 3.
- Missing certificate/key, mismatched key pair, or a cert without `*.wtree.dev` coverage must fail before changing routes; pinned by TLS preflight tests in Task 3.
- Missing container engine, occupied port 443, container startup failure, and Caddy reload failure must return actionable errors and preserve the previous route/config; pinned by fake-engine tests in Tasks 2–3 and a platform smoke test in Task 5.

---

### Task 1: Go module, slug rules, config paths, and route registry

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `internal/wtree/slug.go`
- Test: `internal/wtree/slug_test.go`
- Create: `internal/wtree/paths.go`
- Test: `internal/wtree/paths_test.go`
- Create: `internal/wtree/registry.go`
- Test: `internal/wtree/registry_test.go`

**Interfaces:**
- `ResolveSlug(explicit, branch string) (string, error)` validates an explicit slug or derives one from a branch.
- `type Route struct { Slug string; Port uint16; Worktree string }` is the persisted route record.
- `OpenRegistry(path string) (*Registry, error)`, `(*Registry).Register(Route) error`, `(*Registry).Remove(slug string) (bool, error)`, and `(*Registry).List() ([]Route, error)` provide persistent, serialized route operations.
- Config paths are rooted at `os.UserConfigDir()/wtree`; allow `WTREE_CONFIG_DIR` to override for tests and advanced local setups. TLS defaults to `tls/fullchain.pem` and `tls/privkey.pem` under that directory, with `WTREE_CERT_FILE` and `WTREE_KEY_FILE` overrides.

- [ ] **Step 1: Write failing slug tests** for the ticket example → `jira-12345`, branch fallback normalization and truncation to at most 63 characters, explicit override, and rejection of empty/invalid labels.
- [ ] **Step 2: Run `go test ./internal/wtree -run 'TestResolveSlug|TestValidateSlug' -v`;** confirm the new tests fail because the functions do not exist.
- [ ] **Step 3: Implement slug parsing** in `internal/wtree/slug.go`: use the first Jira-style `[A-Za-z][A-Za-z0-9]*-[0-9]+` key when present; otherwise lowercase, replace each non-alphanumeric run with `-`, trim edge hyphens, and truncate safely to 63 characters. Explicit slugs are lowercased and then validated against DNS-label rules.
- [ ] **Step 4: Run the slug tests;** confirm all pass.
- [ ] **Step 5: Write failing path and registry tests** for stable user-config paths, empty registry creation, persistence across reload, duplicate slug rejection, route removal, sorted listing, and two concurrent registrations for the same slug resulting in exactly one success.
- [ ] **Step 6: Run `go test ./internal/wtree -run 'TestPaths|TestRegistry' -v`;** confirm the new tests fail.
- [ ] **Step 7: Implement paths and the registry** with versioned JSON, atomic temp-file replacement, and a cross-platform lock (`github.com/gofrs/flock`) around read-modify-write operations. Do not store certificate contents in registry state. Ignore local build output and key/certificate files in `.gitignore`.
- [ ] **Step 8: Run `go test ./internal/wtree -v`;** confirm path, persistence, duplicate, and concurrency cases pass.
- [ ] **Step 9: Commit** as `feat: add wtree route state and slug rules`.

### Task 2: Caddyfile renderer and container-engine adapter

**Files:**
- Create: `internal/wtree/caddyfile.go`
- Test: `internal/wtree/caddyfile_test.go`
- Create: `internal/wtree/engine.go`
- Test: `internal/wtree/engine_test.go`

**Interfaces:**
- `RenderCaddyfile(routes []Route) ([]byte, error)` returns deterministic Caddy configuration using the fixed in-container TLS mount paths.
- `type CommandRunner interface { Run(context.Context, string, ...string) ([]byte, error) }` allows engine commands to be tested without Docker or Podman.
- `type Engine struct { Binary string; Runner CommandRunner }` owns container discovery, start, validate, and reload operations for the fixed `wtree-caddy` container.

- [ ] **Step 1: Write failing Caddyfile tests** asserting one `https://<slug>.wtree.dev` site block per route, `bind 127.0.0.1`, the fixed in-container certificate/key paths, `reverse_proxy 127.0.0.1:<port>`, deterministic ordering, and disabled automatic HTTP redirects.
- [ ] **Step 2: Run `go test ./internal/wtree -run TestRenderCaddyfile -v`;** confirm failure before renderer implementation.
- [ ] **Step 3: Implement `RenderCaddyfile`** using validated slugs/ports; set `auto_https disable_redirects`, bind each site to `127.0.0.1`, and use Caddy’s explicit certificate/key directive so Caddy does not attempt per-host ACME issuance.
- [ ] **Step 4: Run the renderer tests;** confirm pass.
- [ ] **Step 5: Write failing fake-runner tests** for engine selection, host-network container creation, loopback listener/config/cert mounts, read-only TLS mounts, persistent Caddy data, detection of an existing container, and error propagation for unavailable engine/port conflict/start failure/reload failure.
- [ ] **Step 6: Run `go test ./internal/wtree -run 'TestEngine' -v`;** confirm failure before adapter implementation.
- [ ] **Step 7: Implement the engine adapter** with `WTREE_CONTAINER_ENGINE` override; use Docker Desktop on macOS and prefer available Podman then Docker on Linux. Start the official Caddy image with host networking, mount the whole config directory read-only (so atomic Caddyfile replacements stay visible), mount TLS files read-only, and persist `/data` in a named volume. Keep Caddy’s admin API at its loopback default; validate config before reload.
- [ ] **Step 8: Run `go test ./internal/wtree -v`;** confirm all renderer and fake-engine tests pass without a real engine.
- [ ] **Step 9: Commit** as `feat: add loopback Caddy container adapter`.

### Task 3: `wtree up`, `down`, and `status` lifecycle

**Files:**
- Create: `cmd/wtree/main.go`
- Create: `internal/wtree/cli.go`
- Create: `internal/wtree/app.go`
- Test: `internal/wtree/cli_test.go`
- Modify: `cmd/wtree/main.go`

**Interfaces:**
- `Run(ctx context.Context, args []string, stdout, stderr io.Writer, deps Dependencies) int` parses and executes CLI commands.
- Public syntax: `wtree up --port <1..65535> [--slug <slug>]`, `wtree down [--slug <slug>]`, and `wtree status`.
- `Dependencies` supplies Git, registry, engine, and paths so command tests use temporary directories and fakes.

- [ ] **Step 1: Write failing command tests** for help/unknown command, invalid port, derived and explicit slug, duplicate slug failure with `use --slug <value>`, repeated same-worktree failure, down by current worktree or explicit slug, idempotent down of a missing route, ambiguous current-worktree down, and sorted status output.
- [ ] **Step 2: Add certificate preflight tests** for missing files, mismatched cert/key, and a certificate that does not verify for a representative `*.wtree.dev` hostname.
- [ ] **Step 3: Run `go test ./internal/wtree -run 'TestRun|TestCertificate' -v`;** confirm failures before CLI implementation.
- [ ] **Step 4: Implement `Run` and lifecycle orchestration.** Resolve the current branch and canonical Git worktree root; require `--slug` when the worktree has no branch; validate the cert/key pair and wildcard hostname before touching routes; reject active slugs; render/validate a candidate config; start or reload Caddy; persist state only after successful activation; roll back the config if reload fails. `down` removes only the selected/current worktree route, returns an actionable error if the current worktree owns multiple routes and no slug is provided, and is safe to repeat. `status` requires no running engine.
- [ ] **Step 5: Run `go test ./...`;** confirm command, certificate, registry, renderer, and fake-engine tests pass.
- [ ] **Step 6: Run `go vet ./...`;** confirm no vet diagnostics.
- [ ] **Step 7: Commit** as `feat: implement wtree route lifecycle commands`.

### Task 4: Reproducible CLI release artifacts

**Files:**
- Create: `.github/workflows/test.yml`
- Create: `.github/workflows/release.yml`
- Create: `scripts/check-release-assets.sh`
- Create: `README.md`

**Interfaces:**
- Release archive names: `wtree_<version>_<os>_<arch>.tar.gz`, where version omits the leading `v` and OS/architecture are Go `GOOS`/`GOARCH` values. Initial shared Make module default: `WTREE_VERSION=0.1.0`.
- V1 assets: `darwin_amd64`, `darwin_arm64`, `linux_amd64`, and `linux_arm64`; publish `SHA256SUMS` with the archives on `v*` tags.

- [ ] **Step 1: Add a CI workflow** that checks formatting (`gofmt -l` must be empty), then runs `go test ./...` and `go vet ./...` on Linux and macOS.
- [ ] **Step 2: Add `scripts/check-release-assets.sh`** that checks the four platform archive names and checksum manifest format without publishing a release.
- [ ] **Step 3: Run `bash scripts/check-release-assets.sh`;** confirm it passes for all four required targets and fails when one target/archive is omitted.
- [ ] **Step 4: Implement tag-triggered cross-build and packaging** with version metadata embedded in `wtree version`; generate one SHA-256 manifest covering all four archives. Do not put TLS files or DNS credentials in artifacts.
- [ ] **Step 5: Run `go test ./...`, `go vet ./...`, and the release validation;** confirm all pass and each target cross-compiles.
- [ ] **Step 6: Document local build, versioned release assets, and the supported macOS/Linux matrix** in `README.md`.
- [ ] **Step 7: Commit** as `build: publish verified macOS and Linux binaries`.

### Task 5: Shared makefiles module, lazy binary bootstrap, and example

**Files in `github.com/darianbr/makefiles`:**
- Create: `modules/wtree/Makefile`
- Create: `modules/wtree/prerequisites.mk`
- Create: `modules/wtree/targets.mk`
- Modify: `modules/manifest.txt`
- Modify: `README.md`
- Modify: `examples/README.md`
- Create: `examples/wtree/Makefile`
- Create: `examples/wtree/README.md`
- Create: `examples/wtree/.gitignore`
- Create: `tests/wtree-module.sh`

**Interfaces:**
- Register public module targets `clean`, `install`, `help`, `up`, `down`, and `status`; private binary prerequisite targets are not registered.
- Default `WTREE_VERSION` to `0.1.0` and `WTREE_BIN` to `.makefiles/tools/wtree-$(WTREE_VERSION)` (with `.exe` reserved for the Windows v2 port).
- Support `WTREE_VERSION`, `WTREE_BIN`, `WTREE_PORT`, `WTREE_SLUG`, `WTREE_CONTAINER_ENGINE`, `WTREE_CERT_FILE`, and `WTREE_KEY_FILE` Make variables, forwarding applicable values to the CLI.
- A project includes the module using the repository’s existing `.makefiles/modules/%/Makefile` bootstrap and composes project-owned `start`/`stop` targets around `make wtree up` / `make wtree down`.

- [ ] **Step 1: Write `tests/wtree-module.sh` with fake `curl`, checksum tool, `tar`, and wtree binary.** Assert first invocation downloads only the version/platform archive, verifies its checksum, installs atomically into `.makefiles/tools/`, and calls the expected command; a second invocation reuses the cached binary; a version change selects a different path; corrupt checksum, unsupported platform, and missing prerequisites fail clearly.
- [ ] **Step 2: Run `bash tests/wtree-module.sh`;** confirm the new tests fail because the wtree module is not present.
- [ ] **Step 3: Implement the module** following Composer’s file-target prerequisite pattern: `install` provisions the pinned release binary only when absent; `up`, `down`, and `status` depend on it; use HTTPS downloads, temporary files, SHA-256 verification, and atomic rename; never download or print certificate/key material. Keep `clean` an explicit safe no-op so a generic project clean cannot tear down active routes.
- [ ] **Step 4: Add all module files to `modules/manifest.txt` and document the module target interface** in `README.md` and `examples/README.md`.
- [ ] **Step 5: Create an example consumer** showing `make start` register a chosen port/branch slug and launch a simple local dev server, and `make stop` unregister the route. Keep the dev-server command project-owned.
- [ ] **Step 6: Run `bash tests/wtree-module.sh`, `make wtree help`, `make wtree install` in the fixture, and `make wtree clean`;** confirm tests pass, help has no side effects, provisioning is lazy, and clean leaves routes/container untouched.
- [ ] **Step 7: Commit the makefiles changes** as `feat: add wtree module and lazy CLI bootstrap`.

### Task 6: Platform smoke test and onboarding documentation

**Files:**
- Modify: `README.md` in the wtree repository
- Modify: `docs/superpowers/specs/2026-11-19-wtree-design.md` only if verified behavior requires a spec correction
- Modify: `github.com/darianbr/makefiles/examples/wtree/README.md` if setup notes need clarification

- [ ] **Step 1: Document a certificate provisioning contract**: place the public certificate and matching private key at the default user-config paths (or set the documented overrides); key material must remain out of Git and images. State that renewal is handled outside wtree with Let’s Encrypt DNS-01 and Porkbun is needed only by that designated renewal process.
- [ ] **Step 2: Document one-time container setup** for Linux Docker/Podman and macOS Docker Desktop 4.34+ host networking. For Linux rootless Podman, document the one-time privileged low-port setting (`net.ipv4.ip_unprivileged_port_start=443`) needed for host-network binding; do not make `make start` invoke `sudo`. Include a clear Docker Desktop host-networking enablement check.
- [ ] **Step 3: Run the Linux smoke test with Docker**: `make start` in the example, request the generated HTTPS URL, verify certificate hostname validation, confirm the upstream content, run `make stop`, and verify the route no longer appears in `wtree status`.
- [ ] **Step 4: Repeat the smoke test with Podman on Linux** and Docker Desktop on macOS. Verify the listener is loopback-only, the upstream stays bound to host loopback, multiple worktree routes coexist, and duplicate slugs fail without replacing routes.
- [ ] **Step 5: Run final checks**: `go test ./...`, `go vet ./...`, `bash tests/wtree-module.sh`, and `make wtree help` in the example.
- [ ] **Step 6: Commit documentation and smoke-test fixes** as `docs: document wtree local setup and supported engines`.
