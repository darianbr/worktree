#!/usr/bin/env bash
set -euo pipefail
unset WTREE_BIN WTREE_VERSION WTREE_PORT WTREE_SLUG WTREE_CHECKOUT MAKEFILES_CHECKOUT

repo=$(cd "$(dirname "$0")/.." && pwd -P)
source_repo=${MAKEFILES_CORE_GIT:?set MAKEFILES_CORE_GIT to a local canonical Makefiles Git checkout}
core_revision=ecb127ac02953f599c5eac7c7ca03daae787ebe2
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/canonical" "$tmp/fresh" "$tmp/existing" "$tmp/bin" "$tmp/consumer with spaces"
git -C "$source_repo" archive "$core_revision" modules/Makefile modules/core modules/npm | tar -xf - -C "$tmp/canonical"
cat > "$tmp/bin/wtree" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CLI_LOG"
EOF
cat > "$tmp/bin/curl" <<'EOF'
#!/bin/sh
echo 'unexpected network access' >&2
exit 99
EOF
cat > "$tmp/bin/go" <<'EOF'
#!/bin/sh
echo 'unexpected Go invocation' >&2
exit 99
EOF
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" CLI_LOG="$tmp/cli.log"
export WTREE_CHECKOUT="$repo" MAKEFILES_CHECKOUT="$tmp/canonical" WTREE_BIN="$tmp/bin/wtree"
fail() { echo "FAIL: $*" >&2; exit 1; }
run() { "$@" >"$tmp/out" 2>&1 || { cat "$tmp/out" >&2; fail "$*"; }; }
fails() { if "$@" >"$tmp/out" 2>&1; then fail "expected failure: $*"; fi; }
contains() { grep -F -- "$2" "$1" >/dev/null || { cat "$1" >&2; fail "missing $2"; }; }

# Observe every destination write and deliberately copy the recursive module's
# entry first. This exposes partial publication without relying on scheduling.
mkdir "$tmp/observe-bin" "$tmp/order-fresh" "$tmp/order-existing-core"
export REAL_CP=$(command -v cp) REAL_LN=$(command -v ln) ORDER_CANONICAL="$tmp/canonical/modules" ORDER_MODULE="$repo/make/modules/wtree"
cat > "$tmp/observe-bin/check-publication" <<'EOF'
#!/usr/bin/env bash
set -eu
root=.makefiles/modules
if [[ -e "$root/Makefile" ]]; then
  cmp -s "$ORDER_CANONICAL/Makefile" "$root/Makefile" &&
    diff -qr "$ORDER_CANONICAL/core" "$root/core" >/dev/null || {
      echo 'root entry published before complete core' >&2; exit 1;
    }
  echo root >> "$ORDER_LOG"
fi
if [[ -e "$root/wtree/Makefile" ]]; then
  for file in Makefile prerequisites.mk targets.mk; do
    cmp -s "$ORDER_MODULE/$file" "$root/wtree/$file" || {
      echo 'module entry published before complete fragments' >&2; exit 1;
    }
  done
  echo module >> "$ORDER_LOG"
fi
EOF
cat > "$tmp/observe-bin/cp" <<'EOF'
#!/usr/bin/env bash
set -eu
source=${@: -2:1} target=${@: -1}
if [[ "${RACE_CORE_FILE:-}" == yes && "$target" == .makefiles/modules/core/ ]]; then
  printf 'racing core writer\n' > "$target/clean.mk"
fi
if [[ "${FAIL_COPY:-}" == core && "$target" == .makefiles/modules/core/ ]]; then
  "$REAL_CP" "$source/clean.mk" "$target"
  echo 'injected core copy failure' >&2; exit 1
fi
if [[ "${FAIL_COPY:-}" == module && "$source" == */wtree/targets.mk && "$target" == .makefiles/modules/wtree/* ]]; then
  echo 'injected module copy failure' >&2; exit 1
fi
if [[ "$source" == */wtree/. && "$target" == .makefiles/modules/wtree/ ]]; then
  for file in Makefile prerequisites.mk targets.mk; do
    "$REAL_CP" "$source/$file" "$target"
    check-publication
  done
else
  "$REAL_CP" "$@"
  check-publication
fi
EOF
cat > "$tmp/observe-bin/ln" <<'EOF'
#!/usr/bin/env bash
set -eu
source=${@: -2:1} target=${@: -1}
if [[ "${FAIL_COPY:-}" == module && "$source" == */wtree/targets.mk && "$target" == .makefiles/modules/wtree/* ]]; then
  echo 'injected module copy failure' >&2; exit 1
fi
if [[ "${RACE_ENTRY:-}" == "$target" ]]; then
  printf 'racing writer\n' > "$target"
fi
"$REAL_LN" "$@"
check-publication
EOF
chmod +x "$tmp/observe-bin/"*
export ORDER_LOG="$tmp/order.log"
export MAKEFILES_CHECKOUT="$tmp/canonical"
# Exercise module publication independently with an already installed core.
mkdir -p "$tmp/order-existing-core/.makefiles/modules"
cp -R "$tmp/canonical/modules/core" "$tmp/order-existing-core/.makefiles/modules/"
cp "$tmp/canonical/modules/Makefile" "$tmp/order-existing-core/.makefiles/modules/Makefile"
: > "$ORDER_LOG"
run env PATH="$tmp/observe-bin:$PATH" bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$tmp/order-existing-core"
contains "$ORDER_LOG" module

run env PATH="$tmp/observe-bin:$PATH" bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$tmp/order-fresh"
contains "$ORDER_LOG" root
contains "$ORDER_LOG" module

# A failed copy must not leave the corresponding entry point published, and
# the lock must be removed even when installation aborts partway through.
for stage in core module; do
  dir="$tmp/fail-$stage"; mkdir "$dir"
  fails env PATH="$tmp/observe-bin:$PATH" FAIL_COPY="$stage" bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$dir"
  contains "$tmp/out" "injected $stage copy failure"
  [[ ! -e "$dir/.makefiles/modules/.wtree-bootstrap.lock" ]] || fail "$stage failure left lock"
  if [[ "$stage" == core ]]; then
    [[ -f "$dir/.makefiles/modules/core/clean.mk" ]] || fail 'core failure did not leave a partial copy'
    [[ ! -e "$dir/.makefiles/modules/Makefile" ]] || fail 'core failure published root'
  else
    [[ -f "$dir/.makefiles/modules/wtree/prerequisites.mk" ]] || fail 'module failure did not leave a partial copy'
    [[ ! -e "$dir/.makefiles/modules/wtree/Makefile" ]] || fail 'module failure published entry'
  fi
  cp -R "$dir/.makefiles/modules" "$tmp/before-failed-$stage"
  fails bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$dir"
  diff -r "$tmp/before-failed-$stage" "$dir/.makefiles/modules" || fail "$stage retry modified partial cache"
done

# Even a writer that appears inside the newly created core directory must not
# have its file overwritten or cause an incomplete core to be published.
mkdir "$tmp/race-core-file"
fails env PATH="$tmp/observe-bin:$PATH" RACE_CORE_FILE=yes bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$tmp/race-core-file"
[[ $(cat "$tmp/race-core-file/.makefiles/modules/core/clean.mk") == 'racing core writer' ]] || fail 'racing core file overwritten'
[[ ! -e "$tmp/race-core-file/.makefiles/modules/Makefile" ]] || fail 'core conflict published root'
[[ ! -e "$tmp/race-core-file/.makefiles/modules/.wtree-bootstrap.lock" ]] || fail 'core conflict left lock'

# A competing writer at a fragment or final publication must never be replaced.
for entry in Makefile wtree/prerequisites.mk wtree/Makefile; do
  dir="$tmp/race-${entry//\//-}"; mkdir "$dir"
  fails env PATH="$tmp/observe-bin:$PATH" RACE_ENTRY=".makefiles/modules/$entry" bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$dir"
  [[ $(cat "$dir/.makefiles/modules/$entry") == 'racing writer' ]] || fail "$entry was overwritten"
  [[ ! -e "$dir/.makefiles/modules/.wtree-bootstrap.lock" ]] || fail "$entry race left lock"
  if [[ "$entry" == wtree/prerequisites.mk ]]; then
    [[ ! -e "$dir/.makefiles/modules/wtree/Makefile" ]] || fail 'fragment race published module entry'
  fi
done

# A genuine first Make invocation bootstraps only the core and wtree module.
cat > "$tmp/fresh/Makefile" <<'EOF'
export WTREE_CHECKOUT MAKEFILES_CHECKOUT
include .makefiles/modules/wtree/Makefile
.makefiles/modules/wtree/Makefile:
	@test -n "$$WTREE_CHECKOUT" || { echo 'WTREE_CHECKOUT is required' >&2; exit 1; }
	@bash "$$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"
EOF
run make -C "$tmp/fresh" --no-print-directory wtree status
contains "$CLI_LOG" status
[[ -f "$tmp/fresh/.makefiles/modules/core/registry.mk" ]] || fail 'canonical core missing'
[[ ! -e "$tmp/fresh/.makefiles/modules/npm" ]] || fail 'unrelated module provisioned'
diff -r "$tmp/canonical/modules/core" "$tmp/fresh/.makefiles/modules/core" || fail 'core differs from canonical snapshot'
cmp "$tmp/canonical/modules/Makefile" "$tmp/fresh/.makefiles/modules/Makefile" || fail 'root differs from canonical snapshot'
run bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$tmp/fresh"
run make -C "$tmp/fresh" --no-print-directory wtree help
contains "$tmp/out" 'Available targets: clean down help install status up'

# A consumer path containing spaces must bootstrap from a completely empty cache.
cp "$tmp/fresh/Makefile" "$tmp/consumer with spaces/Makefile"
run make -C "$tmp/consumer with spaces" --no-print-directory wtree status
[[ -f "$tmp/consumer with spaces/.makefiles/modules/wtree/targets.mk" ]] || fail 'consumer did not bootstrap'

# Existing canonical core and module data must survive bootstrap unchanged.
mkdir -p "$tmp/existing/.makefiles/modules"
cp -R "$tmp/canonical/modules/." "$tmp/existing/.makefiles/modules/"
printf 'user data\n' > "$tmp/existing/.makefiles/modules/npm/user-file"
cp -R "$tmp/existing/.makefiles/modules" "$tmp/before"
{ printf '%s\n' 'include .makefiles/modules/npm/Makefile'; cat "$tmp/fresh/Makefile"; } > "$tmp/existing/Makefile"
unset MAKEFILES_CHECKOUT
run make -C "$tmp/existing" --no-print-directory wtree status
run make -C "$tmp/existing" --no-print-directory npm help
contains "$tmp/out" 'Available targets: clean help install'
diff -r "$tmp/before/core" "$tmp/existing/.makefiles/modules/core" || fail 'existing core modified'
diff -r "$tmp/before/npm" "$tmp/existing/.makefiles/modules/npm" || fail 'existing module modified'
cmp "$tmp/before/Makefile" "$tmp/existing/.makefiles/modules/Makefile" || fail 'existing root modified'
printf 'module user data\n' > "$tmp/existing/.makefiles/modules/wtree/user-file"
cp -R "$tmp/existing/.makefiles/modules/wtree" "$tmp/before-wtree"
run bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$tmp/existing"
diff -r "$tmp/before-wtree" "$tmp/existing/.makefiles/modules/wtree" || fail 'existing wtree module modified'

# A complete but different wtree module, and a pre-existing lock, are left alone.
mkdir -p "$tmp/different-module/.makefiles/modules" "$tmp/locked/.makefiles/modules/.wtree-bootstrap.lock"
cp -R "$tmp/existing/.makefiles/modules/wtree" "$tmp/different-module/.makefiles/modules/"
printf 'custom fragment\n' > "$tmp/different-module/.makefiles/modules/wtree/targets.mk"
cp -R "$tmp/different-module/.makefiles/modules" "$tmp/before-different-module"
printf 'lock owner\n' > "$tmp/locked/.makefiles/modules/.wtree-bootstrap.lock/owner"
export MAKEFILES_CHECKOUT="$tmp/canonical"
fails bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$tmp/different-module"
contains "$tmp/out" 'refusing to overwrite'
diff -r "$tmp/before-different-module" "$tmp/different-module/.makefiles/modules" || fail 'different module modified'
fails bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$tmp/locked"
contains "$tmp/out" 'stale lock'
[[ $(cat "$tmp/locked/.makefiles/modules/.wtree-bootstrap.lock/owner") == 'lock owner' ]] || fail 'existing lock modified'

# A core damaged after installation must not run even an aggregate CLI target.
mkdir -p "$tmp/damaged"
cp -R "$tmp/fresh/.makefiles" "$tmp/damaged/.makefiles"
cp "$tmp/fresh/Makefile" "$tmp/damaged/Makefile"
rm "$tmp/damaged/.makefiles/modules/core/dispatch.mk"
before_calls=$(wc -l < "$CLI_LOG")
fails make -C "$tmp/damaged" --no-print-directory status
[[ $(wc -l < "$CLI_LOG") -eq $before_calls ]] || fail 'damaged core ran the CLI'

# A missing or malformed source and partial existing caches fail without writes.
for case_name in no-source bad-source incomplete-source partial-core empty-core invalid-core conflict; do
  dir="$tmp/$case_name"; mkdir -p "$dir/.makefiles/modules"
  case "$case_name" in
    no-source) unset MAKEFILES_CHECKOUT ;;
    bad-source) export MAKEFILES_CHECKOUT="$tmp/missing" ;;
    incomplete-source) mkdir -p "$tmp/incomplete/modules/core"; echo broken > "$tmp/incomplete/modules/Makefile"; export MAKEFILES_CHECKOUT="$tmp/incomplete" ;;
    partial-core) export MAKEFILES_CHECKOUT="$tmp/canonical"; mkdir "$dir/.makefiles/modules/core"; echo sentinel > "$dir/.makefiles/modules/core/paths.mk" ;;
    empty-core) export MAKEFILES_CHECKOUT="$tmp/canonical"; cp -R "$tmp/canonical/modules/core" "$dir/.makefiles/modules/"; cp "$tmp/canonical/modules/Makefile" "$dir/.makefiles/modules/Makefile"; : > "$dir/.makefiles/modules/core/registry.mk" ;;
    invalid-core) export MAKEFILES_CHECKOUT="$tmp/canonical"; cp -R "$tmp/canonical/modules/core" "$dir/.makefiles/modules/"; cp "$tmp/canonical/modules/Makefile" "$dir/.makefiles/modules/Makefile"; echo '# mf_register_module' > "$dir/.makefiles/modules/core/registry.mk" ;;
    conflict) export MAKEFILES_CHECKOUT="$tmp/canonical"; mkdir "$dir/.makefiles/modules/wtree"; echo sentinel > "$dir/.makefiles/modules/wtree/Makefile" ;;
  esac
  cp -R "$dir/.makefiles/modules" "$tmp/before-$case_name"
  fails bash -c 'cd "$1" && bash "$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"' _ "$dir"
  diff -r "$tmp/before-$case_name" "$dir/.makefiles/modules" || fail "$case_name changed the cache"
done

# A real first Make invocation reports a missing source instead of ignoring it.
unset MAKEFILES_CHECKOUT
mkdir "$tmp/missing-first-run"
cp "$tmp/fresh/Makefile" "$tmp/missing-first-run/Makefile"
fails make -C "$tmp/missing-first-run" --no-print-directory wtree status
contains "$tmp/out" 'MAKEFILES_CHECKOUT'
[[ ! -e "$tmp/missing-first-run/.makefiles" ]] || fail 'failed first run created a cache'

echo 'make bootstrap tests passed'