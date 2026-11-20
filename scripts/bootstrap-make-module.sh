#!/usr/bin/env bash
# Run from the consumer root; both inputs are trusted local checkouts.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd -P)
module="$repo/make/modules/wtree"
dest=.makefiles/modules
core_files=(clean.mk discover-common.sh dispatch.mk help.mk paths.mk registry.mk remove-with-guardrails.sh)
module_files=(Makefile prerequisites.mk targets.mk)
die() { printf '[wtree bootstrap] %s\n' "$*" >&2; exit 1; }
regular() { [[ -f "$1" && ! -L "$1" && -s "$1" ]]; }
valid_core() {
  local root=$1 file
  [[ -d "$root/core" && ! -L "$root/core" ]] || return 1
  regular "$root/Makefile" || return 1
  for file in "${core_files[@]}"; do regular "$root/core/$file" || return 1; done
  grep -Eq '^ifndef[[:space:]]+MF_ROOT_INCLUDED([[:space:]]|$)' "$root/Makefile" &&
    grep -Eq '^MF_TOOLS_ROOT[[:space:]]*:=' "$root/core/paths.mk" &&
    grep -Eq '^define[[:space:]]+mf_register_module([[:space:]]|$)' "$root/core/registry.mk"
}
for file in "${module_files[@]}"; do
  regular "$module/$file" || die "missing module source: $module/$file"
done
for path in .makefiles "$dest"; do
  [[ ! -L "$path" ]] || die "refusing symlink cache directory: $path"
  [[ ! -e "$path" || -d "$path" ]] || die "not a cache directory: $path"
done

have_core=0
if [[ -e "$dest/Makefile" || -L "$dest/Makefile" || -e "$dest/core" || -L "$dest/core" ]]; then
  valid_core "$dest" || die 'existing Makefiles core is incomplete or invalid; no files changed'
  have_core=1
else
  [[ -n "${MAKEFILES_CHECKOUT:-}" ]] || die 'MAKEFILES_CHECKOUT must name a local canonical Makefiles checkout when core is absent'
  valid_core "$MAKEFILES_CHECKOUT/modules" || die 'MAKEFILES_CHECKOUT does not contain a complete canonical core'
fi

have_module=0
if [[ -e "$dest/wtree" || -L "$dest/wtree" ]]; then
  [[ -d "$dest/wtree" && ! -L "$dest/wtree" ]] || die 'existing wtree module is not a directory'
  for file in "${module_files[@]}"; do
    regular "$dest/wtree/$file" && cmp -s "$module/$file" "$dest/wtree/$file" ||
      die 'existing wtree module differs or is incomplete; refusing to overwrite it'
  done
  have_module=1
fi

mkdir -p "$dest"
lock="$dest/.wtree-bootstrap.lock"
mkdir "$lock" 2>/dev/null || die 'another bootstrap is running or a stale lock exists'
trap 'rm -rf "$lock"' EXIT
# Recheck after taking the lock; never replace files introduced meanwhile.
if (( ! have_core )); then
  [[ ! -e "$dest/Makefile" && ! -L "$dest/Makefile" && ! -e "$dest/core" && ! -L "$dest/core" ]] || die 'core appeared during bootstrap; retry'
  cp -R "$MAKEFILES_CHECKOUT/modules/core" "$lock/core"
  cp "$MAKEFILES_CHECKOUT/modules/Makefile" "$lock/Makefile"
  valid_core "$lock" || die 'core changed while copying'
fi
if (( ! have_module )); then
  [[ ! -e "$dest/wtree" && ! -L "$dest/wtree" ]] || die 'wtree module appeared during bootstrap; retry'
  cp -R "$module" "$lock/wtree"
  for file in "${module_files[@]}"; do
    regular "$lock/wtree/$file" && cmp -s "$module/$file" "$lock/wtree/$file" ||
      die 'module changed while copying'
  done
fi
if (( ! have_core )); then
  # Install all dependencies before exposing the root entry point.
  mkdir "$dest/core" || die 'cannot install core without overwriting'
  cp -R -n "$lock/core/." "$dest/core/"
  diff -qr "$lock/core" "$dest/core" >/dev/null || die 'core changed while installing; refusing to publish root'
  ln "$lock/Makefile" "$dest/Makefile" || die 'cannot install root without overwriting'
fi
if (( ! have_module )); then
  mkdir "$dest/wtree" || die 'cannot install module without overwriting'
  for file in prerequisites.mk targets.mk; do
    ln "$lock/wtree/$file" "$dest/wtree/$file" || die "cannot install $file without overwriting"
  done
  ln "$lock/wtree/Makefile" "$dest/wtree/Makefile" || die 'cannot install module entry without overwriting'
fi
echo '[wtree bootstrap] module ready'