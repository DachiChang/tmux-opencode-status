# #4 cwd 模式即時 tab 清單：架構與驗證

## 執行方式

```sh
go build -o tmux-opencode-status ./cmd/tmux-opencode-status
./tmux-opencode-status start [-show] [-tabs <tabs.json>] [-backstop 5s]   # 冪等啟動（TPM 入口），見 lifecycle.md
./tmux-opencode-status run -L <socket> [-show] [-tabs <tabs.json>] [-backstop 5s]   # 常駐程式本體；或 -S <path>
```

- 必須明確指定 `-L` 或 `-S`，不使用預設 tmux server；`-f /dev/null` 僅供隔離測試。
- `-show` 才會把 `side-status` 設為 `left`（預設不強制打開使用者隱藏的側欄）。
- 本票只有 cwd 模式；狀態一律灰色 `!`（#5 才接 session 狀態）。

## 外部資料來源與測試接縫（`internal/sidebar`）

| 介面 | 正式實作 | 測試以什麼替換 |
| --- | --- | --- |
| `PaneSource.Panes()` | `tmuxsrc.Client`（`list-panes -a` + `ps` + `KERN_PROCARGS2` + `lsof`；以 pane_id 去重） | 受控 pane 事實 |
| `TabSource.Read()` | `tabsrc.Source`（唯讀 tabs.json；不存在＝空，其他錯誤＝錯誤） | 受控 `TabsFile` 或錯誤 |
| `Output.Publish()` | `tmuxsrc.Client`（單一 `source-file -`，選項前綴 `@tos_sb_`） | 記錄寫入、以 `Published.Select(avail)` 還原 client 畫面 |
| `Deps.Wake` / `Deps.Ticks` | tmux hook + `wait-for`、tabs.json 目錄 fsnotify／低頻 ticker | 受控 channel（不需等待真實時間） |

三層測試：
1. `internal/sidebar`：程序核心行為（畫面輸出、不重複寫入、Run 迴圈事件／補漏、錯誤不偽裝空清單）。
2. `cmd/tmux-opencode-status/e2e_test.go`：真實常駐程式 + 隔離 tmux（`-L tos-e2e-… -f /dev/null`）+ 假 `opencode` 行程 + 真實 attached client 畫面。
3. 真實 OpenCode／多 client 高度：沿用 #2、#3 的驗證，本票未重做。

## 時序量測（隔離 tmux，macOS arm64，backstop=5s）

| 情境 | 目標 | 實測 |
| --- | --- | --- |
| 新增 TUI window（hook 通知） | 1 秒 | 約 0.23–0.48 s |
| tabs.json 新增 tab（rename） | 1 秒 | 約 0.18–0.24 s |
| 關閉 TUI window | 1 秒 | < 1 s（測試斷言） |
| 既有 shell 啟動 TUI（終端標題開啟，`pane-title-changed` 喚醒） | 1 秒 | 約 0.58 s |
| 既有 shell 結束 TUI（終端標題開啟） | 1 秒 | 約 0.23–0.47 s |
| 既有 shell 啟動／結束 TUI（終端標題關閉，無喚醒） | 10 秒 | ≤ 10 s（退回補漏，測試斷言） |
| TUI 行程被強制結束、pane 仍在（無 hook） | 10 秒 | 約 5 s（= backstop 週期） |
| shell 內啟動 TUI（無 hook、無標題變更） | 10 秒 | ≤ 10 s（測試斷言） |

「內容不變不寫入」：以輸出指紋判斷，單元測試驗證 3 次同步只寫 1 次。

## 重要設計決定

- 使用者選項改用專屬前綴 `@tos_sb_<avail>`（heightfit 的 `OptPrefix`）；舊 POC 的 `@sb_`、`@oc_side*` 不使用也不修改。
- hook 使用專屬陣列索引 `41700`，結束時只移除自己的索引；不新增任何按鍵／滑鼠綁定。
  hook 是視窗層級（`show-hooks -gw`），`pane-exited`、`window-linked` 等都在其中。
- hook 只負責「喚醒」，內容每次重新讀取；漏掉任何 hook 由低頻補漏補上。
- **pane title 喚醒**：在既有 shell 啟動／結束 TUI 時 tmux 沒有結構事件，但 TUI 會改終端標題，
  `pane-title-changed` hook 因此作為「提早重新掃描」的喚醒訊號。辨識仍只看前景行程 argv，
  **不讀 title 內容**（hook 指令只是 `wait-for -S`，不帶 `#{pane_title}`）；任意行程把標題設成 OpenCode 不會被誤認為 TUI
  （測試：`TestTitleChangesNeverMakeANonTUIProcessLookLikeATUI`）。
- **去抖**：title hook 使用獨立的 `tos_title` channel，常駐程式每次被喚醒後至少間隔 300 ms 才再次等待；
  間隔內的連續變更由 tmux 記住並合併成一次 trailing 喚醒（不遺失、不累積），再加上 sidebar 的 100 ms debounce。
  實測 TUI 每 20 ms 改一次標題時，3 秒內只掃描約 6 次；標題變化本身不寫 tmux（內容指紋不變不寫，實測寫入 0 次）。
- **標題關閉**：行程不改標題就沒有喚醒，行為退回低頻補漏（≤ 10 s），功能不受影響。
- **啟動空窗**：tmux hook 與 tabs.json watcher 就緒後各補一次喚醒，裝好之前發生的變動不必等補漏
  （`tabsrc.TestWatchWakesOnceWhenReadyEvenWithoutAnyFileEvent`）。
- 讀取失敗（tmux、tabs.json）：沿用最後可用資料並在標題下顯示一行提示；從未成功讀過時只有提示，不顯示「沒有 OpenCode TUI」／「無 tab」。
- tabs.json 不存在視為全新環境（空清單），格式不相容才是錯誤。
- heightfit 擴充：`View`（`Empty`、`Notice`）、無 tab 群組以 `無 tab` 行呈現且不計入隱藏 tab 數。
