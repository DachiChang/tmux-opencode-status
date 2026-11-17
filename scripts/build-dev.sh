#!/usr/bin/env bash
# 開發者自行編譯：輸出到 bin/dev/tmux-opencode-status（需要 Go）。
# TPM 入口腳本不會自動使用它；要明確在 ~/.tmux.conf 設定
#   set -g @opencode-status-binary "/絕對路徑/bin/dev/tmux-opencode-status"
# 設定後入口腳本改用該執行檔，完全不下載（路徑不存在時明確報錯，不退回下載）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
command -v go >/dev/null || { echo "build-dev：需要 Go" >&2; exit 1; }
mkdir -p "$ROOT/bin/dev"
cd "$ROOT"
go build -o "$ROOT/bin/dev/tmux-opencode-status" ./cmd/tmux-opencode-status
echo "已編譯 $ROOT/bin/dev/tmux-opencode-status"
