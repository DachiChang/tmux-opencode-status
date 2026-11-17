# #12 TPM 預編譯安裝流程

## 產物

| 檔案 | 說明 |
| --- | --- |
| `scripts/build-release.sh [版本]` | 建置 darwin/arm64 與 darwin/amd64 單一執行檔，封裝成 `tmux-opencode-status_<版本>_darwin_<arch>.tar.gz`（內容只有 `tmux-opencode-status`），並產生 `SHA256SUMS`（`shasum -a 256` 格式） |
| `tools/pack` | 可重現封存：固定檔案權限、擁有者、mtime，gzip 不含檔名與時間 |
| `tmux-opencode-status.tmux` | TPM 入口（TPM 以可執行檔執行插件根目錄的 `*.tmux`；每次載入／重載都執行） |
| `scripts/build-dev.sh` | 開發者自行編譯，搭配 `@opencode-status-binary` |
| `.github/workflows/release.yml` | 推 `v*` tag 時測試、建置並建立 Release（**尚未觸發；沒有建立 tag 或 Release**） |
| `VERSION` | 入口腳本據此決定下載 `releases/download/v<VERSION>/` 的資產 |

可重現性：`CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false`、`-ldflags="-s -w -buildid="`；兩次建置得到相同 `SHA256SUMS`
（`TestReleaseBuildIsReproducible`；同一台機器、同一個 Go 版本。未跨主機驗證）。

## 入口腳本行為

1. 不在 tmux 內（沒有 `$TMUX`）就拒絕；絕不使用預設 server。TPM 的 `run-shell` 沿用 `$TMUX`。
2. 檢查 `side-status`、`side-status-width`、`side-status-format`；缺少（`invalid option`）明確失敗，**先於任何下載**；其他錯誤如實說「無法確認」，不誤報成缺少。
3. 取得執行檔：
   - 設了 `@opencode-status-binary`：只用它（絕對路徑、存在、可執行，否則報錯，**不退回下載**）。
   - 否則依 `VERSION` 與 `uname -s/-m`（`arm64`→arm64、`x86_64`→amd64；其餘平台／架構明確失敗，**不發出任何下載**）。
4. 已安裝且執行檔 SHA-256 與安裝時記錄相符就直接使用；否則安裝：以 `mkdir` 鎖序列化並行載入 → 下載 `SHA256SUMS` 與資產到暫存目錄 →
   核對校驗碼（64 位十六進位）→ 檢查封存只含 `tmux-opencode-status`、為一般檔案、架構符合（`file`）→ 才移入 `bin/<版本>-darwin-<arch>/`。
   任一步失敗：暫存清除、不放置任何檔案、不啟動。
5. 執行 `<binary> start -show`（冪等、單實例、隱藏狀態保護見 `lifecycle.md`）；非 0 時把輸出轉成錯誤。
6. 所有失敗：stderr + `tmux display-message`（`#` 已跳脫）；結束碼 1。**入口腳本從不呼叫 `go`**。

下載基底網址可由 `TMUX_OPENCODE_STATUS_RELEASE_URL` 注入（測試與鏡像用；注入時不強制 https，預設則限制 https）。

## 測試（程序外部接縫）

`test/tpm/tpm_test.go`：每個測試都用獨立 tmux socket（`-f /dev/null`）、臨時 HOME／XDG_RUNTIME_DIR／TPM 插件目錄，
發布資產由本機 `httptest` 伺服器提供（資產來自真實的 `build-release.sh`）；PATH 只含系統目錄與 tmux，
並在最前面放一個「go 陷阱」，被呼叫就記錄並失敗（模擬缺少 Go，同時證明沒有靜默編譯）。不碰使用者的 `~/.tmux.conf`、`~/.tmux/plugins`、預設 server 或 OpenCode 設定；結束時 kill 隔離 server 與常駐程式。

| 情境 | 測試 |
| --- | --- |
| 兩種架構產物為 Mach-O 且校驗檔可驗證；建置可重現；無效版本拒絕 | `TestReleaseBuild…` |
| 缺少 Go 的乾淨環境：只下載本機架構、驗證、放置、啟動、左側 36 格、不新增綁定 | `TestInstallsTheHostArchitecture…WithoutGo` |
| x86_64 選擇 amd64 資產且不 fallback（Rosetta 可用才執行，否則必須明確失敗） | `TestIntelArchitectureSelects…` |
| 不支援的架構明確失敗且不下載 | `TestUnsupportedArchitectureFailsLoudly…` |
| 資產 404／伺服器不可連線：明確失敗、不換架構、不編譯、不留暫存 | `TestDownloadFailureIsLoud…` |
| 資產被竄改／校驗檔缺項／格式無效：拒絕安裝與啟動 | `TestVerificationFailureRefuses…` |
| 校驗正確但封存內是另一架構：拒絕 | `TestEntryPointRejectsAnArchive…` |
| 4 個並行載入只下載一次且單實例；重複載入不重啟、不重新打開已隱藏側欄 | `TestRepeatedAndConcurrentLoads…` |
| 已安裝檔被改動：重新下載驗證後取代 | `TestCorruptedInstalledBinary…` |
| 缺少 tmux 能力：先於下載明確失敗 | `TestMissingTmuxCapabilityFailsLoudly…` |
| 不在 tmux 內拒絕 | `TestRunningOutsideTmux…` |
| 開發者 `@opencode-status-binary`：不下載；指向不存在則報錯不退回下載 | `TestDeveloperBuiltBinary…`、`TestDeveloperBinaryOptionPointingNowhere…` |
| 由 `run-shell` 載入失敗時原因出現在 tmux 訊息 | `TestFailuresAreAlsoShown…` |
| server 結束後常駐程式退出 | `TestLeaksNoProcessesAfterServerExit` |

測試有效性：對入口腳本做突變（跳過 SHA 驗證、下載失敗改 `go build`、未知架構退回 arm64）各被至少一個測試抓到。

## 實測記錄

- 本機 darwin/arm64（Apple Silicon）：上述測試全過（真實 tmux next-3.9、真實 start／常駐程式）。
- 產物架構：`file`／`lipo -archs` 確認 arm64 與 x86_64 兩份 Mach-O。
- Rosetta 可用：`arch -x86_64` 可啟動 amd64 產物（僅確認行程能執行並印出預期的拒絕訊息）。**這不等同真實 Intel 機器驗證。**

## 尚未驗證（如實標示）

- **Intel（x86_64）真機**：未驗證。僅有 `file`／`lipo` 的架構檢查與本機 Rosetta 啟動；未驗證 Intel 上的 tmux 互動、`lsof`／`KERN_PROCARGS2` 行為與效能。
- **真實 TPM**：入口腳本以 TPM 的行為（執行 `*.tmux` 為可執行檔、stdout/stderr 被丟棄）設計並以 `run-shell` 模擬；未在使用者的 TPM 安裝上跑過（刻意不碰 `~/.tmux/plugins`）。
- **真實 GitHub Release 下載**：未發布 Release，下載以本機 HTTP 伺服器驗證；預設 URL `https://github.com/DachiChang/tmux-opencode-status/releases/download/v<版本>/` 與 workflow 尚未實際跑過。
- 可重現性只在同一台機器、同一個 Go 版本驗證；未簽章／未 notarize（`curl` 下載不帶 quarantine 屬性，但 Gatekeeper 行為未在乾淨 Intel／其他 macOS 版本確認）。
- 校驗檔與資產來自同一個來源：SHA-256 防損毀與資產被單獨替換，**不防整個 Release 被入侵**（未提供簽章）。
