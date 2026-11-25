#!/usr/bin/env bash
# Build the development binary at bin/dev/tmux-opencode-status (requires Go).
# The TPM entry script does not use it automatically. Set this in ~/.tmux.conf:
#   set -g @opencode-status-binary "/absolute/path/bin/dev/tmux-opencode-status"
# With this setting, the entry script uses the local binary instead of downloading.
# If the path does not exist, it reports an error without falling back to a download.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
command -v go >/dev/null || { echo "build-dev: Go is required" >&2; exit 1; }
mkdir -p "$ROOT/bin/dev"
cd "$ROOT"
go build -o "$ROOT/bin/dev/tmux-opencode-status" ./cmd/tmux-opencode-status
echo "Built $ROOT/bin/dev/tmux-opencode-status"
