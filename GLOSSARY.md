# OpenCode 狀態側欄

彙整同一 tmux server 中正在開啟的 OpenCode TUI 工作，讓使用者掌握各目錄與 tab 的狀態。

## Language

**TUI client**:
一個正在執行的 OpenCode 終端介面；同一目錄可以同時有多個 TUI client。
_Avoid_: Agent（用來指終端介面）

**Tab**:
OpenCode TUI 中開啟的工作項目，對應一個 session；不是所有歷史 session 的集合。
_Avoid_: Session（用來泛指目前開啟的 tab）

**Session**:
OpenCode 的一份對話與執行紀錄，可以是 tab 對應的 session，也可以是其後代。

**Session 家族**:
一個 session 及其所有層級的後代 session；後代的運作狀態彙整到對應 tab，不另列為 subagent 項目。

**目錄群組**:
依正在執行的 TUI client 所在目錄彙整的 tab 集合；同一目錄的多個 TUI client 屬於同一群組。

**目錄身分**:
OpenCode 保存 tab 清單時使用的目錄識別；不因 symlink 指向相同位置或屬於同一 Git repository 而自行合併，不同 worktree 也保持獨立。

**狀態側欄**:
同一 tmux server 內跨 tmux session 共用的 OpenCode 工作狀態總覽；不同 tmux server 的總覽互相獨立。

**Tab 狀態**:
一個 tab 當下呈現的單一狀態，優先順序為等你核准、等你回答、執行中、失敗未讀、結束未讀、閒置。初版不把排隊中的 prompt 納入判斷。

**等你核准**:
Session 家族中至少有一個待核准的權限請求。

**等你回答**:
Session 家族中至少有一個待回答的問題，且沒有待核准的權限請求。

**執行中**:
Session 家族中至少有一個 session 正在執行，且沒有需要核准或回答的事項。

**未讀**:
Tab 本身最近一次執行結束尚未被 OpenCode 標記為已讀；此狀態由各 OpenCode client 共用，不另計側欄自己的已讀狀態。

**失敗未讀**:
Tab 本身最近一次執行失敗且未讀，並且家族沒有更高優先的狀態。

**結束未讀**:
Tab 本身最近一次執行已結束且未讀、結果不是失敗，並且家族沒有更高優先的狀態；包括成功與中斷，不代表成功完成。
_Avoid_: 完成未讀（容易被誤解為成功完成）

**閒置**:
已確認 tab 沒有任何更高優先的狀態；無法取得最新資料不算閒置。

**等你處理**:
等你核准與等你回答的統稱；畫面共用問號符號，但兩種狀態的內部判斷與優先順序仍然分開。

**狀態未知**:
因資料不可用而無法確認 tab 當下狀態；不代表閒置、執行失敗或最後已知狀態仍然有效。

**Session 不存在**:
Tab 仍然存在，但 OpenCode 已明確回報其對應 session 不存在；不同於暫時無法取得資料，不因此移除 tab。

**資料異常**:
狀態未知與 session 不存在的統稱；畫面共用驚嘆號符號，不在 tab 標題旁附加異常文字，也不等同於執行失敗。

**Tab scope**:
OpenCode TUI 的 tab 清單共享範圍，包含 cwd scope 與 global scope。

**Cwd scope**:
依 TUI 工作目錄分開保存 tab 清單的共享範圍。

**Global scope**:
不依 TUI 工作目錄區分、全域共享 tab 清單的範圍。

**插件模式**:
插件明確選用的 tab 清單來源，cwd 與 global 互斥；不代表自動偵測到的 TUI 有效 scope。

**Cwd 模式**:
只納入正在執行 TUI 的目錄所對應的 cwd tab 清單；同一目錄的多個 TUI client 共用一份清單。

**Global 模式**:
有 TUI 正在執行時，納入一份 global 共享 tab 清單，不以個別 session 的所在目錄過濾。
