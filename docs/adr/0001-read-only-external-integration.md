# 以唯讀外部整合追蹤 OpenCode TUI 工作

為保持 OpenCode 可獨立使用，插件以 tmux pane、TUI 保存的 tab 清單與本機共用服務的唯讀資料建立狀態總覽，不安裝 OpenCode plugin、不修改 OpenCode 設定，也不代管服務生命週期。相較由 OpenCode plugin 主動回報，這個選擇減少使用者端整合步驟，但必須承擔非公開 tab 儲存格式的相容性風險，以及斷線後重新同步資料的責任。

插件模式由使用者明確選擇 cwd 或 global，預設 cwd；不自動推斷各 TUI 的有效 scope，使用者負責讓插件模式與 OpenCode 設定一致。這刻意排除不同 TUI 覆寫成不同 scope 的混用情境，以免為了取得每個 client 的設定而引入更深的整合。
