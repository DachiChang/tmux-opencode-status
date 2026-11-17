# OpenCode / tmux 資料契約（能力驗證）

對應 issue #2。本文件記錄後續實作所依賴的外部契約，並標明每項資訊的證據等級。

## 驗證基線

| 項目 | 版本 |
| --- | --- |
| tmux | `next-3.9`（`tmux -V`） |
| OpenCode | `v2.3.0`（`opencode --version`），Homebrew `opencode-v2` 2.3.0，macOS arm64 |
| 服務 | 使用者的本機共用背景服務 `opencode serve --service`（唯讀探測） |
| 沙盒服務 | 同一個 2.3.0 執行檔，`XDG_*` 全部指向暫存目錄的獨立 `opencode serve`（寫入型實驗只在此進行） |
| Go | go1.26.1 |

證據等級：**[實測]** 本機真實環境、**[沙盒]** 隔離沙盒服務、**[文件]** 官方文件／v2.3.0 原始碼（`anomalyco/opencode` tag `v2.3.0`）、**[未確認]**。
本文件不混用 POC 的 next-3.9／舊服務／dev 原始碼：原始碼引用一律取 `v2.3.0` tag。

## 唯讀與隔離方式

- 對使用者環境只做 GET、讀檔、`ps`／`lsof`／`tmux list-*`。開始與結束時比對 `service.json`、`tabs.json` 雜湊一致，服務 PID 未變。
- 需要寫入才能產生的情境（待核准、待回答、失敗、中斷、已讀、刪除）全部在沙盒服務（獨立 `XDG_*`、獨立連接埠、獨立密碼）完成，沙盒 `tabs.json` 也在沙盒狀態目錄。
- tmux 實驗用 `tmux -L <隔離 socket> -f /dev/null`，結束後 kill。
- 結論：插件程式只需要下列 GET 端點，不需要任何寫入端點。

## 1. 辨識互動 TUI

**結論：用「pane tty 的前景行程組」的 argv 辨識，不依賴 pane title、不掃畫面。**

| 做法 | 結果 |
| --- | --- |
| `pane_current_command == opencode` | 不足：`serve`、`run`、`mini`、`session list` 也是 `opencode` [實測] |
| `alternate_on == 1` | 不足：`opencode session list`（pager）也進 alternate screen；`mini` 不進 [實測] |
| `pane_title == OpenCode` | 不採用（見下）。TUI 會設定 title，但 `mini`／`run` 不會，且 title 取決於終端設定 [實測] |
| `pane_pid` 的子行程 | 不採用：`pane_pid` 是 shell，且 TUI 為 shell 的前景子行程 |
| **前景行程 argv** | 可靠 [實測]：見下表 |

取得方式（皆唯讀、無需特權）：

1. `tmux list-panes -a -F '#{pane_tty} …'`
2. `ps -t <tty> -o pid=,pgid=,tpgid=`：`pgid == tpgid` 的行程屬於前景行程組。
3. argv：macOS `sysctl` `KERN_PROCARGS2`（`[CTL_KERN=1, 49, pid]`）可得**精確 argv**（含空白的路徑不會被 `ps` 攤平）。`ps -ww -o args=` 會丟失空白邊界。
4. cwd：`lsof -a -p <pid> -d cwd -Fn`（單次約 15 ms）。

判斷規則（`internal/contract.IsTUICandidate`，由 `testdata/opencode-2.3.0/panes.json` 回歸）：
`basename(argv[0]) == "opencode"`，且去掉旗標後第一個位置參數**不是**子命令（`upgrade update uninstall acp api debug auth mcp plugin models stats mini run session service reload pair serve`），且沒有 `--help/-h/--version/-v/--wizard`。會吃值的旗標：`--server --session/-s --prompt --log-level --completions`。

| 場景 | 前景 argv | `alternate_on` | TUI 候選 |
| --- | --- | --- | --- |
| 預設 | `opencode` | 1 | 是 |
| 指定服務 | `opencode --server URL` | 1 | 是 |
| 指定目錄 | `opencode … /path` | 1 | 是（位置參數是目錄） |
| 目錄為 `.` | `opencode … .` | 1 | 是 |
| 續接 | `opencode … -c` / `-s ID` | 1 | 是 |
| 獨立服務 | `opencode --standalone` | 1 | 是 |
| mini | `opencode … mini` | **0** | 否 |
| run | `opencode … run …` | **0** | 否 |
| serve | `opencode serve …` | 0 | 否 |
| session list | `opencode … session list` | **1** | 否 |
| 一般 shell | `zsh -f` | 0 | 否 |

注意：
- `alternate_on` 可當**輔助**線索（TUI 必為 1），但不能單獨作證據，也不能當排除條件以外的依據。
- `pane_current_command` 在 shell 剛啟動、TUI 尚未取得前景時仍是 `zsh`；插件需以輪詢／事件補捉轉換時刻（時序目標屬後續票）。
- linked window：同一 pane 被 `link-window` 到多個 tmux session 時，`list-panes -a` 會重複列出同一 `pane_id`（實測 `sb:0` 與 `sb:90` 都列 `%0`）；必須以 `pane_id` 去重，才能滿足「同目錄多 pane／linked window 不重複 tab」。
- 未確認：以 wrapper script／`exec env … opencode` 啟動時（argv[0] 仍為 `opencode`，已在隔離環境驗證可辨識）；若使用者以別名改名可執行檔則不在承諾範圍。

### terminal.title

OpenCode TUI 會把 pane title 設為 `OpenCode`；`mini`／`run` 不會。**本契約完全不使用 title**。tmux 的 `allow-set-title` 預設為 on，但這不是產品前提，也沒有任何驗證步驟需要它。

## 2. tab 清單來源（tabs.json）

位置 [實測][文件]：`${XDG_STATE_HOME:-~/.local/state}/opencode/latest/tui/tabs.json`
（`latest` 是 channel；`createStorage(root, channel)` → `<state>/<channel>/tui/<key>.json`，key = `tabs`）。

格式 [實測][文件 `session-tabs.tsx`]：

```json
{"global":{"tabs":[{"sessionID":"ses_…","title":"…"}],"unread":{}},
 "cwd":{"/abs/dir":{"tabs":[{"sessionID":"ses_…","title":"…"}],"unread":{}}}}
```

- `tabs` 順序即 TUI tab 順序。
- `unread` 在 v2.3.0 **恆為空物件**（原始碼註解：Kept empty for rollback compatibility）。**不要讀它**；未讀以 session API 的 `time.idle/viewed` 為準。
- `title` 只是備援（TUI 優先用 API 的 `session.title`）。它是使用者資料，診斷輸出不得包含。
- 其他同目錄 `highlights.json`、`session-list.json`、`session-terminal-selection.json` 與本需求無關。

### 選擇來源（cwd / global）

- cwd 模式：`cwd[<TUI 行程 cwd>]`。TUI 以 `process.cwd()` 作 key [文件 `app.tsx`]。我們取**前景行程的實際 cwd**（`lsof … -d cwd`），不是 `pane_current_path`，也不是命令列上的目錄參數（參數只會改變行程 cwd）。
- global 模式：`global.tabs`，與目錄無關。
- 實測 [實測/沙盒]：同一個 TUI 以 `opencode … /dir` 啟動，key 為行程 cwd 的**逐字**字串；key 不做 symlink／大小寫／結尾斜線正規化。實作必須用**字串精確比對**（目錄身分沿用 OpenCode，符合 GLOSSARY）。
- 實測 [沙盒]：TUI 設定 `tabs.scope` 是**各 TUI 自己**的設定（預設 `cwd`，設定檔 `tabs.scope`，或 `OPENCODE_CLI_CONFIG_CONTENT`）；插件依明確模式選來源，**不讀取**各 client 的覆寫（ADR 0001）。
- 缺少目錄項目 = 「有 TUI、無 tab」，與「檔案不可讀／格式不相容」必須區分（`ParseTabs` 對後者回傳錯誤）。

### 更新行為（原子性）

- 寫入流程 [文件 `util/persistence.ts` `writeJsonAtomic`]：寫 `tabs.json.<pid>.<uuid>.tmp` → `rename`。另有檔案鎖（`<state>/<channel>/locks/`，`Flock.withLock`）。
- 實測 [沙盒]：每次更新後 `tabs.json` 的 inode 改變（例：318609465 → 318610001）；以 80 次 50 ms 取樣觀察到短暫存在的 `.tmp` 檔。
- 含意：**不能以 inode 或單檔 watch 追蹤**；用目錄層級 watch（或定期讀取）並以內容雜湊判斷變化。讀到半寫入內容的風險由 rename 原子性排除，但**仍要以解析錯誤保守處理**（保留最後可用清單）。
- TUI 自己以 50 ms debounce 重載目錄變化 [文件 `storage.tsx`]。
- 未確認：Linux／Windows 行為（不在承諾範圍）。

## 3. 服務探索與認證

**探索**：`${XDG_STATE_HOME:-~/.local/state}/opencode/service.json`，權限 `-rw-------` [實測]。內容 [實測]：

```json
{"id":"<uuid>","version":"2.3.0","url":"http://0.0.0.0:49374","pid":4118,"password":"<secret>"}
```

- `url` 的 host 可能是 `0.0.0.0`（監聽位址）。**連線時改用 `127.0.0.1`**（實測 `http://0.0.0.0:PORT` 在本機也可連線，但不應依賴）。`GET /api/info` 的 `urls[]` 會列出實際可用位址。
- 驗證服務身分 [文件 `service-probe.ts`]：`GET /api/info` 回傳的 `pid` 必須等於 `service.json.pid`，`version` 必須相符；`/api/info` 回 404 代表健康協定不相容；500 = 啟動失敗；其他非 2xx = 啟動中。
- **每次重連重新讀取 `service.json`**：服務重啟後 port／password／pid 都可能改變（不永久快取）。
- 找不到檔案或服務未回應 = 服務不可連線（與 tab 清單不可讀是不同錯誤）。

**認證** [實測]：HTTP Basic，帳號固定 `opencode`，密碼 = `service.json.password`。
- `Authorization: Bearer <pw>` → 401。`Basic ":<pw>"`（空帳號）→ 401。`Basic "opencode:<pw>"` → 200。
- `/doc` 可免認證（HTML）；`/api/*` 全部需要認證，未認證回 `{"_tag":"UnauthorizedError"}` 401。
- 憑證不得輸出到診斷、日誌或 fixture（`TestFixturesAreDeidentified` 會檢查）。

**服務生命週期** [沙盒]：獨立沙盒服務在無活動一段時間後會自行結束（本次驗證中發生 3 次）。使用者的共用服務也可能在無 client 時結束 → 插件必須把「服務消失」當正常情況、自動重試，且**不得啟動／重啟服務**（`opencode service start/restart` 是寫入操作，禁止）。

## 4. API 端點（插件只用 GET）

OpenAPI 取自 `GET /openapi.json`（需認證）。以下均 [實測] 並與原始碼相符。

| 目的 | 端點 | 備註 |
| --- | --- | --- |
| 健康／身分 | `GET /api/info` | `{version,pid,hostname,urls[],paths,capabilities}` |
| session 資訊 | `GET /api/session/{id}` | `{data: Session.Info}`；不存在 → **404** `{"_tag":"SessionNotFoundError","sessionID","message"}` |
| 子層列表 | `GET /api/session?parentID={id}&limit={n}[&cursor={c}]` | 分頁，見下 |
| 執行中 | `GET /api/session/active` | `{"data":{"<id>":{"type":"running"}}}`；**不在其中 = 非執行中** |
| 待核准（單一 session） | `GET /api/session/{id}/permission` | `{data:[{id,sessionID,action,resources[],save[],…}]}`；session 不存在 → 404 |
| 待核准（依目錄） | `GET /api/permission/request?location[directory]=<dir>` | deepObject；未帶 location 則用服務預設位置（非 `/`），**不能依賴它涵蓋所有目錄** |
| 待回答（單一 session） | `GET /api/session/{id}/form` | OpenCode 把「問題」稱為 **form**；`{data:[{id,sessionID,title,fields[]}]}` |
| 待回答（依目錄） | `GET /api/form?location[directory]=<dir>` | 同上 |
| 事件 | `GET /api/event?types=…` | SSE，見下 |

**不使用**（寫入或非必要）：`POST /api/session/{id}/view`（標記已讀）、`…/permission/{id}/reply`、`…/form/…/reply`、`/prompt`、`/interrupt`、`DELETE …`、`/api/session/{id}/inbox`（初版不處理排隊 prompt）。

### Session.Info 關鍵欄位

```text
id, parentID?, projectID, location.directory, title,
outcome?: "succeeded" | "failed" | "interrupted",      // 曾結束過一次執行才有
time: { created, updated, busy?, idle?, viewed?, archived? }   // epoch 毫秒
```

- **未讀** [文件 `session-tabs.tsx` `isUnread`][沙盒]：`time.idle` 存在 **且**（`time.viewed` 缺漏 **或** `time.idle > time.viewed`）。只看 tab 本身（root）；失敗 = `outcome == "failed"`；中斷（`interrupted`）與成功都歸「結束未讀」。
- `busy` 不是執行中判斷依據（只是上次開始時間）。執行中請用 `/api/session/active`。
- 重新執行時：新一輪開始後 `outcome` 仍保留上一輪結果、`idle` 仍是上一輪時間；`/api/session/active` 才是目前執行狀態（實測 `s09-running`：active 含該 session 且 `outcome` 暫時還是 `succeeded`）。因此判定順序必須是「待核准 → 待回答 → active → 未讀」。
- 已封存（`time.archived`）的子層：`GET /api/session?parentID=…` **預設仍會列出**；`archived=false`／`true` 可篩選。家族發現使用不帶 `archived` 的預設呼叫即可涵蓋。
- TUI 判定「busy」時除了 `running` 還會把 inbox 中非 synthetic 的待處理項目算進去 [文件]；初版明確不處理 inbox（父 spec），**已知差異**：排隊 prompt 交接時可能短暫呈現結束狀態。

### 分頁

`GET /api/session?parentID=…&limit=N` 回 `{data:[…], cursor:{previous, next}}`。**實測陷阱**：`limit=1` 逐頁走訪時，最後一個**非空頁**仍帶 `cursor.next`；要再多讀一頁得到 `data:[]` 且 `next:null` 才算結束。→ 結束條件是「空頁」，不能只看 `next`（`DiscoverFamily` 如此實作，測試的 fake 重現此行為）。預設 `limit` 為 50；`limit=0` → 400。

### 家族發現（中途啟動）

不能只查 tab 本身，也不能依賴 `/api/session/active`（它只列執行中的）。方法（`contract.DiscoverFamily`）：

1. `GET /api/session/{tab}`（404 → 該 tab「session 不存在」）。
2. 對家族內每個 session：`GET /api/session?parentID={id}&limit=N`，**分頁走到空頁**，把子層加入佇列（廣度優先，所有層級）。
3. 對家族內每個 session：`GET …/permission`、`GET …/form`。
4. `GET /api/session/active`。
5. 任一步失敗 = 整個 tab 資料不完整（回傳錯誤，呼叫端呈現「狀態未知」），**不得當作空集合**。
6. 家族只往下追後代；不追祖先、不納入兄弟。

沙盒驗證（`bootstrap.json`）：R → C → G（孫輩）與 R → C2；G 有待核准、C2 有待回答；只查 R 本身得到 0 筆，家族發現得到 `WaitingApproval`。

TUI 自己的做法 [文件 `data.tsx`/`client/solid/data.ts`]：以 `session.sync(id,{children:true})` 讀直接子層；`server.connected` 時 `session.active()` 取得 snapshot，並補讀「執行中但尚無 info」的 session。我們用同樣概念，但對所有層級遞迴。

## 5. SSE 事件（GET /api/event）

**傳輸** [實測]：
- `Content-Type: text/event-stream`，HTTP/1.1 chunked，`Cache-Control: no-cache, no-transform`。
- **第一個事件永遠是** `data: {"id":"evt_…","type":"server.connected","data":{}}`，接著立刻一個 `: heartbeat`。
- 心跳 = SSE 註解行 `: heartbeat`，**每 15 秒**一次（實測 1794903360 / …375 / …390 / …405 / …420，間隔 15 s）；它不是 `data:` 事件。無聲斷線偵測：> 2~3 個心跳間隔（建議 45 s）沒收到任何位元組即視為斷線。
  - #9 實作：`ocsrc.DefaultIdleTimeout = 45s`；SSE 讀到任何位元組（含心跳）即重置計時，逾時取消連線並回報 `ErrIdleTimeout`，常駐程式把該連線的所有 tab 視為未知、顯示服務斷線提示，之後依重試間隔重新探索並完整重讀。測試／診斷可用 `-idle-timeout` 縮短。
- 行尾 `\n`；解析器仍應容忍 `\r\n`。
- 事件 ID 為 `evt_<hex>`；**不支援重播**：送 `Last-Event-ID` 不會補送斷線期間的事件（實測：新連線只收到 `server.connected`）。官方描述：「Volatile by contract: a slow consumer overflows and fails the stream, and events during disconnection are missed.」
- `?types=a&types=b`（重複參數）過濾；**未知 type → 400** `InvalidRequestError`（會列出所有合法值）。`server.connected` 與心跳不受過濾影響，永遠送出。
- 不帶 `types` 時流量很大（含 `session.text.delta`、`session.tool.*`、`session.usage.updated`、`shell.*`、`project.updated` 等），**務必過濾**。

**插件需要的事件型別**（`types=` 清單）：

```text
session.created session.deleted session.moved session.renamed session.archived session.unarchived session.forked
session.execution.started session.execution.succeeded session.execution.failed session.execution.interrupted
session.viewed
permission.asked permission.replied
form.created form.replied form.cancelled
session.inbox.enqueued session.inbox.delivered session.inbox.cancelled   // 僅供觀察；初版不處理 inbox
location.shutdown
```

**欄位** [實測，見 `testdata/opencode-2.3.0/events.sse`]：

| 事件 | 重點欄位 |
| --- | --- |
| `session.created` | `data.sessionID, data.parentID?`（家族關係可即時建立）、`data.location.directory` |
| `session.execution.started` | `data.sessionID` |
| `session.execution.succeeded` | `data.sessionID`；`created` == REST `time.idle`（已驗證相等） |
| `session.execution.failed` | `data.sessionID, data.error{type,message}`；`created` == `time.idle` |
| `session.execution.interrupted` | `data.sessionID, data.reason: user\|shutdown\|superseded\|inactivity`；`created` == `time.idle` |
| `session.viewed` | `data.sessionID, data.idle`（被確認已讀的 idle 時間戳）；REST `time.viewed` 同值 |
| `permission.asked` | `data{id, sessionID, action, resources[], save[]}`（無 `durable`） |
| `permission.replied` | `data{sessionID, requestID, reply: once\|always\|reject}` |
| `form.created` | `data.form{id, sessionID, title, fields[]}` |
| `form.replied` | `data{id, sessionID, answer}`（沙盒實測） |
| `form.cancelled` | `data{id, sessionID}` |
| `session.deleted` | `data.sessionID` |

`session.execution.*`、`session.viewed`、`session.deleted` 等**持久事件**帶 `durable{aggregateID, seq, version}`；`permission.*`、`form.*` 為**暫態事件**（無 `durable`）。

**不可依賴的事件**：
- `session.status` 與 `session.idle`：OpenAPI 宣告，但在本次所有錄製（使用者服務被動觀察、沙盒多情境、含 `types=session.status` 單獨過濾）中**從未出現**。TUI 也不用它們（改用 `session.execution.*`）。官方標為 deprecated（`session.idle`）。**不得依賴**。
- `session.execution.interrupted` 且 `reason == "shutdown"`：TUI 忽略該事件（不改變 idle 訊息）；它代表服務關閉而不是使用者中斷。實作應與 TUI 一致視為非結束。（來自文件；[未確認] 實測，因本次不能關閉使用者服務。）
  - #9 實作：`Tracker` 忽略此事件（不結束執行、不改未讀），並觸發全部 tab 的重讀，最終狀態以 REST 為準；其他 reason（`user`／`superseded`／`inactivity`）照常視為結束（中斷＝結束未讀，不算失敗）。與 spec 一致，無偏離。
- 斷線期間的任何事件。
- `permission.replied` 在**重啟服務前未回覆**的請求：沙盒重啟後 `GET /api/permission/request` 回空 → 待處理請求**不持久**（服務重啟會失去）；所以重連後必須以 REST 重讀，且不要對舊請求保留狀態。

### 重連後同步與交錯

- 每次連線以 `server.connected` 為界：**收到它就視為事件推導狀態失效**，立即開始完整重讀（家族發現 + active + 權限 + 表單）。在重讀完成前不得顯示舊狀態為最新事實。
- 同步期間已收到事件的 session，不被較舊的 snapshot 覆蓋（`Tracker.ApplySnapshot` 的 `touched` 規則，已測）。
- 這是對 TUI 做法的對齊：TUI 在 `server.connected` 時以 `session.active()` 的 snapshot 為基準，並讓「hydration 期間收到的生命週期事件」勝過 snapshot [文件 `client/solid/data.ts`]。

## 6. 已讀、失敗、中斷、待處理的實測對照

沙盒 session（`testdata/opencode-2.3.0/snapshots.json` 的 10 個階段）驗證每個階段推導出的狀態：

| 階段 | 資料 | 推導 |
| --- | --- | --- |
| 建立 R→C→G | 皆無 idle | 閒置 |
| 孫輩 G 有 permission | `GET /api/session/G/permission` 非空；R、C 為空 | R：等你核准 |
| 另有 C 的 form | `GET /api/session/C/form` 非空 | R：等你核准（優先） |
| permission 已回覆、form 未處理 | | R：等你回答 |
| 全部處理完 | | R：閒置 |
| 失敗 | `outcome=failed`，`idle` 有、`viewed` 無 | 失敗未讀 |
| 成功 | `outcome=succeeded`，`idle` 有、`viewed` 無 | 結束未讀 |
| 已讀（`session.viewed`） | `viewed == idle` | 閒置 |
| 中斷 | `outcome=interrupted`，`idle` 有、`viewed` 無；事件 `reason:user` | 結束未讀（不算失敗） |
| 刪除 | `GET` → 404 SessionNotFoundError；收到 `session.deleted` | session 不存在 |

## 7. 陷阱彙整

1. 只看 `pane_current_command`／`alternate_on`／title 都會誤判（`session list` 進 alternate screen；mini/run/serve 同為 `opencode`）。
2. `pane_pid` 是 shell；要看 tty 前景行程組。`ps args` 會攤平含空白的路徑，用 `KERN_PROCARGS2`。
3. TUI 的 key 是**行程 cwd**，不是命令列目錄參數；也不要正規化目錄。
4. `tabs.json` 以 rename 更新，inode 會變；目錄層級 watch。`unread` 恆空，別讀。
5. `service.json` 的 `url` 可能是 `0.0.0.0`；密碼只用於 Basic（帳號 `opencode`）。每次重連重讀。
6. 分頁用「空頁」結束；`limit=0` 400。
7. `session.status`／`session.idle` 不會出現；`session.execution.interrupted(reason=shutdown)` 不是使用者中斷。
8. `/api/session/active` 才是執行中來源；`outcome`／`idle` 是上一輪結果，重新執行期間仍在。
9. `/api/permission/request` 與 `/api/form` 依 location；未帶時用服務預設位置，不涵蓋全部。逐 session 查詢（`/api/session/{id}/permission|form`）才能保證完整。
10. 待處理請求不持久：服務重啟後消失；服務重啟 = 完整重讀。
11. 未知 `types` → 400；不帶 `types` 流量大。
12. 無活動時服務可能自行結束；插件不得啟動／重啟。

## 8. 與父規格的相容性

未發現無法符合父 spec 的項目：

- TUI 可靠辨識 → 可（argv 前景行程組；不依賴 title）。
- cwd／global 來源與目錄身分 → 可（`tabs.json`）。
- 狀態、未讀、權限、問題、後代 → 可（REST 全為 GET）。
- SSE 契約 → 可（需 `server.connected` 觸發完整重讀；`session.status` 不可用已被規避）。
- 受阻票：無。

**需後續票處理／已知風險**：
- 非公開格式風險：`tabs.json` 與 `service.json` 欄位集中在 `internal/contract`（本票的 fixture 作回歸）。
- 排隊 prompt（inbox）不處理（父 spec 已接受）。
- 前景行程 cwd 的取得依賴 `lsof`（macOS 內建）；時序（1 秒／10 秒）屬後續實測票。
- `permission.asked` 在伺服器端「回覆前服務重啟」會遺失，插件重連後以 REST 重讀即可，但無法得知消失的原因。

## 9. 重放與驗證

```sh
go test ./...                    # 契約回歸（fixture 離線重放）
python3 scripts/contract/probe_panes.py <tmux-socket> [前綴=替換]   # 對真實 tmux 做唯讀 pane 探針
python3 scripts/contract/deidentify.py <scenario.json> <scenario.sse> <bootstrap.json> testdata/opencode-2.3.0
```

重新錄製（只在隔離沙盒）：以暫存 `XDG_DATA_HOME/XDG_STATE_HOME/XDG_CONFIG_HOME/XDG_CACHE_HOME` 與 `OPENCODE_SERVER_PASSWORD` 啟動獨立 `opencode serve --hostname 127.0.0.1 --port <p>`，
建立家族、請求權限／問題、執行、回覆／已讀／中斷，錄下 REST 回應與 `GET /api/event` 串流，再以 `deidentify.py` 轉成 fixture。**不得對使用者服務執行任何寫入端點。**

## 10. 尚未確認

- `session.execution.interrupted(reason=shutdown)` 的實際序列（需關閉服務，被禁止）。
- 多 attached client 的高度適配（屬 UI 票）。
- 1 秒／10 秒／45 秒的實測量測（屬後續驗收票）。
- `tabs.mode=off`（或 `HERDR_ENV=1` 時 `auto`）會讓該 TUI 不登記 tab [文件 `config/index.tsx`、`session-tabs.tsx` `enabled()`]；此時「有 TUI、無 tab」為預期結果，且與「清單陳舊」無法區分（未實測）。屬使用者設定責任，列入診斷說明即可。
- 停在 home 畫面（尚未進入任何 session）的 TUI **不會**在 `tabs.json` 建立該目錄的項目；只有進入／續接 session（`-c`、`-s <id>`、新建後路由到 session）才會登記 [沙盒實測：`dir with space` 的 TUI 停在 home，`cwd` 無此 key；`-c` 的 TUI 有]。因此「有 TUI 但 cwd 無項目」是正常的「無 tab」。
