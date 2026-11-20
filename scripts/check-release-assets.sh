#!/usr/bin/env bash
set -euo pipefail

validate() {
  local dir=$1 version=$2 target name line entry seen=' ' count=0
  local -a expected=(darwin_amd64 darwin_arm64 linux_amd64 linux_arm64)

  [[ $version =~ ^[0-9][0-9A-Za-z.+-]*$ ]] || { echo "invalid version: $version" >&2; return 1; }
  [[ -f $dir/SHA256SUMS ]] || { echo "missing SHA256SUMS" >&2; return 1; }

  for target in "${expected[@]}"; do
    name="wtree_${version}_${target}.tar.gz"
    [[ -f $dir/$name ]] || { echo "missing archive: $name" >&2; return 1; }
    entry=$(tar -tzf "$dir/$name") || return 1
    [[ $entry == wtree ]] || { echo "unexpected archive contents: $name" >&2; return 1; }
  done

  while IFS= read -r line || [[ -n $line ]]; do
    if [[ ! $line =~ ^([0-9a-f]{64})\ \ (wtree_[0-9A-Za-z.+-]+_(darwin|linux)_(amd64|arm64)\.tar\.gz)$ ]]; then
      echo "invalid checksum line: $line" >&2; return 1
    fi
    name=${BASH_REMATCH[2]}
    [[ $name == "wtree_${version}_"* && $seen != *" $name "* ]] || {
      echo "unexpected or duplicate checksum entry: $name" >&2; return 1;
    }
    seen+="$name "
    count=$((count + 1))
  done < "$dir/SHA256SUMS"

  for target in "${expected[@]}"; do
    name="wtree_${version}_${target}.tar.gz"
    [[ $seen == *" $name "* ]] || { echo "missing checksum: $name" >&2; return 1; }
  done
  [[ $count -eq 4 ]] || { echo "unexpected checksum entries" >&2; return 1; }

  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$dir" && sha256sum -c SHA256SUMS)
  else
    (cd "$dir" && shasum -a 256 -c SHA256SUMS)
  fi
}

if [[ $# -eq 0 ]]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  printf '#!/bin/sh\n' > "$tmp/wtree"
  for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
    tar -czf "$tmp/wtree_0.1.0_${target}.tar.gz" -C "$tmp" wtree
  done
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmp" && sha256sum wtree_0.1.0_*.tar.gz > SHA256SUMS)
  else
    (cd "$tmp" && shasum -a 256 wtree_0.1.0_*.tar.gz > SHA256SUMS)
  fi
  validate "$tmp" 0.1.0
  cp "$tmp/SHA256SUMS" "$tmp/SHA256SUMS.saved"
  sed '1d' "$tmp/SHA256SUMS.saved" > "$tmp/SHA256SUMS"
  if validate "$tmp" 0.1.0 >/dev/null 2>&1; then
    echo "validator accepted a missing checksum" >&2; exit 1
  fi
  printf 'not a checksum\n' > "$tmp/SHA256SUMS"
  if validate "$tmp" 0.1.0 >/dev/null 2>&1; then
    echo "validator accepted a malformed manifest" >&2; exit 1
  fi
  cp "$tmp/SHA256SUMS.saved" "$tmp/SHA256SUMS"
  printf '#!/bin/sh\n# modified\n' > "$tmp/wtree"
  tar -czf "$tmp/wtree_0.1.0_linux_arm64.tar.gz" -C "$tmp" wtree
  if validate "$tmp" 0.1.0 >/dev/null 2>&1; then
    echo "validator accepted a corrupted archive" >&2; exit 1
  fi
  rm "$tmp/wtree_0.1.0_linux_arm64.tar.gz"
  if validate "$tmp" 0.1.0 >/dev/null 2>&1; then
    echo "validator accepted a missing target" >&2; exit 1
  fi
  echo "Release asset validator self-test passed (missing target/checksum, malformed manifest, corrupted archive)."
elif [[ $# -eq 2 ]]; then
  validate "$1" "$2"
else
  echo "Usage: bash scripts/check-release-assets.sh [<asset-directory> <version>]" >&2
  exit 2
fi