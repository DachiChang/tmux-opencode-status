# 點擊側欄群組跳到 TUI pane：固定開啟、接管 `MouseDown1SideStatus`

點擊狀態側欄中某個目錄群組的任一列，client 切換到該群組的**跳轉目標**（TUI client 所在的 tmux pane，可跨 session）。功能固定開啟，不提供開關。

- 常駐程式啟動時接管 root 鍵表的 `MouseDown1SideStatus`；range 字串為 `oc<N>` 時切到 pane `%N`，否則執行 tmux 預設的 `switch-client -t =`。綁定完全在 tmux 內執行，不開 shell、不呼叫插件執行檔。
- 明確停止時把綁定寫回 tmux 預設 `switch-client -t =`；不用 `unbind`（停止後 `side-status-format` 回到 tmux 預設，預設格式的 session／window 點擊切換依賴這條預設綁定）。交接時不動，由新實例重寫；server 結束時不處理。
- **不保存或還原使用者原有的自訂綁定。** 取捨：簡單、可預期；代價是覆寫使用者自訂的 `MouseDown1SideStatus`，這在 README 明講。
- 側欄以 `#[range=user|oc<N>]…#[norange]` 逐行包住群組列，不依賴 range 跨換行延續。range 參數只由固定前綴加十進位數字組成，與使用者資料無關。

## 為何用 `range=user` 而非 `range=pane`

以 tmux next-3.9 實測與原始碼確認：

- `range=pane|%N` 在 status 格式中 `%` 會被 strftime 吃掉，需寫成 `%%N`；
- 即使寫對，滑鼠事件只設定 pane，session／window 仍是點擊者目前所在；`cmd_mouse_pane()` 會因 `window_has_pane()` 失敗而退回，因此只能切換同一 window 內的 pane，無法跨 session；
- `mouse_status_range` 只得到字串 `pane`，拿不到編號；
- `range=user|X` 觸發 `SideStatus` 鍵並把 X（最多 15 bytes）放入 `mouse_status_range`，由綁定自行切換，可跨 session 精確到 pane。

待 tmux 支援跨 session 的 `range=pane` 後可重新評估。

## 與其他決策的關係

與 ADR-0001（對 OpenCode 唯讀）不衝突：綁定只動 tmux，不動 OpenCode。但取代先前 README「不新增任何鍵盤或滑鼠綁定」的承諾。
