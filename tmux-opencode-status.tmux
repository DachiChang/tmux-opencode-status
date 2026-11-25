#!/usr/bin/env bash
# TPM entry point (TPM runs each *.tmux file in the plugin root as an executable; tmux runs it again whenever it loads or reloads its configuration).
#
# Flow: check tmux capabilities → obtain the executable → `start -show` (idempotent; reloading never creates a second instance).
# Obtaining the executable:
#   1. If @opencode-status-binary is set: use the developer's own build and do not download anything.
#   2. Otherwise pick the GitHub Release asset from VERSION and uname -m, download it, verify it with SHA256SUMS, place it in bin/, and use it.
# Any failing step reports an explicit error (stderr and tmux display-message) and exits non-zero;
# it never silently falls back to go build and never substitutes an executable for another architecture.
# This script adds no key or mouse bindings itself; the daemon creates all bindings at startup, three in total:
#   1. Take over MouseDown1SideStatus in the root key table (clicking a sidebar group jumps to its jump target); on explicit stop, only write back the tmux default
#      `switch-client -t =` (see docs/adr/0002-click-to-jump.md).
#   2. One prefix trigger key (default A, configurable with @opencode-status-jump-trigger; an invalid value is not bound and does not fall back to A) → enter jump mode.
#   3. Keys a–z in the opencode-status-jump key table (pressing a letter switches to that group's jump target).
#   On explicit stop, unbind the trigger key and clear the jump key table; do not write back or restore any previous binding (see docs/adr/0005-keyboard-jump.md).
# After changing @opencode-status-jump-trigger, reload the configuration (this script runs again) to hand over to a new instance that applies the new key.
#
# Injectable (for tests and mirrors): TMUX_OPENCODE_STATUS_RELEASE_URL replaces the default Release download base URL.

NAME=tmux-opencode-status
REPO_URL="https://github.com/DachiChang/tmux-opencode-status"
CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STAGE=""
LOCKDIR=""

cleanup() {
  [ -n "$STAGE" ] && rm -rf "$STAGE"
  [ -n "$LOCKDIR" ] && rm -rf "$LOCKDIR"
}
trap cleanup EXIT

fail() {
  local msg="$NAME: $*"
  echo "$msg" >&2
  if [ -n "${TMUX:-}" ]; then
    msg="${msg//$'\n'/ }"
    tmux display-message -- "${msg//#/##}" >/dev/null 2>&1 || true
  fi
  exit 1
}

[ -n "${TMUX:-}" ] || fail "Run this inside tmux (via TPM or tmux run-shell); the default tmux server is not used"
command -v tmux >/dev/null 2>&1 || fail "Cannot find tmux"

check_tmux_capabilities() {
  local opt out missing=""
  for opt in side-status side-status-width side-status-format; do
    if ! out="$(tmux show-options -gv "$opt" 2>&1)"; then
      case "$out" in
        *"invalid option"*) missing="$missing $opt" ;;
        *) fail "Cannot verify tmux capabilities: $out" ;;
      esac
    fi
  done
  [ -z "$missing" ] || fail "tmux $(tmux -V 2>/dev/null) is missing required capabilities: $missing (requires tmux 3.9 native side-status); not starting"
}

resolve_dev_binary() {
  local bin
  bin="$(tmux show-options -gqv @opencode-status-binary 2>/dev/null)"
  [ -n "$bin" ] || return 1
  case "$bin" in /*) ;; *) fail "@opencode-status-binary must be an absolute path: $bin" ;; esac
  [ -f "$bin" ] && [ -x "$bin" ] || fail "Binary specified by @opencode-status-binary is missing or not executable: $bin (no download fallback)"
  BINARY="$bin"
}

sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }

installed_ok() {
  [ -f "$DEST/$NAME" ] && [ -x "$DEST/$NAME" ] && [ -f "$DEST/$NAME.sha256" ] &&
    [ "$(sha256_of "$DEST/$NAME")" = "$(cat "$DEST/$NAME.sha256")" ]
}

fetch() { # fetch <url> <output file>
  local proto=(--proto '=https' --proto-redir '=https')
  [ -z "${TMUX_OPENCODE_STATUS_RELEASE_URL:-}" ] || proto=()
  curl --fail --silent --show-error --location "${proto[@]}" --connect-timeout 15 --max-time 180 \
    --output "$2" "$1" 2>&1 || fail "Download failed: $1 (no local build fallback; see docs/guide.md#building-from-source)"
}

install_release() {
  local asset="${NAME}_${VERSION}_darwin_${ARCH}.tar.gz"
  local base="${TMUX_OPENCODE_STATUS_RELEASE_URL:-$REPO_URL/releases/download/v$VERSION}"
  base="${base%/}"

  mkdir -p "$CURRENT_DIR/bin" || fail "Cannot create $CURRENT_DIR/bin"
  LOCKDIR="$CURRENT_DIR/bin/.install.lock"
  local waited=0 holder
  until mkdir "$LOCKDIR" 2>/dev/null; do
    holder="$(cat "$LOCKDIR/pid" 2>/dev/null)"
    if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then
      rm -rf "$LOCKDIR"
      continue
    fi
    waited=$((waited + 1))
    if [ "$waited" -gt 240 ]; then LOCKDIR=""; fail "Timed out waiting for another installation"; fi
    sleep 0.5
  done
  echo $$ >"$LOCKDIR/pid"

  # Re-check after acquiring the lock: another load may have just finished installing.
  installed_ok && return 0

  STAGE="$(mktemp -d "$CURRENT_DIR/bin/.stage.XXXXXX")" || fail "Cannot create a temporary directory"
  fetch "$base/SHA256SUMS" "$STAGE/SHA256SUMS"
  fetch "$base/$asset" "$STAGE/$asset"

  local want got
  want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1; exit }' "$STAGE/SHA256SUMS")"
  case "$want" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) [ "${#want}" -eq 64 ] || want="" ;;
    *) want="" ;;
  esac
  [ -n "$want" ] || fail "No valid checksum for $asset in SHA256SUMS; not installing"
  got="$(sha256_of "$STAGE/$asset")"
  [ "$got" = "$want" ] || fail "Integrity verification failed: $asset SHA-256 is $got, expected $want; not installing"

  [ "$(tar -tzf "$STAGE/$asset" 2>/dev/null)" = "$NAME" ] || fail "Unexpected archive contents (expected only $NAME); not installing"
  mkdir "$STAGE/out" && tar -xzf "$STAGE/$asset" -C "$STAGE/out" || fail "Cannot extract archive; not installing"
  [ -f "$STAGE/out/$NAME" ] && [ ! -L "$STAGE/out/$NAME" ] || fail "$NAME in the archive is not a regular file; not installing"
  chmod 755 "$STAGE/out/$NAME"
  if command -v file >/dev/null 2>&1; then
    local want_arch="$ARCH"
    [ "$ARCH" = amd64 ] && want_arch=x86_64
    file "$STAGE/out/$NAME" | grep -q "Mach-O.*$want_arch" || fail "The archived binary is not darwin/$ARCH; not installing"
  fi

  mkdir -p "$DEST" || fail "Cannot create $DEST"
  sha256_of "$STAGE/out/$NAME" >"$STAGE/out/$NAME.sha256"
  mv -f "$STAGE/out/$NAME" "$DEST/$NAME" && mv -f "$STAGE/out/$NAME.sha256" "$DEST/$NAME.sha256" || fail "Cannot install the binary"
  rm -rf "$STAGE" "$LOCKDIR"
  STAGE="" LOCKDIR=""
}

check_tmux_capabilities

BINARY=""
if ! resolve_dev_binary; then
  VERSION="$(tr -d '[:space:]' <"$CURRENT_DIR/VERSION" 2>/dev/null)"
  printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$' || fail "VERSION file is missing or invalid: $VERSION"
  [ "$(uname -s)" = Darwin ] || fail "Unsupported platform $(uname -s): only macOS (darwin/arm64 and darwin/amd64) is supported"
  case "$(uname -m)" in
    arm64) ARCH=arm64 ;;
    x86_64) ARCH=amd64 ;;
    *) fail "Unsupported architecture $(uname -m): only arm64 and x86_64 are supported; no fallback" ;;
  esac
  DEST="$CURRENT_DIR/bin/$VERSION-darwin-$ARCH"
  installed_ok || install_release
  BINARY="$DEST/$NAME"
fi

if ! out="$("$BINARY" start -show 2>&1 </dev/null)"; then
  fail "Startup failed: $out"
fi
exit 0
