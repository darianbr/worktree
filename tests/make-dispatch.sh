#!/usr/bin/env bash
set -euo pipefail
unset MAKEFLAGS GNUMAKEFLAGS MF_LEGACY_GOALS MF_SCOPED_MODULE WTREE_BIN WTREE_VERSION WTREE_PORT WTREE_SLUG

repo=$(cd "$(dirname "$0")/.." && pwd -P)
source_repo=${MAKEFILES_CORE_GIT:?set MAKEFILES_CORE_GIT to a local canonical Makefiles Git checkout}
revision=ecb127ac02953f599c5eac7c7ca03daae787ebe2
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/project/.makefiles/modules" "$tmp/bin"
git -C "$source_repo" archive "$revision" modules/Makefile modules/core | tar -xf - -C "$tmp/project/.makefiles"
cp -R "$repo/make/modules/wtree" "$tmp/project/.makefiles/modules/"
mkdir "$tmp/project/.makefiles/modules/other"
cat > "$tmp/project/.makefiles/modules/other/Makefile" <<'EOF'
-include .makefiles/modules/Makefile
MF_CURRENT_MODULE_ENTRY := $(lastword $(filter %/modules/other/Makefile,$(MAKEFILE_LIST)))
$(eval $(call mf_register_module,$(MF_CURRENT_MODULE_ENTRY),clean install help status))
.PHONY: status other-prerequisite
status:: other-prerequisite
	@echo other-status >> "$$DISPATCH_LOG"
other-prerequisite:
	@echo other-prerequisite >> "$$DISPATCH_LOG"
clean::
	@echo other-clean >> "$$DISPATCH_LOG"
EOF
cat > "$tmp/bin/wtree" <<'EOF'
#!/bin/sh
printf 'wtree-%s\n' "$*" >> "$DISPATCH_LOG"
EOF
cat > "$tmp/bin/true" <<'EOF'
#!/bin/sh
echo 'untrusted true executed' >> "$DISPATCH_LOG"
exit 99
EOF
chmod +x "$tmp/bin/wtree" "$tmp/bin/true"
export PATH="$tmp/bin:$PATH" WTREE_BIN="$tmp/bin/wtree" DISPATCH_LOG="$tmp/dispatch.log"
cd "$tmp/project"
fail() { echo "FAIL: $*" >&2; exit 1; }
run() { make --no-print-directory "$@" > "$tmp/out" 2>&1 || { cat "$tmp/out" >&2; fail "make $*"; }; }
database() {
  local result=0
  make --no-print-directory -qp "$@" > "$tmp/database" 2>&1 || result=$?
  [[ $result -le 1 ]] || { cat "$tmp/database" >&2; fail "make database $*"; }
}
normal_database() {
  # GNU Make 4.4.1 can loop in -q with multiple double-colon goals.
  make --no-print-directory -np "$@" > "$tmp/database" 2>&1 || {
    tail -40 "$tmp/database" >&2; fail "make database $*"
  }
}
assert_log() {
  printf '%s' "$1" > "$tmp/expected"
  diff -u "$tmp/expected" "$DISPATCH_LOG" || fail "$2"
}

[[ -x /usr/bin/true ]] || fail '/usr/bin/true is not executable'
for order in other-first wtree-first; do
  if [[ $order == other-first ]]; then
    printf '%s\n' 'include .makefiles/modules/other/Makefile' 'include .makefiles/modules/wtree/Makefile' > Makefile
  else
    printf '%s\n' 'include .makefiles/modules/wtree/Makefile' 'include .makefiles/modules/other/Makefile' > Makefile
  fi

  # -q prevents execution while -p exposes the outer target-specific shell.
  for module in wtree other; do
    database "$module" status
    grep -Fx 'status: SHELL := /usr/bin/true' "$tmp/database" >/dev/null || {
      grep '^status:.*SHELL' "$tmp/database" >&2 || true
      fail "$order: $module status did not select /usr/bin/true"
    }
  done
  for mode in aggregate legacy multiple; do
    case "$mode" in
      aggregate) database status ;;
      legacy) normal_database MF_LEGACY_GOALS=1 status help ;;
      multiple) normal_database status help status ;;
    esac
    if grep -E '^status: (override )?SHELL[[:space:]]*[:?+]?=' "$tmp/database" >/dev/null; then
      fail "$order: $mode unexpectedly assigned a target-specific shell"
    fi
  done

  : > "$DISPATCH_LOG"
  run wtree status
  assert_log $'wtree-status\n' "$order: scoped wtree ran an aggregate recipe or ran twice"

  : > "$DISPATCH_LOG"
  run other status
  assert_log $'other-prerequisite\nother-status\n' "$order: scoped other ran an aggregate recipe or ran twice"

  : > "$DISPATCH_LOG"
  run -j4 wtree status
  assert_log $'wtree-status\n' "$order: parallel scoped wtree ran an aggregate recipe or ran twice"

  : > "$DISPATCH_LOG"
  run -j4 other status
  assert_log $'other-prerequisite\nother-status\n' "$order: parallel scoped other ran an aggregate recipe or ran twice"

  : > "$DISPATCH_LOG"
  run status
  if [[ $order == other-first ]]; then
    assert_log $'other-prerequisite\nother-status\nwtree-status\n' "$order: aggregate status changed"
  else
    assert_log $'wtree-status\nother-prerequisite\nother-status\n' "$order: aggregate status changed"
  fi

  : > "$DISPATCH_LOG"
  run MF_LEGACY_GOALS=1 status help
  grep -F 'makefiles help' "$tmp/out" >/dev/null || fail "$order: legacy help did not run"
  [[ $(grep -c '^wtree-status$' "$DISPATCH_LOG") -eq 1 && $(grep -c '^other-status$' "$DISPATCH_LOG") -eq 1 ]] || fail "$order: legacy status did not run both modules once"

  : > "$DISPATCH_LOG"
  run status help status
  [[ $(grep -c '^wtree-status$' "$DISPATCH_LOG") -eq 1 && $(grep -c '^other-status$' "$DISPATCH_LOG") -eq 1 ]] || fail "$order: multiple goals duplicated status"
  echo "dispatch tests passed: $order"
done