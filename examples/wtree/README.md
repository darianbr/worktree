# wtree Make example

This example registers an HTTPS route and starts a foreground Python HTTP
server bound to loopback. Run it in a Git worktree. See the [main README](../../README.md)
for certificate provisioning, Linux container setup, security and platform limits.

Set `WTREE_CHECKOUT` to the absolute path of a trusted local Worktree checkout.
For a fresh consumer also set `MAKEFILES_CHECKOUT` to an absolute path of a
trusted local canonical Makefiles checkout containing `modules/Makefile` and
`modules/core/`. The include rule bootstraps on the first `make` invocation;
existing complete canonical core files are reused without a checkout. Neither
checkout is fetched automatically. See [Make integration](../../README.md#make-integration)
for a copyable consumer recipe and cache conflict handling.

Set `WTREE_BIN` to an existing non-symlink executable to use it without Go or
network access. Otherwise the lazy installer needs curl, tar, `uname`, `awk`,
`mktemp`, standard file utilities, and `sha256sum` or `shasum`. Version 0.1.0
is unreleased. The default GitHub release repository is private; private asset
authentication and remote module distribution are unresolved. A public or
authenticated HTTPS mirror must be configured separately. Checksums come from
the same host, not an independent signature.

After provisioning TLS and a Linux container engine, run `make start` to
register port 8000 and start Python 3. Override with `make start WTREE_PORT=3000
WTREE_SLUG=my-branch`. The port must be free; the CLI does not check upstream
reachability. Run `make wtree status` to see routes, and `make stop` (with the
same slug, if supplied) to remove the route. Stop Python separately with
Ctrl-C. If it exits unexpectedly, remove the stale route with `make stop`.
`make wtree clean` leaves active routes and the proxy unchanged.

macOS route activation remains disabled and unvalidated; Docker, trusted
certificate and full port 443 end-to-end smoke tests remain outstanding.
Do not commit the generated `.makefiles/` directory or TLS material.