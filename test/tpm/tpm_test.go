// Package tpm_test 以程序外部行為驗證 TPM 入口腳本與發布封裝流程（issue #12）。
//
// 全部在隔離環境進行：獨立 tmux socket（-f /dev/null）、臨時 HOME／XDG_RUNTIME_DIR、臨時「TPM 插件目錄」，
// 發布資產由本機 HTTP 伺服器提供（經 TMUX_OPENCODE_STATUS_RELEASE_URL 注入）。不碰使用者的 ~/.tmux.conf、
// ~/.tmux/plugins、預設 tmux server 或 OpenCode 設定。PATH 前端放一個「go 陷阱」：被呼叫就記錄並失敗，
// 用來證明任何失敗路徑都不會靜默現場編譯。
package tpm_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const testVersion = "0.0.0-test"

var (
	repoRoot string
	dist     string // build-release.sh 產出的真實發布資產（兩種架構）
)

func TestMain(m *testing.M) {
	code := func() int {
		wd, _ := os.Getwd()
		repoRoot = filepath.Clean(filepath.Join(wd, "..", ".."))
		tmp, err := os.MkdirTemp("", "tos-tpm-dist-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(tmp)
		dist = filepath.Join(tmp, "dist")
		cmd := exec.Command(filepath.Join(repoRoot, "scripts", "build-release.sh"), testVersion)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "DIST_DIR="+dist)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build-release 失敗：%v\n%s", err, out)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

// releaseServer 提供一份發布目錄並記錄所有請求。
type releaseServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string
	dir  string
}

func newReleaseServer(t *testing.T, dir string) *releaseServer {
	t.Helper()
	rs := &releaseServer{dir: dir}
	fs := http.FileServer(http.Dir(dir))
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.reqs = append(rs.reqs, r.URL.Path)
		rs.mu.Unlock()
		fs.ServeHTTP(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *releaseServer) count(sub string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for _, r := range rs.reqs {
		if strings.Contains(r, sub) {
			n++
		}
	}
	return n
}

func (rs *releaseServer) total() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.reqs)
}

// copyDir 複製發布資產到可被改動的目錄。
func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	ents, err := os.ReadDir(src)
	must(t, err)
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		must(t, err)
		must(t, os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644))
	}
	return dst
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func hostArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

// env 是一個乾淨環境：隔離 tmux server、臨時 HOME、臨時 TPM 插件目錄、受控 PATH。
type env struct {
	t         *testing.T
	sock      string
	tmuxEnv   string // $TMUX 值（socket,pid,0）
	plugin    string
	home      string
	runtime   string
	shimDir   string
	goTrapLog string
	extraEnv  []string
	path      string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("沒有 tmux")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e := &env{t: t, plugin: filepath.Join(root, "plugins", "tmux-opencode-status"), home: filepath.Join(root, "home"),
		runtime: filepath.Join(root, "run"), shimDir: filepath.Join(root, "shims"), goTrapLog: filepath.Join(root, "go-called")}
	for _, d := range []string{e.plugin, e.home, e.runtime, e.shimDir} {
		must(t, os.MkdirAll(d, 0o700))
	}
	for _, f := range []string{"tmux-opencode-status.tmux", "VERSION"} {
		b, err := os.ReadFile(filepath.Join(repoRoot, f))
		must(t, err)
		mode := os.FileMode(0o644)
		if strings.HasSuffix(f, ".tmux") {
			mode = 0o755
		}
		must(t, os.WriteFile(filepath.Join(e.plugin, f), b, mode))
	}
	must(t, os.WriteFile(filepath.Join(e.plugin, "VERSION"), []byte(testVersion+"\n"), 0o644))
	e.writeShim("go", fmt.Sprintf("echo \"$@\" >> %q\necho 'go 不應被呼叫' >&2\nexit 1\n", e.goTrapLog))
	e.path = e.shimDir + ":/usr/bin:/bin:/usr/sbin:/sbin"
	if p, err := exec.LookPath("tmux"); err == nil {
		e.path += ":" + filepath.Dir(p)
	}
	// 確認 PATH 中除了陷阱外沒有真的 go。
	if _, err := os.Stat("/usr/bin/go"); err == nil {
		t.Skip("系統 PATH 內有 go，無法模擬缺少 Go")
	}

	e.sock = fmt.Sprintf("tos-tpm-%d-%d", os.Getpid(), time.Now().UnixNano()%1e6)
	e.rawTmux("new-session", "-d", "-s", "main", "-x", "100", "-y", "30")
	t.Cleanup(func() {
		_ = exec.Command("pkill", "-f", filepath.Join(e.plugin, "bin")).Run()
		_ = exec.Command("tmux", "-L", e.sock, "-f", "/dev/null", "kill-server").Run()
		_ = exec.Command("pkill", "-f", e.plugin).Run()
	})
	info := strings.TrimSpace(e.rawTmux("display-message", "-p", "#{socket_path},#{pid},0"))
	e.tmuxEnv = info
	return e
}

func (e *env) writeShim(name, body string) {
	e.t.Helper()
	must(e.t, os.WriteFile(filepath.Join(e.shimDir, name), []byte("#!/bin/sh\n"+body), 0o755))
}

func (e *env) rawTmux(args ...string) string {
	e.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", e.sock, "-f", "/dev/null"}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("tmux %v：%v\n%s", args, err, out)
	}
	return string(out)
}

func (e *env) environ(withTmux bool) []string {
	env := []string{"PATH=" + e.path, "HOME=" + e.home, "XDG_RUNTIME_DIR=" + e.runtime, "TMPDIR=" + filepath.Join(e.runtime, "tmp"), "LANG=C"}
	must(e.t, os.MkdirAll(filepath.Join(e.runtime, "tmp"), 0o700))
	if withTmux {
		env = append(env, "TMUX="+e.tmuxEnv)
	}
	return append(env, e.extraEnv...)
}

// load 執行入口腳本一次（等同 TPM 在載入／重載時執行）。
func (e *env) load() (string, error) {
	return e.loadWith(true)
}

func (e *env) loadWith(withTmux bool) (string, error) {
	cmd := exec.Command(filepath.Join(e.plugin, "tmux-opencode-status.tmux"))
	cmd.Dir = e.home
	cmd.Env = e.environ(withTmux)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	must(e.t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		e.t.Fatalf("入口腳本逾時：%s", out.String())
		return "", nil
	}
}

func (e *env) mustLoad() string {
	e.t.Helper()
	out, err := e.load()
	if err != nil {
		e.t.Fatalf("入口腳本應成功：%v\n%s", err, out)
	}
	return out
}

func (e *env) installDir(arch string) string {
	return filepath.Join(e.plugin, "bin", testVersion+"-darwin-"+arch)
}

func (e *env) daemons() []int {
	out, _ := exec.Command("ps", "-axo", "pid=,command=").Output()
	var pids []int
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if !strings.Contains(l, filepath.Join(e.plugin, "bin")) || !strings.Contains(l, " run ") {
			continue
		}
		var pid int
		fmt.Sscanf(l, "%d", &pid)
		pids = append(pids, pid)
	}
	return pids
}

func (e *env) waitDaemons(want int) []int {
	e.t.Helper()
	var got []int
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if got = e.daemons(); len(got) == want {
			return got
		}
	}
	e.t.Fatalf("常駐實例數應為 %d，實際 %v", want, got)
	return nil
}

func (e *env) assertGoNeverCalled() {
	e.t.Helper()
	if b, err := os.ReadFile(e.goTrapLog); err == nil {
		e.t.Fatalf("入口腳本不得呼叫 go（靜默現場編譯）：%s", b)
	}
}

func (e *env) assertNothingInstalledOrStarted() {
	e.t.Helper()
	time.Sleep(200 * time.Millisecond)
	if got := e.daemons(); len(got) != 0 {
		e.t.Fatalf("不得啟動常駐程式：%v", got)
	}
	if ents, _ := filepath.Glob(filepath.Join(e.plugin, "bin", testVersion+"-*", "*")); len(ents) != 0 {
		e.t.Fatalf("不得放置任何執行檔：%v", ents)
	}
	if stage, _ := filepath.Glob(filepath.Join(e.plugin, "bin", ".stage.*")); len(stage) != 0 {
		e.t.Fatalf("暫存目錄未清理：%v", stage)
	}
	e.assertGoNeverCalled()
}

func (e *env) sideStatus() string {
	return strings.TrimSpace(e.rawTmux("show-options", "-gv", "side-status"))
}

// keys 是 list-keys 去掉插件接管的綁定：MouseDown1SideStatus（ADR-0002）、鍵盤跳轉的觸發鍵與
// opencode-status-jump 按鍵表（ADR-0005）。
func (e *env) keys() string {
	var out []string
	for _, l := range strings.Split(e.rawTmux("list-keys"), "\n") {
		if f := strings.Fields(l); len(f) >= 4 && f[3] == "MouseDown1SideStatus" {
			continue
		}
		if strings.Contains(l, "-T opencode-status-jump ") || strings.HasSuffix(strings.TrimSpace(l), "switch-client -T opencode-status-jump") {
			continue
		}
		out = append(out, strings.Join(strings.Fields(l), " ")) // 欄寬會依最長表名重排
	}
	return strings.Join(out, "\n")
}

// --- 發布封裝 ---

func TestReleaseBuildProducesBothMacArchitecturesWithVerifiableChecksums(t *testing.T) {
	for _, c := range []struct{ arch, want string }{{"arm64", "arm64"}, {"amd64", "x86_64"}} {
		asset := filepath.Join(dist, fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, c.arch))
		tmp := t.TempDir()
		if out, err := exec.Command("tar", "-xzf", asset, "-C", tmp).CombinedOutput(); err != nil {
			t.Fatalf("解開 %s：%v\n%s", asset, err, out)
		}
		out, err := exec.Command("file", filepath.Join(tmp, "tmux-opencode-status")).Output()
		must(t, err)
		if !strings.Contains(string(out), "Mach-O") || !strings.Contains(string(out), c.want) {
			t.Errorf("%s 應是 darwin/%s 的 Mach-O：%s", c.arch, c.want, out)
		}
	}
	cmd := exec.Command("shasum", "-a", "256", "-c", "SHA256SUMS")
	cmd.Dir = dist
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("SHA256SUMS 驗證失敗：%v\n%s", err, out)
	}
}

func TestReleaseBuildIsReproducible(t *testing.T) {
	second := filepath.Join(t.TempDir(), "dist2")
	cmd := exec.Command(filepath.Join(repoRoot, "scripts", "build-release.sh"), testVersion)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "DIST_DIR="+second)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	a, _ := os.ReadFile(filepath.Join(dist, "SHA256SUMS"))
	b, _ := os.ReadFile(filepath.Join(second, "SHA256SUMS"))
	if len(a) == 0 || !bytes.Equal(a, b) {
		t.Fatalf("相同輸入應得到相同產物：\n%s\n%s", a, b)
	}
}

func TestReleaseBuildRejectsABadVersion(t *testing.T) {
	cmd := exec.Command(filepath.Join(repoRoot, "scripts", "build-release.sh"), "v1;rm")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "DIST_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("無效版本應失敗：%s", out)
	}
}

// --- 安裝與啟動 ---

func TestInstallsTheHostArchitectureVerifiesItAndStartsTheSidebarWithoutGo(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	keysBefore := e.keys()

	e.mustLoad()

	bin := filepath.Join(e.installDir(hostArch()), "tmux-opencode-status")
	if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("應已放置可執行檔 %s：%v", bin, err)
	}
	if rs.count("_darwin_"+hostArch()+".tar.gz") != 1 || rs.count("SHA256SUMS") != 1 {
		t.Fatalf("應下載一次對應架構資產與校驗檔：%v", rs.reqs)
	}
	other := "amd64"
	if hostArch() == "amd64" {
		other = "arm64"
	}
	if rs.count("_darwin_"+other) != 0 {
		t.Fatalf("不得下載另一種架構：%v", rs.reqs)
	}
	e.waitDaemons(1)
	if got := e.sideStatus(); got != "left" {
		t.Fatalf("首次載入應顯示側欄（side-status=left），實際 %q", got)
	}
	// 寬度在常駐程式第一次發布時寫入，start 返回時可能還沒到。
	var w string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if w = strings.TrimSpace(e.rawTmux("show-options", "-gv", "side-status-width")); w == "36" {
			break
		}
	}
	if w != "36" {
		t.Fatalf("預設寬度應為 36 格，實際 %q", w)
	}
	if e.keys() != keysBefore {
		t.Fatal("載入除了 MouseDown1SideStatus 外不得新增或改動任何按鍵綁定")
	}
	e.assertGoNeverCalled()
}

func TestIntelArchitectureSelectsTheAmd64AssetAndNeverFallsBack(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	e.writeShim("uname", "if [ \"$1\" = -m ]; then echo x86_64; else exec /usr/bin/uname \"$@\"; fi\n")

	out, err := e.load()
	if rs.count("_darwin_amd64.tar.gz") != 1 || rs.count("_darwin_arm64") != 0 {
		t.Fatalf("x86_64 應只下載 amd64 資產：%v\n%s", rs.reqs, out)
	}
	bin := filepath.Join(e.installDir("amd64"), "tmux-opencode-status")
	fileOut, ferr := exec.Command("file", bin).Output()
	if ferr != nil || !strings.Contains(string(fileOut), "x86_64") {
		t.Fatalf("放置的執行檔應是 x86_64：%s %v", fileOut, ferr)
	}
	if exec.Command("arch", "-x86_64", "/usr/bin/true").Run() != nil {
		// 本機沒有 Rosetta：無法執行 Intel 檔，入口腳本必須明確失敗而不是換成 arm64。
		if err == nil {
			t.Fatalf("沒有 Rosetta 時啟動 Intel 執行檔應失敗：%s", out)
		}
		return
	}
	if err != nil {
		t.Fatalf("Rosetta 可用時應能啟動：%v\n%s", err, out)
	}
	e.waitDaemons(1)
}

func TestUnsupportedArchitectureFailsLoudlyWithoutDownloading(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	e.writeShim("uname", "if [ \"$1\" = -m ]; then echo i386; else exec /usr/bin/uname \"$@\"; fi\n")
	out, err := e.load()
	if err == nil || !strings.Contains(out, "不支援的架構") || !strings.Contains(out, "i386") {
		t.Fatalf("應明確報錯：%v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("不支援的架構不得發出任何下載請求：%v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestDownloadFailureIsLoudAndDoesNotCompileOrChangeArchitecture(t *testing.T) {
	for name, setup := range map[string]func(*env) *releaseServer{
		"資產 404": func(e *env) *releaseServer {
			d := copyDir(t, dist)
			must(t, os.Remove(filepath.Join(d, fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, hostArch()))))
			return newReleaseServer(t, d)
		},
		"伺服器不可連線": func(e *env) *releaseServer {
			rs := newReleaseServer(t, dist)
			rs.Close()
			return rs
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			rs := setup(e)
			e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
			out, err := e.load()
			if err == nil || !strings.Contains(out, "下載失敗") {
				t.Fatalf("應明確報下載失敗：%v\n%s", err, out)
			}
			if strings.Contains(out, "go build") && !strings.Contains(out, "不會改用本機編譯") {
				t.Fatalf("訊息不應暗示會自動編譯：%s", out)
			}
			other := "amd64"
			if hostArch() == "amd64" {
				other = "arm64"
			}
			if rs.count("_darwin_"+other) != 0 {
				t.Fatalf("不得換備援架構：%v", rs.reqs)
			}
			e.assertNothingInstalledOrStarted()
		})
	}
}

func TestVerificationFailureRefusesToInstallOrStart(t *testing.T) {
	asset := fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, hostArch())
	cases := map[string]func(dir string){
		"資產被竄改": func(d string) {
			f, err := os.OpenFile(filepath.Join(d, asset), os.O_APPEND|os.O_WRONLY, 0)
			must(t, err)
			_, _ = f.WriteString("tampered")
			f.Close()
		},
		"校驗檔沒有該資產": func(d string) {
			must(t, os.WriteFile(filepath.Join(d, "SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  other.tar.gz\n"), 0o644))
		},
		"校驗碼格式無效": func(d string) {
			must(t, os.WriteFile(filepath.Join(d, "SHA256SUMS"), []byte("xyz  "+asset+"\n"), 0o644))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			d := copyDir(t, dist)
			mutate(d)
			rs := newReleaseServer(t, d)
			e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
			out, err := e.load()
			if err == nil || !(strings.Contains(out, "完整性驗證失敗") || strings.Contains(out, "校驗碼")) {
				t.Fatalf("應明確報驗證失敗：%v\n%s", err, out)
			}
			e.assertNothingInstalledOrStarted()
		})
	}
}

func TestEntryPointRejectsAnArchiveWhoseBinaryIsTheWrongArchitecture(t *testing.T) {
	// 校驗碼正確但封存內是另一種架構的執行檔：不得放置（不換備援、不靜默使用）。
	e := newEnv(t)
	d := copyDir(t, dist)
	other := "amd64"
	if hostArch() == "amd64" {
		other = "arm64"
	}
	mine := fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, hostArch())
	theirs := fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, other)
	b, err := os.ReadFile(filepath.Join(d, theirs))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(d, mine), b, 0o644))
	sums := new(bytes.Buffer)
	for _, a := range []string{mine, theirs} {
		out, err := exec.Command("shasum", "-a", "256", filepath.Join(d, a)).Output()
		must(t, err)
		fmt.Fprintf(sums, "%s  %s\n", strings.Fields(string(out))[0], a)
	}
	must(t, os.WriteFile(filepath.Join(d, "SHA256SUMS"), sums.Bytes(), 0o644))
	rs := newReleaseServer(t, d)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	out, lerr := e.load()
	if lerr == nil || !strings.Contains(out, "不是 darwin/"+hostArch()) {
		t.Fatalf("應拒絕錯誤架構：%v\n%s", lerr, out)
	}
	e.assertNothingInstalledOrStarted()
}

func TestRepeatedAndConcurrentLoadsDownloadOnceAndKeepOneInstance(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}

	var wg sync.WaitGroup
	errs := make(chan string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := e.load(); err != nil {
				errs <- fmt.Sprintf("%v\n%s", err, out)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for m := range errs {
		t.Fatalf("並行載入應全部成功：%s", m)
	}
	pids := e.waitDaemons(1)
	if n := rs.count("_darwin_"); n != 1 {
		t.Fatalf("並行載入只應下載資產一次，實際 %d：%v", n, rs.reqs)
	}

	e.rawTmux("set-option", "-g", "side-status", "off") // 使用者隱藏側欄
	for i := 0; i < 3; i++ {
		e.mustLoad()
	}
	if got := e.waitDaemons(1); got[0] != pids[0] {
		t.Fatalf("重複載入不得重啟實例：%v → %v", pids, got)
	}
	if rs.count("_darwin_") != 1 {
		t.Fatalf("已安裝且校驗相符時不得再下載：%v", rs.reqs)
	}
	if got := e.sideStatus(); got != "off" {
		t.Fatalf("重載不得重新打開使用者隱藏的側欄：%q", got)
	}
}

func TestCorruptedInstalledBinaryIsReplacedByAVerifiedDownload(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	e.mustLoad()
	_ = exec.Command("tmux", "-L", e.sock, "-f", "/dev/null", "kill-server").Run()
	e.waitDaemons(0)

	bin := filepath.Join(e.installDir(hostArch()), "tmux-opencode-status")
	must(t, os.WriteFile(bin, []byte("#!/bin/sh\necho evil\n"), 0o755))
	e.rawTmux("new-session", "-d", "-s", "main", "-x", "100", "-y", "30")
	e.tmuxEnv = strings.TrimSpace(e.rawTmux("display-message", "-p", "#{socket_path},#{pid},0"))
	e.mustLoad()
	if rs.count("_darwin_") != 2 {
		t.Fatalf("校驗不符的已安裝檔應重新下載驗證：%v", rs.reqs)
	}
	if b, _ := os.ReadFile(bin); strings.Contains(string(b), "evil") {
		t.Fatal("被改動的執行檔應已被取代")
	}
	e.waitDaemons(1)
}

func TestMissingTmuxCapabilityFailsLoudlyBeforeDownloadingOrStarting(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	real, _ := exec.LookPath("tmux")
	e.writeShim("tmux", fmt.Sprintf("case \"$*\" in *show-options*side-status-width*) echo \"invalid option: side-status-width\" >&2; exit 1;; esac\nexec %q \"$@\"\n", real))
	e.path = e.shimDir + ":" + e.path
	out, err := e.load()
	if err == nil || !strings.Contains(out, "缺少必要能力") || !strings.Contains(out, "side-status-width") {
		t.Fatalf("應明確報缺少能力：%v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("缺少能力不得下載：%v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestRunningOutsideTmuxIsRefusedInsteadOfUsingTheDefaultServer(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	out, err := e.loadWith(false)
	if err == nil || !strings.Contains(out, "必須在 tmux 內執行") {
		t.Fatalf("應拒絕：%v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("不得下載：%v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestDeveloperBuiltBinaryIsUsedExplicitlyWithoutAnyDownload(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	rs.Close() // 任何下載都會失敗
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}

	devBin := filepath.Join(t.TempDir(), "tmux-opencode-status")
	build := exec.Command("go", "build", "-o", devBin, "./cmd/tmux-opencode-status")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("開發者編譯：%v\n%s", err, out)
	}
	e.rawTmux("set-option", "-g", "@opencode-status-binary", devBin)
	e.mustLoad()
	e.waitDaemons(0 + len(e.daemons())) // 實例的 argv 指向 devBin，不在插件 bin/ 內
	out, _ := exec.Command("ps", "-axo", "command=").Output()
	if !strings.Contains(string(out), devBin+" run") {
		t.Fatalf("應以指定的開發者執行檔啟動常駐程式")
	}
	_ = exec.Command("pkill", "-f", devBin).Run()
	if ents, _ := filepath.Glob(filepath.Join(e.plugin, "bin", "*")); len(ents) != 0 {
		t.Fatalf("開發者路徑不得下載或放置檔案：%v", ents)
	}
}

func TestDeveloperBinaryOptionPointingNowhereFailsInsteadOfDownloading(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	e.rawTmux("set-option", "-g", "@opencode-status-binary", "/nonexistent/tmux-opencode-status")
	out, err := e.load()
	if err == nil || !strings.Contains(out, "@opencode-status-binary") {
		t.Fatalf("應明確報錯：%v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("不得退回下載：%v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestFailuresAreAlsoShownThroughTmuxDisplayMessageWhenLoadedByRunShell(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	rs.Close()
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	// 與 TPM 相同：由 tmux server 的 run-shell 執行，沿用其 $TMUX。
	cmd := exec.Command("tmux", "-L", e.sock, "-f", "/dev/null", "run-shell", filepath.Join(e.plugin, "tmux-opencode-status.tmux"))
	cmd.Env = e.environ(false)
	_, _ = cmd.CombinedOutput()
	msgs := e.rawTmux("show-messages")
	if !strings.Contains(msgs, "下載失敗") {
		t.Fatalf("失敗原因應出現在 tmux 訊息：\n%s", msgs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestLeaksNoProcessesAfterServerExit(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	e.mustLoad()
	pid := e.waitDaemons(1)[0]
	_ = exec.Command("tmux", "-L", e.sock, "-f", "/dev/null", "kill-server").Run()
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	t.Fatalf("server 結束後常駐程式 %d 應退出", pid)
}
