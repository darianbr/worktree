#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || $1 != v* ]]; then
  echo "Usage: bash scripts/build-release.sh <v-prefixed-tag>" >&2
  exit 2
fi
version=${1#v}
if [[ ! $version =~ ^[0-9][0-9A-Za-z.+-]*$ ]]; then
  echo "invalid release version: $version" >&2
  exit 2
fi
if [[ ! -f cmd/wtree/main.go ]]; then
  echo "missing cmd/wtree/main.go: implement the CLI before building a release" >&2
  exit 1
fi

# GNU tar and gzip -n remove archive timestamps and host-specific metadata.
if ! tar --version | grep -q 'GNU tar'; then
  echo "release packaging requires GNU tar" >&2
  exit 1
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p dist
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  GOOS=${target%_*} GOARCH=${target#*_} CGO_ENABLED=0 \
    go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$version" \
    -o "$tmp/wtree" ./cmd/wtree
  chmod 0755 "$tmp/wtree"
  if [[ $target == linux_amd64 && $(uname -s) == Linux && $(uname -m) == x86_64 ]]; then
    output=$("$tmp/wtree" version)
    if [[ $output != *"$version"* ]]; then
      echo "wtree version does not report release version $version: $output" >&2
      exit 1
    fi
  fi
  tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner --format=ustar \
    -C "$tmp" -cf - wtree | gzip -n > "dist/wtree_${version}_${target}.tar.gz"
done

(cd dist && sha256sum \
  "wtree_${version}_darwin_amd64.tar.gz" \
  "wtree_${version}_darwin_arm64.tar.gz" \
  "wtree_${version}_linux_amd64.tar.gz" \
  "wtree_${version}_linux_arm64.tar.gz" > SHA256SUMS)
bash scripts/check-release-assets.sh dist "$version"