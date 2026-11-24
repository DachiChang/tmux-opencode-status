# tmux-opencode-status

[English](README.md) · 繁體中文

在 tmux 側欄查看 OpenCode 的 tab 狀態，不必逐一切換 pane。

## 快速安裝

**需求：** macOS（Apple Silicon 或 Intel）、支援 `side-status` 的 tmux 3.9，以及已安裝的 TPM。OpenCode 須使用 `latest` channel、預設資料位置與本機共用背景服務。不用安裝 Go。

在 `~/.tmux.conf` 的 TPM 初始化指令之前加入：

```tmux
set -g @plugin 'DachiChang/tmux-opencode-status'
```

執行 `tmux source-file ~/.tmux.conf` 重載設定，再按 tmux 的 prefix（預設 `Ctrl-b`），接著按大寫 `I`。首次載入後，左側會出現 `OPENCODE` 側欄。沒看到的話，請看[故障排除](docs/guide.zh-TW.md#故障排除)。

插件預設使用 `cwd` 模式。如果 OpenCode 使用 global scope，請看[設定模式](docs/guide.zh-TW.md#設定模式)。

## 基本操作

- **滑鼠：** 開啟 tmux 的 `mouse` 選項後，點目錄群組中的任一列，就能跳到該群組的 OpenCode pane。
- **鍵盤：** 按 `prefix + A`，再按側欄上顯示的字母。

插件會覆寫 `MouseDown1SideStatus` 和觸發鍵原有的 prefix 綁定。如果你有自訂綁定，請先看[跳轉與按鍵綁定](docs/guide.zh-TW.md#跳轉與按鍵綁定)。

## 更多資訊

[配色](docs/guide.zh-TW.md#配色) · [故障排除](docs/guide.zh-TW.md#故障排除) · [開發與發布](docs/guide.zh-TW.md#開發與發布)
