# tmux-opencode-status 詳細指南

[English](guide.md) · 繁體中文 · [快速入門](../README.zh-TW.md)

在 tmux 3.9 的原生 **side-status** 顯示同一 tmux server 中所有 OpenCode TUI 的 tab 狀態（等你核准／回答、執行中、失敗未讀、結束未讀、閒置、資料異常）。
唯讀資訊總覽：不安裝 OpenCode plugin、不修改 OpenCode 設定、不啟動或重啟服務。插件在 tmux 中的綁定只有三項：接管 root 表的 `MouseDown1SideStatus`（見「點擊跳轉」）、一個 prefix 觸發鍵（預設 `A`）與 `opencode-status-jump` 按鍵表（見「鍵盤跳轉」）。

## 需求

- tmux **3.9**（需要 `side-status`、`side-status-width`、`side-status-format`；缺少時明確失敗，不改用其他顯示方式）。
- macOS（Apple Silicon `arm64`、Intel `x86_64`）。一般使用者**不需要 Go**。
- OpenCode 標準 **latest** channel、**預設資料位置**（`$XDG_STATE_HOME` 或 `~/.local/state` 下的 `opencode/`）與本機共用背景服務。
  不支援其他 release channel、自訂資料位置、遠端或獨立私有服務，也不彙整多套資料環境。

## 安裝細節

```tmux
set -g @plugin 'DachiChang/tmux-opencode-status'
# 選用：cwd（預設）或 global
# set -g @opencode-status-mode global
# 選用：覆寫配色，見下方「配色」
# set -g @opencode-status-color-project colour253
```

`prefix + I` 安裝。TPM 會執行插件根目錄的 `tmux-opencode-status.tmux`，它會：

1. 確認 tmux 具備必要能力；
2. 依插件的 `VERSION` 與 `uname -m` 從 GitHub Release 下載對應架構的 `tmux-opencode-status_<版本>_darwin_<arch>.tar.gz` 與 `SHA256SUMS`；
3. 驗證 SHA-256、封存內容與執行檔架構，通過才放到插件的 `bin/<版本>-darwin-<arch>/`；
4. 執行 `tmux-opencode-status start -show`。

安裝目錄中的架構名稱在 Apple Silicon 上為 `arm64`，在 Intel macOS（`x86_64`）上為 `amd64`。

已安裝且校驗相符時不再下載。**任何一步失敗（下載失敗、驗證失敗、架構不支援、缺少 tmux 能力）都會明確報錯**：
訊息寫到 stderr，並以 `tmux display-message` 顯示；**不會靜默改用 `go build`，也不會換用其他架構的執行檔**。

### 預設行為

- 預設放在**左側**，寬 **36 格**（終端格寬，不是字元數）。首次載入打開側欄；之後重載、交接都不會把你已隱藏的側欄重新打開（每個 tmux server 只打開一次）。
- **已知限制：無法尊重「你在設定檔明確寫了 `set -g side-status off`」。** tmux 的 `side-status` 預設值就是 `off`，
  `show-options`（含 `-g`、`-q`、`-A`、`-H`）與格式變數都不會標示「這是預設值還是被明確設定」（以 tmux next-3.9 實測：
  未設定、`set -g side-status off`、`set -gu side-status` 三種狀態的輸出完全相同；原始碼 `options-table.c` 中預設為 `default_num = 0`）。
  所以插件啟動（`start -show`）的**第一次**載入無法分辨你是「從未打開過」還是「明確想隱藏」，會把側欄打開一次。
  之後（同一個 tmux server 的重載、交接、重啟常駐程式）都不會再替你打開。若你不想在啟動時打開：
  不要使用 `-show`（例如在 TPM 以外自行呼叫 `start`，不帶 `-show`），再用 `tmux set -g side-status left` 自行顯示。
- 重複載入（`tmux source-file`、TPM 更新）是冪等的：每個 tmux socket 只有一個常駐實例；設定或執行檔不同時安全交接。`start` 成功只代表有實例持有鎖並已記錄有效設定，不代表側欄已經顯示。
- tmux server 結束時常駐程式自動退出並清理本插件資源。

## 跳轉與按鍵綁定

### 點擊跳轉

點擊側欄中某個**目錄群組**的任一列（目錄列、其下的 tab 列、群組沒有 tab 時的提示列），client 立即切到該群組 **TUI client 所在的 pane**：
session、window、pane 一次到位，可跨 tmux session，目標 pane 不是該 window 的 active pane 時也一樣。功能固定開啟，不需設定。

- **多個 TUI client**：同一目錄有多個 TUI 時，跳到 pane 編號（`%N` 的 N，以數字比較，`%3` 先於 `%13`）最小者；global 模式的 GLOBAL 群組取全部 TUI 中最小者。
  編號最小代表 pane 最早建立，不代表 OpenCode 最早啟動。
- **只到 pane**：插件唯讀，無法切換到 TUI 內的特定 tab。
- **其他位置**：點標題、分隔線、讀取錯誤提示、沒有 OpenCode TUI 時的提示或空白處，以及其他非插件的 range，行為與 tmux 預設（`switch-client -t =`）相同。
- **跳轉目標會隨側欄更新**：TUI 開啟、關閉或移動 pane 後，下一次側欄更新就指向新的 pane。

**這會覆寫你的 `MouseDown1SideStatus` 綁定。** 常駐程式啟動時（含交接給新實例）把 root 鍵表的 `MouseDown1SideStatus` 設成插件的綁定；
明確停止（`stop`）時**只會寫回 tmux 預設** `switch-client -t =`，不保存、不還原你原本自訂的綁定。
tmux server 結束時綁定隨之消失；常駐程式被強制終止（`kill -9`）時綁定會殘留，但非插件的 range 仍走 `switch-client -t =`，下次啟動會重寫。
綁定完全在 tmux 內執行，不開 shell、不呼叫插件執行檔。設計理由見 [ADR-0002](adr/0002-click-to-jump.md)。

### 鍵盤跳轉

按觸發鍵（預設 `prefix` 再按 `A`）進入**跳轉模式**：只有這個 client 的側欄，每個目錄群組列開頭的 `▎` 換成紫底的**跳轉標籤**（` a `、` b `…），
再按該字母就切到該群組的跳轉目標（與點擊同一個 pane，可跨 session）；跳轉後標籤消失。按 `Esc`、沒有綁定的鍵或沒有群組的字母會離開跳轉模式，位置不變，那個鍵不會送進 pane。

```tmux
# 選用：改觸發鍵（只寫鍵名，一律綁在 prefix 表）。預設 A 不覆蓋 tmux 預設鍵與常見插件的鍵
# set -g @opencode-status-jump-trigger J
# 選用：自己在 root 表加別名，少按一下 prefix（插件不提供、不管理 root 鍵；表名 opencode-status-jump 是對外承諾）
# bind -n M-a switch-client -T opencode-status-jump
```

- **標籤規則**：字母依側欄群組順序從 `a` 分配，只分給有跳轉目標的群組，最多 26 個（第 27 個起維持 `▎`，只能用滑鼠點）；tab 列不加標籤。
  字母與側欄高度無關：群組被截到畫面外時按它的字母仍會跳轉。標籤用 `emphasis` 底、`title` 字，不新增色票。
- **多 client**：只有按下觸發鍵的那個 client 顯示標籤，其他 client 的側欄不變。側欄隱藏（`side-status off`）時按觸發鍵仍會進入跳轉模式。
- **改觸發鍵**：改 `@opencode-status-jump-trigger` 後**重載 tmux.conf** 才生效（TPM 重新執行入口腳本，觸發鍵設定是常駐程式指紋的一部分，因此交接給新實例：
  先 unbind 舊鍵、再綁新鍵）。只在命令列 `set -g` 而不重載不會改綁。
- **無效的觸發鍵**（空字串、tmux 不認得的鍵名）：不綁任何 prefix 鍵，也**不會退回預設 `A`**；`diagnose` 會回報並計入問題數。
- **會覆寫該鍵的 prefix 綁定。** 預設鍵 `A` 在 tmux 預設與常見插件中都沒用到；若你自己的 `tmux.conf` 或其他插件綁了同一個鍵，插件載入後以插件為準。如果該鍵後來被改綁，`diagnose` 會回報綁定已被覆寫。
- **停止與重載的影響**：明確停止（`stop`）時 unbind 觸發鍵、清空 `opencode-status-jump` 表，並清掉所有 `@tos_*`；**不寫回、不還原任何原有綁定**。
  被覆蓋的若是你 `tmux.conf` 的綁定，重載設定即恢復；若是 tmux 內建預設鍵，需要重啟 tmux server。你自己加的 root 別名插件不會動，但停止後它指向的表是空的，按下去等同取消。
- 綁定完全在 tmux 內執行，不開 shell、不呼叫插件執行檔。設計理由見 [ADR-0004](adr/0004-internal-option-naming.md)、[ADR-0005](adr/0005-keyboard-jump.md)。

## 設定模式

```tmux
set -g @opencode-status-mode cwd      # 預設；依 TUI 工作目錄分組，對應 OpenCode 的 cwd scope
set -g @opencode-status-mode global   # 單一 GLOBAL 群組，對應 OpenCode 的 global scope
```

重載設定即套用，不需重啟 tmux。**模式必須由你設定成與 OpenCode 實際的 tab scope 一致**：
插件不偵測、不警告不一致（例如 OpenCode 用 cwd scope 而插件設 global，可能看到空的或過期的清單），也不支援各 TUI 混用不同 scope。
其他值（含空字串、大小寫不同、前後空白）視為無效。未設定時預設為 `cwd`，但讀取失敗不會套用預設值。設定無效或讀取失敗時，插件會顯示提示；如果已有有效模式就沿用，否則不開始追蹤。

## 配色

側欄有 13 個色票，各對應一個 tmux 全域使用者選項 `@opencode-status-color-<名稱>`。未設定的色票使用預設值；只能改顏色，版面、符號與樣式（全部不粗體）固定。

| 名稱 | 預設 | 對應位置 |
| --- | --- | --- |
| `emphasis` | `#A78BFA` | OPENCODE 底塊、專案名稱前的 `▎`、跳轉標籤底色 |
| `title` | `#18181B` | 底塊上的 OPENCODE 文字、跳轉標籤上的字母 |
| `split` | `#404040` | 標題下分隔線 |
| `project` | `#F2EEE3` | 專案名稱 |
| `active-tab` | `#cac5b5` | 執行中、等你處理、結束未讀、失敗未讀的 tab 標題 |
| `inactive-tab` | `#71717A` | 閒置與資料異常的 tab 標題、群組沒有 tab 時的提示、右側摘要放不下時補的 `…` |
| `state-running` | `#4ADE80` | `●` 執行中（含右側摘要中的符號與數字） |
| `state-ask` | `#FACC15` | `?` 等你處理 |
| `state-finished` | `#60A5FA` | `◆` 結束未讀 |
| `state-failed` | `#F87171` | `✗` 失敗未讀 |
| `state-problem` | `#A1A1AA` | `!` 資料異常 |
| `state-idle` | `#71717A` | `·` 閒置 |
| `notice` | `#FACC15` | `▲` 讀取錯誤提示的符號與文字 |

```tmux
set -g @opencode-status-color-emphasis '#7C3AED'     # #RRGGBB
set -g @opencode-status-color-project colour253      # colourN／colorN（0–255）
set -g @opencode-status-color-state-running green    # tmux 具名色（含 brightred 等 bright 變體）
set -g @opencode-status-color-split default          # 終端預設色
```

合法格式只有四種：`#RRGGBB`、`colourN`／`colorN`（N 為 0–255）、tmux 具名色（`black`、`red`、`green`、`yellow`、`blue`、`magenta`、`cyan`、`white` 與 `bright` 加上述名稱）、`default`。
具名色**只**接受上述 8 個基本色與其 `bright` 變體（如 `brightred`）及 `default`；其他 tmux 名稱（例如 `orange`）一律無效，請改用 `#RRGGBB` 或 `colourN`。
其餘值（空字串、前後有空白，或含 `]`、`,`、`#{` 等會破壞 tmux 格式的字元）都視為無效：只有該項退回預設，其他色票照常套用，側欄**不會**顯示錯誤；
`diagnose` 會列出無效的選項名稱並說明已退回預設。

設定、取消（`set -gu`）或重載 tmux.conf 後側欄立即更新，不需重啟常駐程式。
插件只讀取這些選項，不會寫入或刪除；停止常駐程式後你的設定仍保留。

## 故障排除

### 診斷（唯讀）

```sh
# 在 tmux 內，或帶 -L <name> / -S <path> 指定 server（不會使用預設 server）
~/.tmux/plugins/tmux-opencode-status/bin/<版本>-darwin-<arch>/tmux-opencode-status diagnose
```

輸出 tmux 版本與能力、插件模式、無效的色票選項、常駐實例狀態、發現的 TUI／tab 數、服務連線狀態與最近錯誤。
插件的畫面與指令訊息使用英文；本指南以繁中說明。**診斷不含憑證、prompt、對話或 OpenCode 的 session／tab 標題**，也不連 OpenCode、不寫任何選項。

### Session 層級覆寫

若你曾在某個 tmux session 以 `set -t <session> side-status…` 覆寫，該 session 不會跟隨全域設定，插件也**不會清除**。
`diagnose` 會列出受影響的 session（只顯示 session id）。要讓它跟隨全域：`tmux set -u -t <session> side-status`（`side-status-width`、`side-status-format` 同理）。

### 常見問題

| 現象 | 處理 |
| --- | --- |
| 載入插件時下載失敗 | 確認網路與 Release 是否存在該版本資產（`VERSION` 對應 tag `v<版本>`）。不會自動編譯；開發者可使用[自行編譯](#自行編譯)的執行檔 |
| 校驗碼驗證失敗 | 資產的校驗碼不符，或 `SHA256SUMS` 沒有該資產的有效校驗碼，因此拒絕安裝。重試；持續發生請勿使用該版本並回報 |
| 平台或架構不支援 | 只提供 macOS arm64 與 x86_64 |
| tmux 缺少必要能力 | 升級到 tmux 3.9（需 `side-status` 系列選項）；`tmux -V` 查看版本 |
| 側欄沒出現 | 先跑 `diagnose`：看常駐實例、模式、服務連線；確認 `side-status` 不是 `off`；檢查 session override |
| 一直是灰色 `!` | 資料異常（服務不可連線／清單不可讀等）；用 `diagnose` 查看近期錯誤原因。插件不會啟動 OpenCode 服務 |
| 配色沒生效 | 跑 `diagnose`：無效的色票值會列出並退回預設（側欄不顯示錯誤）；合法格式見「配色」 |
| 模式不對／清單是空的 | 確認 `@opencode-status-mode` 與 OpenCode 的 scope 一致 |
| 點側欄沒有跳轉 | 只有目錄群組的列可點；用 `tmux list-keys -T root \| grep MouseDown1SideStatus` 確認綁定是插件的（內容含 `mouse_status_range`），並確認 `mouse` 選項為 `on` |
| 按觸發鍵沒反應 | 跑 `diagnose` 查看鍵盤跳轉的綁定狀態，再用 `tmux list-keys -T prefix <鍵>` 確認綁定是 `switch-client -T opencode-status-jump`；改了 `@opencode-status-jump-trigger` 要重載 tmux.conf |
| 想完全移除 | `tmux-opencode-status stop`（清理本插件資源），再從 `tmux.conf` 移除 `@plugin` 並 `prefix + alt + u` |

常駐程式的錯誤記錄在 `$XDG_RUNTIME_DIR/tmux-opencode-status/`（沒有則 `$TMPDIR/tmux-opencode-status-<uid>/`）下的 `<id>.log`。

## 開發與發布

### 自行編譯

```sh
scripts/build-dev.sh            # → bin/dev/tmux-opencode-status（需要 Go）
```

```tmux
set -g @opencode-status-binary "/絕對路徑/bin/dev/tmux-opencode-status"
```

設定 `@opencode-status-binary` 後入口腳本使用該檔，**完全不下載**；路徑不存在或不可執行時明確報錯，不退回下載。
沒有設定時絕不自動編譯。

### 發布（維護者）

```sh
scripts/build-release.sh [版本]   # 預設讀 VERSION；輸出 dist/*.tar.gz 與 dist/SHA256SUMS
```

建置使用 `CGO_ENABLED=0`、`-trimpath`、固定的封存中繼資料，同一原始碼與 Go 版本得到逐位元組相同的產物。
[發布工作流程](../.github/workflows/release.yml)在推送 `v<VERSION>` tag 時建置並建立 Release；**本機建置不會打 tag 或發布，需由維護者啟動發布流程**。

### 測試與發布限制

在具備必要 tmux 功能的 macOS 上執行 `go test -count=1 ./...`。端對端測試使用隔離的 tmux server 和暫存目錄。TPM 測試透過本機 HTTP 提供自行建置的發布產物，並確認安裝程式不會呼叫 Go。

**Intel（`x86_64`）產物尚未在 Intel 實機上驗證。** Mach-O 架構檢查與 Rosetta 啟動不等於原生 Intel 驗證。可重現建置只在同一台主機、同一個 Go 版本驗證，未做跨主機驗證。發布產物未簽章、未經 Apple 公證。校驗碼可檢查損毀或與 `SHA256SUMS` 不符，但若產物和校驗檔都被替換，無法驗證發布的真偽。

## 文件

- [OpenCode 整合契約](contracts/opencode-2.3.0.md)
- [架構決策紀錄](adr/)
- [專案詞彙表](../GLOSSARY.md)
