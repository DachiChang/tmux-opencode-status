## Agent skills

### Issue tracker

Issues 與 specs 使用 GitHub Issues；操作前閱讀 `docs/agents/issue-tracker.md`。

### Triage labels

使用五個預設 triage 標籤；分類議題前閱讀 `docs/agents/triage-labels.md`。

### Domain docs

採 single-context；探索程式碼前閱讀 `docs/agents/domain.md`。

## 工作流程規則

- grill 訪談中只更新 GLOSSARY.md 與 ADR；spec 只由 /to-spec 產生。
- prototype 在獨立 worktree 製作，分支名稱為 `prototype/<name>`，完成後 commit，不 merge 回 main；
  確認後移除 worktree，並關閉 prototype 啟動的服務。
- 實作票的方向臨時變更與 spec 不同時，先暫停並告知使用者：引用 spec 原句，說明新方向、差異及受影響的未完成票。
  同意修改後，回頭更新 spec ，使得 spec 和新方向的實作能夠對齊。
- 實作分支 commit 後，確認建立時的來源分支（Base Branch），以 `git merge-tree --write-tree --name-only <base-branch> HEAD` 預檢能否乾淨 merge 回來源分支
  （exit 0 無衝突；exit 1 列出衝突檔），並回報結果；無法確認來源分支或發生衝突時，先詢問使用者，再 merge main 解衝突。

