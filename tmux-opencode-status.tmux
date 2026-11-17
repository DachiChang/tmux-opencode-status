#!/usr/bin/env bash
# TPM 入口（TPM 會把插件根目錄的 *.tmux 當可執行檔執行；tmux 載入／重載設定時都會再執行一次）。
#
# 流程：確認 tmux 能力 → 取得執行檔 → `start -show`（冪等；重複載入不會產生第二個實例）。
# 取得執行檔：
#   1. 設了 @opencode-status-binary：使用開發者自行編譯的執行檔，不下載。
#   2. 否則依 VERSION 與 uname -m 選擇 GitHub Release 資產，下載、以 SHA256SUMS 驗證、放到 bin/ 後使用。
# 任何一步失敗都明確報錯（stderr 與 tmux display-message）並以非 0 結束；
# 絕不靜默改用 go build、也絕不換用其他架構的執行檔。不新增任何按鍵或滑鼠綁定。
#
# 可注入（測試與鏡像用）：TMUX_OPENCODE_STATUS_RELEASE_URL 取代預設的 Release 下載基底網址。

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

[ -n "${TMUX:-}" ] || fail "必須在 tmux 內執行（由 TPM 或 tmux run-shell 載入）；不會使用預設 tmux server"
command -v tmux >/dev/null 2>&1 || fail "找不到 tmux"

check_tmux_capabilities() {
  local opt out missing=""
  for opt in side-status side-status-width side-status-format; do
    if ! out="$(tmux show-options -gv "$opt" 2>&1)"; then
      case "$out" in
        *"invalid option"*) missing="$missing $opt" ;;
        *) fail "無法確認 tmux 能力：$out" ;;
      esac
    fi
  done
  [ -z "$missing" ] || fail "tmux $(tmux -V 2>/dev/null) 缺少必要能力：$missing（需要 tmux 3.9 的原生 side-status）；不啟動"
}

resolve_dev_binary() {
  local bin
  bin="$(tmux show-options -gqv @opencode-status-binary 2>/dev/null)"
  [ -n "$bin" ] || return 1
  case "$bin" in /*) ;; *) fail "@opencode-status-binary 必須是絕對路徑：$bin" ;; esac
  [ -f "$bin" ] && [ -x "$bin" ] || fail "@opencode-status-binary 指定的執行檔不存在或不可執行：$bin（不會改用下載）"
  BINARY="$bin"
}

sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }

installed_ok() {
  [ -f "$DEST/$NAME" ] && [ -x "$DEST/$NAME" ] && [ -f "$DEST/$NAME.sha256" ] &&
    [ "$(sha256_of "$DEST/$NAME")" = "$(cat "$DEST/$NAME.sha256")" ]
}

fetch() { # fetch <url> <輸出檔>
  local proto=(--proto '=https' --proto-redir '=https')
  [ -z "${TMUX_OPENCODE_STATUS_RELEASE_URL:-}" ] || proto=()
  curl --fail --silent --show-error --location "${proto[@]}" --connect-timeout 15 --max-time 180 \
    --output "$2" "$1" 2>&1 || fail "下載失敗：$1（不會改用本機編譯；開發者請見 README 的自行編譯路徑）"
}

install_release() {
  local asset="${NAME}_${VERSION}_darwin_${ARCH}.tar.gz"
  local base="${TMUX_OPENCODE_STATUS_RELEASE_URL:-$REPO_URL/releases/download/v$VERSION}"
  base="${base%/}"

  mkdir -p "$CURRENT_DIR/bin" || fail "無法建立 $CURRENT_DIR/bin"
  LOCKDIR="$CURRENT_DIR/bin/.install.lock"
  local waited=0 holder
  until mkdir "$LOCKDIR" 2>/dev/null; do
    holder="$(cat "$LOCKDIR/pid" 2>/dev/null)"
    if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then
      rm -rf "$LOCKDIR"
      continue
    fi
    waited=$((waited + 1))
    if [ "$waited" -gt 240 ]; then LOCKDIR=""; fail "等待另一個安裝完成逾時"; fi
    sleep 0.5
  done
  echo $$ >"$LOCKDIR/pid"

  # 取得鎖後再確認一次：別的載入可能剛裝好。
  installed_ok && return 0

  STAGE="$(mktemp -d "$CURRENT_DIR/bin/.stage.XXXXXX")" || fail "無法建立暫存目錄"
  fetch "$base/SHA256SUMS" "$STAGE/SHA256SUMS"
  fetch "$base/$asset" "$STAGE/$asset"

  local want got
  want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1; exit }' "$STAGE/SHA256SUMS")"
  case "$want" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) [ "${#want}" -eq 64 ] || want="" ;;
    *) want="" ;;
  esac
  [ -n "$want" ] || fail "SHA256SUMS 沒有 $asset 的有效校驗碼；不安裝"
  got="$(sha256_of "$STAGE/$asset")"
  [ "$got" = "$want" ] || fail "完整性驗證失敗：$asset 的 SHA-256 為 $got，預期 $want；不安裝"

  [ "$(tar -tzf "$STAGE/$asset" 2>/dev/null)" = "$NAME" ] || fail "封存內容不符預期（應只含 $NAME）；不安裝"
  mkdir "$STAGE/out" && tar -xzf "$STAGE/$asset" -C "$STAGE/out" || fail "解開封存失敗；不安裝"
  [ -f "$STAGE/out/$NAME" ] && [ ! -L "$STAGE/out/$NAME" ] || fail "封存內的 $NAME 不是一般檔案；不安裝"
  chmod 755 "$STAGE/out/$NAME"
  if command -v file >/dev/null 2>&1; then
    local want_arch="$ARCH"
    [ "$ARCH" = amd64 ] && want_arch=x86_64
    file "$STAGE/out/$NAME" | grep -q "Mach-O.*$want_arch" || fail "封存內的執行檔不是 darwin/$ARCH；不安裝"
  fi

  mkdir -p "$DEST" || fail "無法建立 $DEST"
  sha256_of "$STAGE/out/$NAME" >"$STAGE/out/$NAME.sha256"
  mv -f "$STAGE/out/$NAME" "$DEST/$NAME" && mv -f "$STAGE/out/$NAME.sha256" "$DEST/$NAME.sha256" || fail "放置執行檔失敗"
  rm -rf "$STAGE" "$LOCKDIR"
  STAGE="" LOCKDIR=""
}

check_tmux_capabilities

BINARY=""
if ! resolve_dev_binary; then
  VERSION="$(tr -d '[:space:]' <"$CURRENT_DIR/VERSION" 2>/dev/null)"
  printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$' || fail "VERSION 檔缺少或格式無效：$VERSION"
  [ "$(uname -s)" = Darwin ] || fail "不支援的平台 $(uname -s)：只提供 macOS（darwin/arm64、darwin/amd64）"
  case "$(uname -m)" in
    arm64) ARCH=arm64 ;;
    x86_64) ARCH=amd64 ;;
    *) fail "不支援的架構 $(uname -m)：只提供 arm64 與 x86_64；不會改用其他架構" ;;
  esac
  DEST="$CURRENT_DIR/bin/$VERSION-darwin-$ARCH"
  installed_ok || install_release
  BINARY="$DEST/$NAME"
fi

if ! out="$("$BINARY" start -show 2>&1 </dev/null)"; then
  fail "啟動失敗：$out"
fi
exit 0
