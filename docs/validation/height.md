# 多 client 側欄高度適配驗證（issue #3）

結論：**能力成立。** 同一 tmux server、同一份全域狀態，每個 attached client 依自己的高度各自裁切；
頂部摘要含畫面外 tabs；「… 還有 N 個 tab」的 N 以 tab 計。不需任何額外綁定、pane 或 control-mode client。

## 環境

| 項目 | 值 |
| --- | --- |
| tmux | `tmux next-3.9`（Homebrew HEAD-05109d9；原始碼對照 tmux/tmux master `71afaaf`） |
| Go | go1.26.1 darwin/arm64 |
| OS | macOS arm64 |
| 隔離方式 | 全程 `tmux -L <唯一 socket> -f /dev/null`；結束時 `kill-server`。使用者預設 server／設定未被觸碰 |

「多個不同高度的 client」：外層隔離 server 的 pane 尺寸即為內層 client 的 tty 尺寸，內層 client 是真正的
`tmux attach`，各自擁有 `#{client_height}`。

## 重現

```sh
./tools/heightdemo/validate-height.sh           # 全部情境，輸出即 docs/validation/fixtures/height-run.txt
go test ./...                           # 純邏輯／輸出測試
```

需要 `tmux` next-3.9、Go、python3（僅用於以終端格寬度切齊輸出）。

## 查證出的 tmux 3.9 能力（man page + 原始碼 `status.c` / `format-draw.c` / `format.c`）

1. `side-status [left|right]`、`side-status-width`、`side-status-format`、`side-status-style` 存在。可用列數 =
   `client tty 高度 − status 行數`（`status_side_rows`）。**每個 client 各自展開 format**（`status_side_redraw`
   以 `format_create(c, …)` 展開，`ft->c` 是該 client），因此 `#{client_height}` 是逐 client 值。
2. `client_height`、`client_width` 在 side-status-format 內可用（實測）。`#{status}` 為 session 的 status 行數
   （`on`/`off`/`2..5`），可用 `#{e|-:…}` 與 `#{?#{==:…}}` 換算可用列數（實測 status=on/3/off）。
3. 原生 `list=on / list=left-marker / list=right-marker`：**實測可垂直裁切並以 marker 取代最後一個可見列**
   （見下），但它按「文字列」裁切、以 marker 文字取代最後一列，**無法**表達「完整 tab 行 + 剩餘 tab 數」，
   marker 也不能帶數字。故正式 UI 不採用，改用逐 client 選擇預先算好的版面。
4. 以 `#{@tos_sb_<avail>}` 讀取使用者選項（插件專屬前綴；使用者 POC 的 `@oc_side`、`@sb_*` 不使用也不修改）：值**只展開一次**（`#{@opt}`）。這是安全做法；`#{E:@opt}` / `#{T:@opt}`
   會再展開一次，使 `#{pane_id}`、`#(cmd)` 生效，**不可用於含使用者資料的內容**。
5. 對 option 值本身：`#[...]` 樣式有效，`##` 為字面 `#`；資料中的 `#[`、`#{`、`#(` 若已把 `#` 雙寫則只被畫成字面字元
   （實測：`#(touch /tmp/tos-h3-injected)` 未執行）。換行字元在 option 值內可用（一個 option 存整份多列版面）。
6. client 被 resize、session `status` 行數改變、option 變更，都會在下一次重繪重新展開（實測 B、G、H；
   publish 到畫面變化約 91 ms，不必等 `status-interval`）。

## 做法（已實測）

- 純函式 `render.Fit(groups, avail)`：保留標題＋分隔線，依群組／tab 順序累積「完整 tab 行（含必要群組標題與群組間空行）」，
  放不下時最後一列是 `… 還有 N 個 tab`（N = 未呈現的 tab 數）；整組被隱藏則不顯示其群組標題，只計入 N。
- `render.Publish`：為每個「版面會改變的可用高度」各算一份版面，存成全域使用者選項 `@tos_sb_<avail>`；
  `side-status-format` 為 `#{?#{e|>=:AVAIL,N},#{@tos_sb_N},…,#{@tos_sb_min}}`，其中
  `AVAIL = client_height − status 行數`。所有 client 共用同一份全域選項，**各自**依 `client_height` 選版面，
  不是以最小 client 高度裁切。
- 一次更新使用單一 `source-file -`（同一個 command queue，client 不會看到只更新一半的狀態）。

## 畫面證據（36 終端格，節錄自 `fixtures/height-run.txt`）

> #10 之後版面採 POC 幾何：左右各留 1 格邊距、tab 行縮排 2 格、標題與群組名稱貼在邊距內，可見字元最多 34 格（含邊距）。
> `fixtures/height-run.txt` 已用最新版面重跑；下方節錄保留當時的版面骨架說明，行數與 N 的推導仍然成立，
> 欄位位置以 fixture 為準。

資料：3 群組、8 tabs（`aicova/api` ●?·、`infra` ✗◆、`profcoo/api` !●·）。四個 client 同時 attached：

```
### c8（8 列，可用 7）                  ### c12（12 列，可用 11）
|◆ OPENCODE           ●2 ?1 ◆1 ✗1 !1│|  |◆ OPENCODE           ●2 ?1 ◆1 ✗1 !1│|
|───────────────────────────────────│|  |───────────────────────────────────│|
|aicova/api                    ●1 ?1│|  |aicova/api                    ●1 ?1│|
|● 修正登入逻辑 bug                 │|  |● 修正登入逻辑 bug                 │|
|? PR #42 #[fg=red]x #{pane_id} #(t…│|  |? PR #42 #[fg=red]x #{pane_id} #(t…│|
|· 寫單元測試                       │|  |· 寫單元測試                       │|
|… 還有 5 個 tab                    │|  |                                   │|
                                         |infra                         ◆1 ✗1│|
                                         |✗ terraform plan failed 這是一個非…│|
                                         |◆ k8s upgrade                      │|
                                         |… 還有 3 個 tab                    │|
```

- c8：可用 7 列＝標題、分隔線、群組標題、3 個 tab、footer；`infra`、`profcoo/api` 整組隱藏且不留標題，N=5（b1,b2,c1,c2,c3）。N 是 tab 數，不是 2 個群組也不是文字列數。
- c12：跨群組截斷，`infra` 完整顯示、`profcoo/api` 整組隱藏，N=3（c1,c2,c3）；與 c8 相同的頂部摘要。
- c16、c24：全部顯示，無 footer；兩者的頂部摘要與 c8 相同（`●2 ?1 ◆1 ✗1 !1`，含畫面外 tabs，idle 省略）。
- B（resize）：c12 改 24 列→變成完整顯示；c24 改 9 列→變成 c8 式裁切；其他 client 不變。
- D/D2：狀態成長（scenario=grown）後各 client 重新各自裁切；`c8` 13 列時在第一組後被截（`… 還有 2 個 tab`）。
- G：`status` 行數 1→3→off，同一 client 的可用列數 9→7→10，版面隨之改變。
- E：標題含 `#[fg=red]`、`#{pane_id}`、`#(touch …)`、`,}` 均被畫成字面字元，未執行任何指令。
- 36 格可讀性：中文（全形 2 格）、符號、右對齊摘要、`…` 截斷皆對齊在 35 格內容＋1 格分隔線。
- H：96 tabs／12 群組，publish→畫面 91 ms；c16 顯示完整 tab 行並 `… 還有 87 個 tab`，c8 `… 還有 88 個 tab`。
- F：`list-keys` 前後完全相同；server 內只有 1 個 pane；control-mode client 0。

### 原生 `list=on` 實測（為何不採用）

```
c7（7 列）                             c10（10 列）
|TITLE                              │|  |TITLE                              │|
|RULE                               │|  |RULE                               │|
|grpA                               │|  |grpA                               │|
| a1                                │|  | a1                                │|
| a2                                │|  | a2                                │|
|▼ more below                       │|  | a3                                │|
                                         |                                   │|
                                         |grpB                               │|
                                         |▼ more below                       │|
```

可逐 client 垂直裁切，但按文字列裁切、可能把群組標題留在最後一個可見列（`grpB` 之下無 tab），
且 marker 無法顯示 N。不滿足「只顯示完整 tab 行＋正確 N」。

## 限制與風險

- **版面數量隨高度線性成長**：每個「會改變版面的可用高度」一份選項。#10 以真實 tmux 實測發布成本
  （含版面計算與單一 `source-file -`）：

  | tabs | 版面份數 | 選項總量 | 版面計算 | 發布到 tmux |
  | --- | --- | --- | --- | --- |
  | 36 | 36 | 81 KB | 4 ms | 15 ms |
  | 96 | 96 | 514 KB | 25 ms | 43 ms |
  | 200 | 200 | 2.1 MB | 166 ms | 166 ms |
  | 400 | 400 | 8.4 MB | 936 ms | 651 ms |

  只有內容真的改變才會發布，所以成本只在有可見變化時付出；日常規模（數十個 tab）是百 KB 等級。
  **評估結論：初版不縮減為「只為目前 attached client 高度發布」。** 理由：該方案需要
  `client-attached`／`client-resized`／`client-detached` hook（實測三者都會觸發）並追蹤每個 session 的 `status` 行數；
  resize 到重新發布完成之間，client 會短暫以舊版面（或找不到對應高度的選項）繪製，違反「各 client 隨時正確」；
  現行「預先算好所有高度、由 format 選取」沒有任何時序相依。若實際使用超過約 200 個 tab 再回頭處理。
  `set-option` 一次以 `source-file -` 送出可行；改用 argv 傳遞會 `command too long`（約 16 KB）。
- tmux 沒有「逐 client」寫入選項的能力；因此「各 client 各自呈現」靠 format 內以 `#{client_height}` 選擇。
- 36 格＝`side-status-width 36`，其中最右 1 格為分隔線，實際內容寬度 35；`Render` 以 35 計。
- **寬度計算已與 tmux 對照（#10）**：改用 `internal/cellwidth`，逐碼點寬度表由 tmux next-3.9（utf8proc）對 U+00A0–U+10FFFF
  全碼點掃描產生（481 個範圍：344 個零寬、137 個寬 2），群集合併規則對應 `screen_write_combine`。
  注意兩點：(1) tmux 的 `#{w:}` 是逐碼點加總，會把 `👨‍👩‍👧` 量成 6，但實際繪製只佔 2 格，所以不能拿 `#{w:}` 當準則；
  (2) 繪製行為有幾個不直覺的規則，已用 attached client 實測並寫成測試：ZWJ／VS16／組合字元併入前一格、
  ZWJ 後接 ASCII 不併入（`a‍b` 是 2 格）、非膚色基底後接修飾符獨立成格（`😀🏽` 是 4 格）、國旗只合併前兩個、
  Hangul jamo 依 初聲→中聲→終聲 組合並丟棄多餘字元、`U+3164` 不畫、行首零寬字元不畫。
  `TestWidthAgreesWithARealAttachedTmuxClient` 以隔離 tmux 的真實 client 逐一驗證 43 個樣本。
- 未驗證 `side-status right`、`status-position top`（原始碼上可用列數同樣由 `status_side_rows` 決定，預期相同）。
- #3 本身不涵蓋正式資料來源；`tools/heightdemo` 是驗證用工具（以受控資料與 `-socket` 隔離 server 展示版面，不屬於正式側欄），`internal/render` 已由 #10 成為正式排版／渲染。

## 受影響需求

無。規格「高度不足時保留頂部摘要、完整 tab 行、`… 還有 N 個 tab`」「各 client 自己的尺寸」在 tmux next-3.9 成立，
但實作方式為「逐 client 由 format 選取預先算好的版面」，不是原生 list 樣式。
