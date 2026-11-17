#!/usr/bin/env bash
# 建置並封裝 macOS arm64／amd64 發布檔，輸出到 $DIST_DIR（預設 ./dist）：
#   tmux-opencode-status_<版本>_darwin_<arch>.tar.gz   （內容只有一個執行檔 tmux-opencode-status）
#   SHA256SUMS                                          （shasum -a 256 格式，TPM 入口腳本以它驗證）
# 用法：scripts/build-release.sh [版本]      版本預設讀 VERSION 檔
# 環境變數：DIST_DIR 輸出目錄；ARCHS 要建的架構（預設 "arm64 amd64"）。
# 可重現：CGO_ENABLED=0、-trimpath、清除 build id、固定的封存中繼資料（tools/pack）；
# 相同原始碼與 Go 版本應得到逐位元組相同的產物（Go 版本由 go.mod 釘住）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME=tmux-opencode-status
VERSION="${1:-$(tr -d '[:space:]' < "$ROOT/VERSION")}"
DIST="${DIST_DIR:-$ROOT/dist}"
ARCHS="${ARCHS:-arm64 amd64}"

vre='^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$'
[[ "$VERSION" =~ $vre ]] || { echo "build-release：版本格式無效：$VERSION" >&2; exit 2; }
command -v go >/dev/null || { echo "build-release：需要 Go（建置者才需要；使用者安裝不需要）" >&2; exit 1; }

mkdir -p "$DIST"
DIST="$(cd "$DIST" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cd "$ROOT"
go build -trimpath -o "$work/pack" ./tools/pack

assets=()
for arch in $ARCHS; do
  case "$arch" in arm64) want="arm64" ;; amd64) want="x86_64" ;; *) echo "build-release：不支援的架構 $arch" >&2; exit 2 ;; esac
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" \
    -o "$work/$arch-$NAME" ./cmd/tmux-opencode-status
  if command -v file >/dev/null && ! file "$work/$arch-$NAME" | grep -q "Mach-O.*$want"; then
    echo "build-release：產物不是 darwin/$want：$(file "$work/$arch-$NAME")" >&2
    exit 1
  fi
  asset="${NAME}_${VERSION}_darwin_${arch}.tar.gz"
  "$work/pack" -in "$work/$arch-$NAME" -out "$DIST/$asset" -name "$NAME"
  assets+=("$asset")
done

( cd "$DIST" && rm -f SHA256SUMS && shasum -a 256 "${assets[@]}" > SHA256SUMS )
echo "已建置 ${VERSION}（$(go version)）→ ${DIST}"
cat "$DIST/SHA256SUMS"
