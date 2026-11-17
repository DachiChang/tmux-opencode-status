# Issue tracker: GitHub

本專案的 issues 與 specs 存放於 DachiChang/tmux-opencode-status 的 GitHub Issues。
使用 gh CLI 操作，從儲存庫目錄執行。

## 操作慣例

- 首次建立議題前，執行 `gh repo view --json nameWithOwner` 確認目標。
  僅在使用者自己的儲存庫建立議題；若目標不符，先向使用者確認。
- 建立：`gh issue create --title "..." --body-file -`，以 heredoc 傳入多行內容。
- 讀取：`gh issue view <number> --json number,title,body,labels,comments`。
- 列出：`gh issue list --state open --json number,title,body,labels,comments`；
  視需要加上 --label 或調整 --state。
- 留言：`gh issue comment <number> --body "..."`。
- 標籤：`gh issue edit <number> --add-label "..."` 或 --remove-label。
- 關閉：先留言解釋，再執行 `gh issue close <number>`；
  已關閉的議題也應用獨立留言指令補充說明。

## 子議題與相依性

優先使用 GitHub 原生 sub-issues 與 issue dependencies。
使用前以 gh help 確認目前版本支援的指令。

若無法使用原生關聯：
- 子議題本文頂端寫 `Part of #<parent>`，並在父議題加入子議題 task list。
- 被阻擋議題本文頂端寫 `Blocked by: #<n>, #<n>`。
- 所有阻擋議題皆關閉後，該議題才可開始。

## Pull requests as a triage surface

**PRs as a request surface: no.**

## 技能指令的對應

「publish to the issue tracker」表示建立 GitHub issue。
「fetch the relevant ticket」表示讀取對應的 GitHub issue。

## Wayfinding

- Map：單一議題，標籤為 wayfinder:map，本文包含 Notes、Decisions-so-far、Fog。
- Child ticket：以 sub-issue 連至 map；不支援時使用上述替代關聯。
  標籤為 wayfinder:<type>，type 為 research、prototype、grilling 或 task。
- Frontier：依 map 順序選取第一個未關閉、無負責人且無未關閉 blocker 的子議題。
- Claim：以 `gh issue edit <n> --add-assignee @me` 作為該工作階段的首次寫入。
- Resolve：先留言答案，再關閉議題，最後將摘要與連結加入 map 的 Decisions-so-far。
