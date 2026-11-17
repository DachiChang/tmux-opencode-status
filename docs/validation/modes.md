# #8 cwd／global 模式與設定重載

## 使用者設定

```tmux
# ~/.tmux.conf（預設 cwd；只接受 cwd 或 global）
set -g @opencode-status-mode global
```

重載（`tmux source-file ~/.tmux.conf`）即套用，不需重啟 tmux 或常駐程式。選項使用插件專屬前綴
`@opencode-status-mode`，不使用也不修改舊 POC 的 `@oc_side*`。

## 語意

| 狀態 | 行為 |
| --- | --- |
| 未設定（選項不存在） | 預設 cwd |
| `cwd` / `global` | 套用該模式 |
| 其他任何值（含空字串、大小寫不同、前後空白） | 無效；**不修剪、不猜測** |
| 無效，且從未有過有效模式（首次） | 不開始追蹤：不讀 tmux／tab 清單，側欄只留「模式設定無效，尚未開始追蹤」，不顯示「沒有 OpenCode TUI」或「無 tab」 |
| 無效，但已有有效模式（重載） | 保留原有效模式，標題下提示「模式設定無效，未套用，仍用 <mode>」；選項修正或移除後提示消失 |
| 讀不到選項（tmux 錯誤） | 視為錯誤，不當成「未設定」；已有模式時保留，並提示「無法讀取模式設定」 |

- cwd：每個「有執行中 TUI 的目錄」一個群組，tab 取自 `cwd[<TUI 行程 cwd>]`。
- global：只要有 TUI（不論其所在目錄、數量）就顯示**單一 `GLOBAL` 群組**，tab 取自 `global.tabs`，沿用來源順序；
  同樣的標題／群組摘要／「無 tab」結構。最後一個 TUI 關閉後呈現「沒有 OpenCode TUI」。
- cwd 與 global 來源從不合併；不推斷各 TUI 的有效 scope、不支援混用。
- **使用者責任**：插件模式應與 OpenCode 的 tab scope 設定一致。若 OpenCode 實際以 cwd scope 運作卻把插件設為 global，
  global 清單可能是空的或過期的（OpenCode 只在 global scope 下維護 `global.tabs`）；插件不會偵測也不會警告這種不一致。

## 套用機制與單實例

- 常駐程式在專屬索引 `41700` 安裝 `after-set-option` hook，條件為 `#{hook_argument_0}` 等於 `@opencode-status-mode` 時
  才 `wait-for -S`，因此插件自己寫入側欄選項（`@tos_sb_*`）不會造成回授。設定、`set -gu`、重載設定檔都會觸發。
- **啟動空窗**：hook 是常駐程式啟動後才安裝；首次同步與 hook 就緒之間的設定變動沒有通知。hook 全部裝好後補一次喚醒，
  該空窗內的重載因此也在 1 秒內套用，不必等 5 秒補漏（`TestModeChangeDuringDaemonStartupIsNotLeftToTheBackstop` 以 tmux 包裝確定性放大空窗）。
- 喚醒後內容一律重讀；漏通知由低頻補漏補上。模式與資料都沒變時不重寫 tmux（指紋比對）。
- 切換模式只改變同一個實例的讀取結果，不重啟、不產生第二個實例，也不呼叫 `side-status`
  （不強制打開已隱藏的側欄；只有明確 `-show` 才會設定）。完整單實例生命週期屬 #11。
- 結束時只移除自己的 hook 索引。

## 測試（外部行為接縫）

- `internal/sidebar/mode_test.go`：以受控 `ModeSource`／pane／tab 來源驗證畫面輸出——global 單一群組與來源順序、其他目錄 session、
  多 TUI 不重複、最後 TUI 關閉、cwd⇄global 雙向切換無殘留、預設 cwd、首次無效不追蹤（連 pane 都不讀）、重載無效保留原值、
  讀不到設定不退回預設、同一模式重載不重寫。
- `cmd/tmux-opencode-status/e2e_test.go`（真實常駐程式 + 隔離 tmux `-L … -f /dev/null` + 真實 attached client 畫面）：
  以 `source-file` 重載設定切換模式（實測約 0.2 s 生效）、實例數維持 1、多 TUI／最後關閉、無效值流程、
  `side-status off` 不被切換重新打開、使用者選項與其他 hook 不被改。
