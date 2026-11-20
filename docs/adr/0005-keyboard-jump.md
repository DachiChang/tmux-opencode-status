# 鍵盤跳轉：`prefix A` 進入跳轉模式，側欄依 `client_key_table` 切換兩份版面

按觸發鍵（預設 `prefix A`，`@opencode-status-jump-trigger` 可改）後，client 進入跳轉模式：側欄目錄群組列的 `▎` 換成跳轉標籤 ` a `、` b `…，按字母切到該群組的跳轉目標（與點擊跳轉相同），按其他鍵離開。

- 觸發鍵綁定為 `bind-key -T prefix <鍵> switch-client -T opencode-status-jump`。按鍵表名稱 `opencode-status-jump` 是對外承諾：使用者自行在 root 表綁別名（例如 `bind -n M-a switch-client -T opencode-status-jump`），插件不提供、不管理 root 鍵。
- 每次發布兩份版面 `@tos_layout_main`、`@tos_layout_jump`（ADR-0004 命名），`side-status-format` 為 `#{?#{==:#{client_key_table},opencode-status-jump},#{@tos_layout_jump},#{@tos_layout_main}}`。`client_key_table` 逐 client 展開，所以只有按下觸發鍵的 client 看到標籤。版面以 `#{@…}` 展開一次，標題中的逗號或 `#{` 不會破壞條件。
- 跳轉按鍵表的 `a`–`z` 只在啟動時綁一次，按下時讀取 `@tos_jump_key_<字母>`（pane id `%N`）並以 `run-shell -C` 在 tmux 內執行 `switch-client`，不開 shell；選項為空時不動作。字母對應與版面同一次 `source-file` 寫入，畫面上的標籤與實際跳轉一致。
- 跳轉版面的群組列由 render 依標籤寬度（3 格標籤＋1 格空白）重新截斷名稱，群組摘要不被擠掉。沒有字母的群組（第 27 個以後、沒有跳轉目標）維持 `▎`。標籤用 `emphasis` 底、`title` 字，不新增色票。
- 實際綁定的鍵寫入 `@tos_jump_trigger`。交接時新實例讀取它，鍵不同就先 unbind 舊鍵再綁新鍵；明確停止時依它 unbind，並清空跳轉按鍵表。**不寫回任何東西、不保存或還原原有綁定**；被覆蓋的若是使用者 tmux.conf 的綁定，重載設定即恢復，若是 tmux 內建預設則要重啟 server。
- 觸發鍵設定無效（空字串、tmux 不認得的鍵名）時不綁定、不退回預設 `A`；`diagnose` 回報設定的鍵、實際綁定的鍵與成功／失敗／已被覆寫，無效計入問題數。

## 為何這樣做

- 不加 `refresh-client`：tmux 處理按鍵時，切換按鍵表會先要求重繪再執行綁定，進入與離開都會即時重繪（next-3.9 原始碼與實機試玩確認）。
- 預設 `A`：tmux next-3.9 預設 prefix 表、TPM、resurrect、continuum 與常見插件（pain-control、sessionist、yank、logging、fzf、floax、fingers、sidebar、urlview 等）都沒用到。`o`（next pane）、`g`（3.9 浮動 pane 的 move 表）、`Space`、`Tab` 為 tmux 預設；`a` 常被 `C-a` prefix 的使用者綁成 send-prefix。
- root 鍵不預設綁：root 鍵會在所有程式之前被攔截，影響比 prefix 鍵大，交給使用者決定。
- 停止時直接 unbind 而非寫回預設（與 ADR-0002 對 `MouseDown1SideStatus` 不同）：預設 `A` 不是 tmux 預設鍵，沒有可寫回的內容；使用者改鍵屬自行選擇。

## 與其他決策的關係

- 取代 README「唯一的 tmux 綁定是接管 `MouseDown1SideStatus`、不新增任何鍵盤綁定」的承諾。
- 建立在 ADR-0003（單一版面）之上：只需一般與跳轉兩份版面。
- 與 ADR-0001 不衝突：只動 tmux。
