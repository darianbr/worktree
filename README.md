# wtree

`wtree` maps Git worktree names to HTTPS URLs such as
`https://jira-12345.wtree.dev`, proxying to a development server on the same
host. The intended DNS record for `*.wtree.dev` resolves to `127.0.0.1`.
Windows is not supported in v1.

## Platform and validation status

The CLI and release builds target Linux and macOS on amd64 and arm64. On Linux,
wtree uses Docker or Podman with host networking. It configures Caddy to bind
`127.0.0.1:443`, proxies to `127.0.0.1:<port>`, and disables automatic HTTP
redirects on port 80. The rootless Podman integration test described below has
exercised the adapter with a self-signed certificate and high ports; it is not
a full production smoke test.

macOS is **not validated or supported yet**: no real Mac test has been
performed, and route activation is disabled there to avoid exposing a listener
without a verified loopback boundary. Docker Desktop 4.34 or later and its host-networking setting are
intended prerequisites, but Docker Desktop implements host networking through
its VM and does not guarantee that the host-side listener has the same
loopback-only boundary as native Linux. Do not rely on it for LAN isolation;
verify the actual host listener and loopback upstream behavior on a real Mac
before use. Docker Desktop's Settings > Resources > Network > Enable host
networking must be enabled and applied; check the setting after restart. Podman
Machine on macOS is not validated either.

No Docker smoke test, publicly trusted certificate smoke test, or full port 443
smoke test has been run. In particular, the high-port Podman test does not
establish public trust, low-port access, or end-to-end Make onboarding.

## Certificate provisioning

Obtain a currently valid, publicly trusted `*.wtree.dev` wildcard full chain
and matching private key through the existing approved distribution channel.
Provision them before the first `up`; wtree and the Make module do not obtain
or distribute them. The defaults are under the user's configuration directory:

| Platform | Certificate | Private key |
|----------|-------------|-------------|
| Linux | `${XDG_CONFIG_HOME:-$HOME/.config}/wtree/tls/fullchain.pem` | `${XDG_CONFIG_HOME:-$HOME/.config}/wtree/tls/privkey.pem` |
| macOS | `$HOME/Library/Application Support/wtree/tls/fullchain.pem` | `$HOME/Library/Application Support/wtree/tls/privkey.pem` |

`WTREE_CONFIG_DIR` overrides the `wtree` directory, including the default
`tls/` paths, registry and generated Caddyfile. `WTREE_CERT_FILE` and
`WTREE_KEY_FILE` independently override the TLS file paths. Use readable files
with restrictive private-key permissions and keep both files out of Git,
release archives and images. They are mounted read-only. `up` checks the
matching key pair, current validity and wildcard DNS name; it does **not**
verify the certificate chain against browser trust roots. Use a valid full
chain trusted by the intended clients.

Renewal happens outside wtree: the designated trusted renewal process obtains
the certificate using Let's Encrypt DNS-01 and alone holds the restricted
Porkbun credentials. Developers need neither Porkbun credentials nor DNS API
access. Redistribute renewed files through the approved channel; wtree does not
renew or automatically reload them. Individual file bind mounts may retain old
inodes if files are replaced, so arrange a controlled container recreation and
verify the served certificate after renewal. Rotate and redistribute the
shared key if compromised.

## Container setup

Install and start a usable Linux Docker or Podman engine; the Caddy image is
`docker.io/library/caddy:2.11.9`. On Linux, wtree reuses an existing compatible
`wtree-caddy` container if found; otherwise it prefers Podman, then Docker.
Set `WTREE_CONTAINER_ENGINE` to the
desired executable to override selection. If both engines have a `wtree-caddy`
container, choose an engine and resolve the duplicate deliberately. Existing
containers must have compatible read-only config and TLS mounts; use the same
paths for all invocations or stop the routes and deliberately replace the
container.

Port 443 must be free and the engine must be allowed to bind it. For rootless
Podman on Linux, when the current host policy prevents binding port 443, an
administrator can apply this one-time privileged setting:

```sh
sudo sysctl -w net.ipv4.ip_unprivileged_port_start=443
```

This changes the host-wide threshold, allowing unprivileged processes to bind
ports 443 and above; assess the security impact first. The command applies
until reboot; an administrator must configure persistence using the host's
normal sysctl policy if required. It is not run by wtree or `make start`.
Docker or other host configurations may have different low-port requirements.

## Route lifecycle

With `wtree` on `PATH`, run the CLI from the Git worktree containing the
development server. Start the server separately and bind it to `127.0.0.1`;
wtree neither starts nor supervises it. For example:

```sh
wtree up --port 3000
wtree status
wtree down
```

`up` prints the URL and requires a port from 1 to 65535; it does not check that
the upstream is listening. A ticket in the branch name takes precedence, so
`feat/JIRA-12345-this-is-a-new-feature` becomes `jira-12345`. Otherwise the
branch is normalized and truncated to a DNS label. Use `--slug my-branch` to
override it, including on detached HEAD. Explicit slugs are lowercased and must
be valid DNS labels of at most 63 characters. Every active slug is unique:
even a repeated `up` from the same worktree fails without replacing the route.
Run `down` first, or select another slug with `--slug`.

`status` lists routes without requiring an engine. `down` removes the current
worktree's route; if it owns several routes, specify `down --slug my-branch`.
An absent route is safe to remove again. If the server exits separately, the
route remains until `down` and requests can fail with an upstream error.
Removing the last route does not stop or remove the persistent Caddy container.
There is no wtree authentication or browser-origin filtering; applications
remain responsible for their own protections.

## Local build

With Go 1.26 or newer, build the CLI with `go build -o wtree ./cmd/wtree`.
Run the checks with `go test ./...` and `go vet ./...`.

## Make integration

The wtree module lives in this checkout, not in the canonical Makefiles
repository. To bootstrap a consumer, make trusted local checkouts of Worktree
and the canonical `darianbr/makefiles` repository. Set `WTREE_CHECKOUT` to the
absolute Worktree checkout path. For a fresh cache, also set
`MAKEFILES_CHECKOUT` to the absolute canonical checkout path; its
`modules/Makefile` and `modules/core/` are the source of the shared core.
These are local paths, not URLs: the bootstrap does not clone, download or
authenticate either checkout. Review and pin the checkout revisions according
to your organization's policy.

Add this to the consumer's Makefile (recipe lines require tabs):

```make
export WTREE_CHECKOUT MAKEFILES_CHECKOUT
include .makefiles/modules/wtree/Makefile

.makefiles/modules/wtree/Makefile:
	@test -n "$$WTREE_CHECKOUT" || { echo 'WTREE_CHECKOUT is required' >&2; exit 1; }
	@bash "$$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"
```

For example, export `WTREE_CHECKOUT=/path/to/worktree` and
`MAKEFILES_CHECKOUT=/path/to/makefiles`, then run `make wtree help`. GNU Make
remakes the missing include on first invocation. The bootstrap copies only the
shared canonical root/core and the Worktree-owned module; it does not fetch or
install any unrelated canonical module. A consumer with an existing complete
canonical core can omit `MAKEFILES_CHECKOUT`; its core and other modules are
left untouched. The checks reject missing, incomplete, symlinked or visibly
invalid core files, but are structural checks, not a cryptographic integrity
or version-compatibility guarantee. Use a trusted, compatible core checkout.

Existing core is never silently updated. Incomplete core or a conflicting
existing wtree module fails without replacing it. An identical module is
reused on repeated bootstrap. To update a differing module or repair a partial
cache, inspect and deliberately remove or move the affected cache files first;
keep unrelated modules and user data. The bootstrap uses a temporary lock;
after an interrupted bootstrap, inspect and remove a stale
`.makefiles/modules/.wtree-bootstrap.lock` before retrying. Ignore
`.makefiles/` in the consumer's Git repository.

`make wtree help`, `install`, `up`, `down`, `status`, and `clean` are scoped
module operations. For example, `make wtree up WTREE_PORT=3000` registers a
route and `make wtree down` removes it. `WTREE_SLUG` overrides the branch-based
name; `WTREE_CONTAINER_ENGINE`, `WTREE_CERT_FILE`, and `WTREE_KEY_FILE` are
passed through. Scoped `clean` leaves active routes, the proxy and the binary
alone. The canonical aggregate `make clean`, by contrast, removes the shared
`.makefiles/` cache. The [example](examples/wtree/README.md) composes routing
with a project-owned foreground server.

`WTREE_VERSION` defaults to `0.1.0`, `WTREE_BIN` to
`.makefiles/tools/wtree-$(WTREE_VERSION)`, and `WTREE_RELEASE_URL` to the
matching GitHub release directory. `install`, `up`, `down`, and `status`
lazily download an HTTPS archive and `SHA256SUMS` when the binary is absent;
the checksum is served by the same host and is not an independent signature.
An existing non-symlink executable `WTREE_BIN` is reused without download,
reverification or Go. Help and scoped clean do not install the binary.

The default GitHub release repository is private and 0.1.0 is unreleased.
Authentication for private GitHub release assets is **unresolved**: the
installer does not configure credentials. Use a pre-provisioned binary or a
separately accessible HTTPS release mirror via `WTREE_RELEASE_URL`; neither
private release downloads nor remote module distribution are claimed to work.

To run the offline Make tests, point `MAKEFILES_CORE_GIT` at a local canonical
Makefiles Git repository containing commit
`ecb127ac02953f599c5eac7c7ca03daae787ebe2`, then run
`bash tests/make-bootstrap.sh` and `bash tests/wtree-module.sh`. The tests use
`git archive` of that exact commit, not the checkout's working files, and do
not access the network. CI explicitly checks out that pinned canonical commit
before running them; if the repository becomes private, CI needs separately
configured read access. The fixture tests do not validate real macOS route
activation, GitHub authentication, or an end-to-end TLS/container deployment.

## SELinux and Podman bind mounts

The config directory, certificate, and private key are bind-mounted read-only.
By default, wtree does not change their SELinux labels or disable container
labeling. If Podman reports `permission denied`, check ordinary filesystem
permissions and SELinux denials and arrange suitable labels manually, or, only
after reviewing the consequences, opt in with `WTREE_SELINUX_RELABEL=1` when
running wtree. This adds Podman's shared `:z` relabel option to all three
read-only bind mounts during candidate validation and container creation. Only
the exact value `1` enables it; Docker mounts are unchanged.

Podman may recursively relabel the **entire config directory** and relabel the
certificate and private key, even if validation later fails. These label changes
persist after the containers are removed, may affect other applications, and
allow appropriately labeled containers to access the files subject to normal
permissions. Do not opt in for a broad or shared directory, or for TLS files
whose labels must remain unchanged; use dedicated copies or arrange labels
manually instead. Restore labels using your host's SELinux administration
procedures if necessary. The option does not change an already running or
stopped container's existing mounts; validation in a running container uses
those mounts. Podman may relabel the sources again when restarting a container
created with `:z`, even if the option is no longer set. Therefore, wtree
requires `WTREE_SELINUX_RELABEL=1` to restart such a container. It also requires
that opt-in for older Podman containers whose relabel history is unknown;
alternatively, after stopping its routes, replace the container with one created
without the relabel option. Validation of a stopped container with the opt-in
can relabel sources before the restart.

## Rootless Podman integration test

On Linux, cache the pinned image with `podman pull docker.io/library/caddy:2.11.9`,
then run `WTREE_PODMAN_INTEGRATION=1 go test -count=1 -v -run '^TestPodmanCaddyIntegration$' ./internal/wtree`.
The test requires rootless Podman and does not pull images itself. It generates
a temporary self-signed wildcard certificate, uses randomly named containers,
temporary bind-mounted data, and high loopback ports, and cleans up its own
containers. It exercises the real adapter with test-only substitutions for the
fixed container name and data volume, and adds test-only HTTPS and admin ports
to the rendered configuration. The test opts into shared relabeling only for
its own disposable config, TLS, and data files; container labeling remains
enabled. No system trust or production container is used.

## Release builds

Version `0.1.0` is unreleased: local builds are not published assets. Before
the shared Make module can download its default version, publish a matching
`v0.1.0` release and its archives and `SHA256SUMS` at the configured HTTPS
release URL, which defaults to
`https://github.com/darianbr/worktree/releases/download/v0.1.0` for that
version. Alternatively, configure a published version and URL or supply an
existing non-symlink executable via `WTREE_BIN`. The Make module itself must
be installed from a local checkout as described above; remote module
distribution and authentication for private GitHub release assets are not yet
implemented.

Pushing a `v*` tag triggers the release workflow. The Linux release builder
requires GNU tar, gzip, and `sha256sum`. To build and validate assets locally,
run `bash scripts/build-release.sh v0.1.0`. This does not publish a release.
The build requires the CLI to expose a string variable `main.version` that
`wtree version` reports; the release build sets it to the tag without `v` and
checks the native Linux binary before packaging.

For `v0.1.0`, the generated files in `dist/` are:

- `wtree_0.1.0_darwin_amd64.tar.gz`
- `wtree_0.1.0_darwin_arm64.tar.gz`
- `wtree_0.1.0_linux_amd64.tar.gz`
- `wtree_0.1.0_linux_arm64.tar.gz`
- `SHA256SUMS`

Each archive contains only the `wtree` executable. The checksum manifest
uses the standard `sha256sum` format and covers all four archives. Run
`bash scripts/check-release-assets.sh dist 0.1.0` to validate them, or run
the script without arguments for its offline self-test. No certificates,
keys, or DNS credentials are included in release archives.
