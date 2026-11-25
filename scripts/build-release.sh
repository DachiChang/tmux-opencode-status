#!/usr/bin/env bash
# Build and package macOS arm64 and amd64 release assets in $DIST_DIR:
#   tmux-opencode-status_<version>_darwin_<arch>.tar.gz (one tmux-opencode-status binary)
#   SHA256SUMS (shasum -a 256 format, used by the TPM entry script)
# Usage: scripts/build-release.sh [version] (defaults to the VERSION file)
# Environment: DIST_DIR defaults to ./dist; ARCHS defaults to "arm64 amd64".
# Reproducible builds use CGO_ENABLED=0, -trimpath, no build ID, and fixed archive
# metadata from tools/pack. The same source and Go version should produce
# byte-for-byte identical assets. The Go version is pinned in go.mod.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME=tmux-opencode-status
VERSION="${1:-$(tr -d '[:space:]' < "$ROOT/VERSION")}"
DIST="${DIST_DIR:-$ROOT/dist}"
ARCHS="${ARCHS:-arm64 amd64}"

vre='^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$'
[[ "$VERSION" =~ $vre ]] || { echo "build-release: invalid version: $VERSION" >&2; exit 2; }
command -v go >/dev/null || { echo "build-release: Go is required to build releases, not to install them" >&2; exit 1; }

mkdir -p "$DIST"
DIST="$(cd "$DIST" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cd "$ROOT"
go build -trimpath -o "$work/pack" ./tools/pack

assets=()
for arch in $ARCHS; do
  case "$arch" in arm64) want="arm64" ;; amd64) want="x86_64" ;; *) echo "build-release: unsupported architecture: $arch" >&2; exit 2 ;; esac
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" \
    -o "$work/$arch-$NAME" ./cmd/tmux-opencode-status
  if command -v file >/dev/null && ! file "$work/$arch-$NAME" | grep -q "Mach-O.*$want"; then
    echo "build-release: expected a darwin/$want binary: $(file "$work/$arch-$NAME")" >&2
    exit 1
  fi
  asset="${NAME}_${VERSION}_darwin_${arch}.tar.gz"
  "$work/pack" -in "$work/$arch-$NAME" -out "$DIST/$asset" -name "$NAME"
  assets+=("$asset")
done

( cd "$DIST" && rm -f SHA256SUMS && shasum -a 256 "${assets[@]}" > SHA256SUMS )
echo "Built ${VERSION} ($(go version)) -> ${DIST}"
cat "$DIST/SHA256SUMS"
