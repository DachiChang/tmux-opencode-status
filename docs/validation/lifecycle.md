# #11 常駐生命週期與唯讀診斷

## 指令（給 TPM 腳本 #12 呼叫的入口）

```sh
tmux-opencode-status start [-show] [-tabs …] [-service …] [-width 36] …   # 冪等；TPM 的 .tmux 在每次載入／重載時呼叫
tmux-opencode-status stop                                                  # 停止並清理本插件資源
tmux-opencode-status diagnose                                              # 唯讀診斷
tmux-opencode-status run -L <name> | -S <path> …                           # 常駐程式本體；一般由 start 啟動，不直接用
```

- 省略子指令等同 `run`（維持既有旗標用法）。
- **`start`／`stop`／`diagnose`** 沒有 `-L`／`-S` 時沿用 `$TMUX` 的 socket 路徑（TPM 的 `run-shell` 內 `$TMUX` 指向載入設定的那個 server）；
  兩者都沒有就拒絕（`必須明確指定 -L/-S`），絕不退回預設 tmux server。**`run`** 一律要求明確 `-L`／`-S`。
- 給 #12：`.tmux` 腳本只要執行 `"$CURRENT_DIR/bin/tmux-opencode-status" start -show`（`-show` 讓首次載入打開側欄，每個 tmux server 只做一次）。
  `start` 成功回 0；失敗（缺能力、無效設定、無法交接、啟動逾時）回非 0 並把原因寫 stderr（#12 應轉為 `tmux display-message`）。
  `start` 會把自己所在的執行檔路徑記進實例指紋，所以 #12 換了新下載的二進位後再呼叫 `start` 就會自動交接。
- 旗標都有預設值且會先驗證（`-width ≥ 4`、各週期 > 0）；無效時 `start` 在動任何東西之前失敗，既有實例不受影響。

## 單實例（每個 tmux socket 一個）

- 以 `display-message -p '#{socket_path}'` 取得 server 回報的 socket 路徑（`-L`／`-S`／`$TMUX` 指到同一個 server 得到同一組檔案），
  SHA-256 前 8 bytes 衍生檔名，放在 `$XDG_RUNTIME_DIR/tmux-opencode-status/`，沒有則放
  `$TMPDIR/tmux-opencode-status-<uid>/`（要求 0700 且為自己所有，否則拒絕）。
  - `<id>.lock`：實例存活期間持有的 **fcntl 記錄鎖**，內容是實例的有效設定指紋。行程結束（含 `kill -9`）鎖自動釋放，沒有過期鎖。
  - `<id>.start`：`start`／`stop` 的序列化鎖。並行 `start` 一次只有一個在判斷與啟動。
  - `<id>.json`：診斷用狀態；`<id>.log`：常駐程式 stderr（超過 64 KiB 於下次啟動時重建）。
- `run` 自己也取 `<id>.lock`：即使有人繞過 `start` 直接執行 `run`，第二個也會立即退出（結束碼 0）。
- 不同 socket 的檔案不同，因此實例、輸出（`@tos_sb_*`）、hook、退出完全獨立；所有 tmux 指令都帶明確的 `-L`／`-S`。

## 重載交接

`start` 的判斷（在啟動鎖內）：

| 情況 | 動作 |
| --- | --- |
| 沒有實例 | 啟動新實例，等它持有鎖且寫好指紋才返回 |
| 有實例、指紋相同 | 什麼都不做（冪等） |
| 有實例、指紋不同（旗標、執行檔，或同 socket 上換了新 tmux server） | 對舊實例送 `SIGUSR1`：它只移除自己的 hook 就退出，**畫面維持原樣**；鎖釋放後才啟動新實例。5 秒內沒讓位才改送 `SIGTERM`，再 5 秒仍沒有就失敗。新舊從不同時運作 |

- 交接不呼叫 `side-status`（不強制打開已隱藏的側欄）、不清除 session 層級 override、不新增任何綁定。
- **`-show` 每個 tmux server 只生效一次**：第一次成功後在全域設 `@opencode-status-shown`（插件專屬；隨 server 結束消失，
  `stop` 會移除以便重新啟動後再次顯示）。之後重載、交接、崩潰後重啟，只要使用者把側欄隱藏就不會被重新打開。
- 新實例第一次寫入時會順便清掉前一個實例（例如被 `kill -9`）留下的 `@tos_sb_*` 孤兒選項。
- 模式切換（`@opencode-status-mode`）不需 `start`：同一實例由 `after-set-option` hook 喚醒後重讀（見 `modes.md`）。

## 退出與清理

| 結束原因 | 清理 |
| --- | --- |
| `stop`／SIGTERM／SIGINT | 移除自己的 hook（索引 41700）、`@tos_sb_*`、`@opencode-status-shown`；`side-status-format` **只在仍等於本實例最後寫入的內容時**才移除（連同 `side-status-width`）；刪除鎖檔與狀態檔 |
| 交接（SIGUSR1） | 只移除 hook、刪除鎖檔與狀態檔；畫面選項保留給新實例接手 |
| tmux server 結束 | 本機鎖檔與狀態檔；tmux 內的資源已隨 server 消失 |

- **server 結束的判定**：每 2 秒（`-server-check`）以 `kill(pid, 0)` 確認 **tmux server 行程**是否仍在，行程不在才退出。
  不以「tmux 指令失敗」判斷——指令可能因暫時忙碌或環境問題失敗。暫時性失敗期間：側欄沿用最後可用資料並提示、鎖與狀態不動、
  使用者選項與 hook 不動；tmux 恢復後繼續運作（`TestTransientTmuxFailureIsNotMistakenForServerExit`）。
- 只清本插件資源：不動 `@oc_side*` 等使用者選項、使用者自己的 hook、session override、綁定。
- 沒有 TUI 或側欄隱藏：實例存活。沒有追蹤項目時不連 OpenCode、不查詢 session；有 TUI 出現或側欄重新顯示都不需手動重啟。

## 診斷（唯讀）

`diagnose` 只查 tmux（`display-message`、`show-options`、`list-sessions`）與本機狀態檔，**不連 OpenCode**、不寫任何選項、不啟動／停止任何程序。
輸出項目：tmux 版本與 pid；必要能力（`side-status`、`side-status-width`、`side-status-format` 可用）；插件模式設定與生效模式；
全域 side-status（隱藏時說明仍在追蹤）；常駐實例狀態；發現的 TUI 數與追蹤的 tab 數；服務連線（已連線／連線中／待命／無法連線＋固定原因）；
目前問題與最近錯誤（最多 8 筆，邊緣觸發，只有來源與固定描述）；session 層級覆寫與影響；結論。

- **能力檢查**：只有 tmux 回報 `invalid option` 才算缺少能力；其他錯誤（server 暫時不可用）如實說無法確認，不誤報成缺少能力。
  `start` 缺能力時明確失敗且不啟動、不寫任何選項。
- **session override**：列出在 session 層級設了 `side-status*` 的 tmux session（只顯示 session id，如 `$2`，不顯示名稱），
  說明它們不跟隨全域設定、插件不會清除；使用者需自行 `set -u -t <session> <選項>`。
- **去敏**：診斷與狀態檔的錯誤文字一律是固定描述（`ocsrc.Reason` 把連線錯誤歸類成固定字串，不含原始錯誤、URL、路徑、回應本文）。
  不輸出憑證、prompt、對話、OpenCode tab／session 標題、目錄路徑、服務 URL、tmux session 名稱。
  測試以哨兵字串植入（密碼、`sk-live-…`、tab 標題、prompt 式字串、目錄名、tmux session 名稱），逐一斷言不會出現在輸出。

## 測試（程序外部接縫）

`cmd/tmux-opencode-status/lifecycle_e2e_test.go`：真實常駐程式 + 隔離 tmux（`-L tos-e2e-… -f /dev/null`）+ 假 OpenCode 服務（`fakeoc`）；
`XDG_RUNTIME_DIR` 指向測試暫存目錄，不碰使用者真實 runtime 目錄與預設 tmux server。

| 情境 | 測試 |
| --- | --- |
| 重複載入冪等（同 pid） | `TestStartIsIdempotentAndReloadsNeverLeaveASecondInstance` |
| 12 個並行 start 只剩一個 | `TestConcurrentStartsLeaveExactlyOneInstance` |
| 重載交接：期間取樣從不超過 1 個；隱藏不被打開；override 與綁定不動；hook 各一份 | `TestReloadWithChangedSettingsHandsOffToExactlyOneNewInstance` |
| 兩 socket 獨立（輸出、停止互不影響） | `TestTwoSocketsRunIndependentInstances` |
| 無 TUI 不查詢；隱藏時追蹤；重新顯示免重啟 | `TestIdleWithoutTUIsAndHiddenSidebarStillRecoverWithoutManualRestart` |
| 最後 TUI 關閉後停止查詢但存活，新 TUI 自動恢復 | `TestLastTUIClosingStopsQueriesButKeepsTheInstanceAlive` |
| server 結束後退出並清理 | `TestInstanceExitsAndCleansUpWhenTheTmuxServerExits` |
| 暫時性 tmux 失敗不誤判（含不清使用者選項／hook） | `TestTransientTmuxFailureIsNotMistakenForServerExit` |
| 明確停止只清自己的資源；使用者自設的 format 不動 | `TestStopCleansOnlyThePluginsOwnResources`、`TestStopDoesNotRemoveAFormatTheUserSetAfterwards` |
| 缺少能力明確失敗；暫時性錯誤不誤報成缺能力 | `TestMissingTmuxCapabilityFailsLoudly…`、`TestTransientTmuxErrorAtStartIsNotReportedAsMissingCapability` |
| kill -9 後無過期鎖、清孤兒選項 | `TestStartRecoversFromAKilledInstance…` |
| 同 socket 換新 server 時接手 | `TestStartAfterTheTmuxServerRestartedTakesOverFromTheStaleInstance` |
| 無效設定先驗證、不動既有實例 | `TestInvalidStartSettingsAreRejectedBeforeTouchingTheRunningInstance` |
| `$TMUX` 解析、無 socket 拒絕 | `TestStartWithoutSocketFlagsFollowsTMUXEnvAndRefusesWithoutIt` |
| 診斷涵蓋項目與去敏 | `TestDiagnoseReportsTheDecidedItemsAndLeaksNothingSensitive`、`TestDiagnoseRecentErrorsInclude…` |
| 診斷唯讀、說明為何沒顯示、隱藏提示、缺能力 | `TestDiagnoseIsStrictlyReadOnly`、`TestDiagnoseExplainsWhyNothingIsShown`、`…MentionsTheSidebarIsHidden…`、`…ReportsAMissingCapability` |

## 已知限制

- 只支援 macOS（沿用 #4 的 `KERN_PROCARGS2`／`lsof`）；fcntl 鎖與 `$TMPDIR` 邏輯本身可攜。
- `-show` 的「只做一次」以 tmux server 為單位；使用者若 `set -gu @opencode-status-shown`，下一次 start 會再次打開側欄（這是他自己的操作）。
- 診斷的服務連線狀態來自常駐程式，不是診斷當下重新探測（刻意：診斷不連 OpenCode）。
- 診斷無法判斷 session override 的「實際效果」是否正好是使用者想要的，只列事實與影響。
