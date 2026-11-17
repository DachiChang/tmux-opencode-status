# #4 cwd 模式即時 tab 清單：架構與驗證

## 執行方式

```sh
go build -o tmux-opencode-status ./cmd/tmux-opencode-status
./tmux-opencode-status -L <socket> [-show] [-tabs <tabs.json>] [-backstop 5s]   # 或 -S <path>
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
| TUI 行程被強制結束、pane 仍在（無 hook） | 10 秒 | 約 5 s（= backstop 週期） |
| shell 內啟動 TUI（無 hook） | 10 秒 | ≤ 10 s（測試斷言） |

「內容不變不寫入」：以輸出指紋判斷，單元測試驗證 3 次同步只寫 1 次。

## 重要設計決定

- 使用者選項改用專屬前綴 `@tos_sb_<avail>`（heightfit 的 `OptPrefix`）；舊 POC 的 `@sb_`、`@oc_side*` 不使用也不修改。
- hook 使用專屬陣列索引 `41700`，結束時只移除自己的索引；不新增任何按鍵／滑鼠綁定。
  hook 是視窗層級（`show-hooks -gw`），`pane-exited`、`window-linked` 等都在其中。
- hook 只負責「喚醒」，內容每次重新讀取；漏掉任何 hook 由低頻補漏補上。`pane-title-changed` 只用來加速偵測，不是辨識依據。
- shell 內啟動 TUI 不會觸發 hook（pane 沒有新增、title 不一定變）→ 靠補漏（<= backstop）發現。
- 讀取失敗（tmux、tabs.json）：沿用最後可用資料並在標題下顯示一行提示；從未成功讀過時只有提示，不顯示「沒有 OpenCode TUI」／「無 tab」。
- tabs.json 不存在視為全新環境（空清單），格式不相容才是錯誤。
- heightfit 擴充：`View`（`Empty`、`Notice`）、無 tab 群組以 `無 tab` 行呈現且不計入隱藏 tab 數。
