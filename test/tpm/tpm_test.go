// Package tpm_test verifies the TPM entry script and the release packaging flow through external process behavior (issue #12).
//
// Everything runs in an isolated environment: a dedicated tmux socket (-f /dev/null), temporary HOME/XDG_RUNTIME_DIR, a temporary "TPM plugin directory",
// and release assets served by a local HTTP server (injected through TMUX_OPENCODE_STATUS_RELEASE_URL). It never touches the user's ~/.tmux.conf,
// ~/.tmux/plugins, the default tmux server, or OpenCode settings. A "go trap" at the front of PATH logs and fails if it is called,
// which proves that no failure path silently compiles on the spot.
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
	dist     string // real release assets produced by build-release.sh (both architectures)
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
			fmt.Fprintf(os.Stderr, "build-release failed: %v\n%s", err, out)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

// releaseServer serves a release directory and records every request.
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

// copyDir copies the release assets into a directory that can be modified.
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

// env is a clean environment: an isolated tmux server, a temporary HOME, a temporary TPM plugin directory, and a controlled PATH.
type env struct {
	t         *testing.T
	sock      string
	tmuxEnv   string // $TMUX value (socket,pid,0)
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
		t.Skip("tmux not available")
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
	// Make sure PATH contains no real go besides the trap.
	if _, err := os.Stat("/usr/bin/go"); err == nil {
		t.Skip("go is on the system PATH, so a missing Go toolchain cannot be simulated")
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
		e.t.Fatalf("tmux %v: %v\n%s", args, err, out)
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

// load runs the entry script once (equivalent to TPM running it on load/reload).
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
		e.t.Fatalf("entry script timed out: %s", out.String())
		return "", nil
	}
}

func (e *env) mustLoad() string {
	e.t.Helper()
	out, err := e.load()
	if err != nil {
		e.t.Fatalf("entry script should succeed: %v\n%s", err, out)
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
	e.t.Fatalf("expected %d daemon instances, got %v", want, got)
	return nil
}

func (e *env) assertGoNeverCalled() {
	e.t.Helper()
	if b, err := os.ReadFile(e.goTrapLog); err == nil {
		e.t.Fatalf("entry script must not call go (silent on-the-spot compilation): %s", b)
	}
}

func (e *env) assertNothingInstalledOrStarted() {
	e.t.Helper()
	time.Sleep(200 * time.Millisecond)
	if got := e.daemons(); len(got) != 0 {
		e.t.Fatalf("must not start a daemon: %v", got)
	}
	if ents, _ := filepath.Glob(filepath.Join(e.plugin, "bin", testVersion+"-*", "*")); len(ents) != 0 {
		e.t.Fatalf("must not place any executable: %v", ents)
	}
	if stage, _ := filepath.Glob(filepath.Join(e.plugin, "bin", ".stage.*")); len(stage) != 0 {
		e.t.Fatalf("staging directory was not cleaned up: %v", stage)
	}
	e.assertGoNeverCalled()
}

func (e *env) sideStatus() string {
	return strings.TrimSpace(e.rawTmux("show-options", "-gv", "side-status"))
}

// waitSideStatus waits until side-status becomes want: start returning only means the daemon holds the lock, and run's first Show may arrive later.
// On timeout it includes this isolated test environment's own daemon log (under this test's XDG_RUNTIME_DIR).
func (e *env) waitSideStatus(want string) {
	e.t.Helper()
	var got string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if got = e.sideStatus(); got == want {
			return
		}
	}
	var logs strings.Builder
	files, _ := filepath.Glob(filepath.Join(e.runtime, "tmux-opencode-status", "*.log"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		fmt.Fprintf(&logs, "--- %s ---\n%s\n", filepath.Base(f), b)
	}
	e.t.Fatalf("side-status should be %q within 5 seconds, got %q\ndaemon log:\n%s", want, got, logs.String())
}

// keys is list-keys without the bindings the plugin takes over: MouseDown1SideStatus (ADR-0002), the keyboard-jump trigger key, and
// the opencode-status-jump key table (ADR-0005).
func (e *env) keys() string {
	var out []string
	for _, l := range strings.Split(e.rawTmux("list-keys"), "\n") {
		if f := strings.Fields(l); len(f) >= 4 && f[3] == "MouseDown1SideStatus" {
			continue
		}
		if strings.Contains(l, "-T opencode-status-jump ") || strings.HasSuffix(strings.TrimSpace(l), "switch-client -T opencode-status-jump") {
			continue
		}
		out = append(out, strings.Join(strings.Fields(l), " ")) // column widths are re-laid out to fit the longest table name
	}
	return strings.Join(out, "\n")
}

// --- Release packaging ---

func TestReleaseBuildProducesBothMacArchitecturesWithVerifiableChecksums(t *testing.T) {
	for _, c := range []struct{ arch, want string }{{"arm64", "arm64"}, {"amd64", "x86_64"}} {
		asset := filepath.Join(dist, fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, c.arch))
		tmp := t.TempDir()
		if out, err := exec.Command("tar", "-xzf", asset, "-C", tmp).CombinedOutput(); err != nil {
			t.Fatalf("extract %s: %v\n%s", asset, err, out)
		}
		out, err := exec.Command("file", filepath.Join(tmp, "tmux-opencode-status")).Output()
		must(t, err)
		if !strings.Contains(string(out), "Mach-O") || !strings.Contains(string(out), c.want) {
			t.Errorf("%s should be a darwin/%s Mach-O: %s", c.arch, c.want, out)
		}
	}
	cmd := exec.Command("shasum", "-a", "256", "-c", "SHA256SUMS")
	cmd.Dir = dist
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("SHA256SUMS verification failed: %v\n%s", err, out)
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
		t.Fatalf("identical input should produce identical output:\n%s\n%s", a, b)
	}
}

func TestReleaseBuildRejectsABadVersion(t *testing.T) {
	cmd := exec.Command(filepath.Join(repoRoot, "scripts", "build-release.sh"), "v1;rm")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "DIST_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("invalid version should fail: %s", out)
	}
}

// --- Installation and startup ---

func TestInstallsTheHostArchitectureVerifiesItAndStartsTheSidebarWithoutGo(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	keysBefore := e.keys()

	e.mustLoad()

	bin := filepath.Join(e.installDir(hostArch()), "tmux-opencode-status")
	if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("executable %s should be in place: %v", bin, err)
	}
	if rs.count("_darwin_"+hostArch()+".tar.gz") != 1 || rs.count("SHA256SUMS") != 1 {
		t.Fatalf("should download the matching-architecture asset and checksum file once: %v", rs.reqs)
	}
	other := "amd64"
	if hostArch() == "amd64" {
		other = "arm64"
	}
	if rs.count("_darwin_"+other) != 0 {
		t.Fatalf("must not download the other architecture: %v", rs.reqs)
	}
	e.waitDaemons(1)
	e.waitSideStatus("left") // the first load should show the sidebar
	// The width is written when the daemon first publishes, so it may not be there yet when start returns.
	var w string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if w = strings.TrimSpace(e.rawTmux("show-options", "-gv", "side-status-width")); w == "36" {
			break
		}
	}
	if w != "36" {
		t.Fatalf("default width should be 36 columns, got %q", w)
	}
	if e.keys() != keysBefore {
		t.Fatal("loading must not add or change any key binding other than MouseDown1SideStatus")
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
		t.Fatalf("x86_64 should download only the amd64 asset: %v\n%s", rs.reqs, out)
	}
	bin := filepath.Join(e.installDir("amd64"), "tmux-opencode-status")
	fileOut, ferr := exec.Command("file", bin).Output()
	if ferr != nil || !strings.Contains(string(fileOut), "x86_64") {
		t.Fatalf("the installed executable should be x86_64: %s %v", fileOut, ferr)
	}
	if exec.Command("arch", "-x86_64", "/usr/bin/true").Run() != nil {
		// No Rosetta locally: the Intel binary cannot run, so the entry script must fail explicitly instead of switching to arm64.
		if err == nil {
			t.Fatalf("starting the Intel executable without Rosetta should fail: %s", out)
		}
		return
	}
	if err != nil {
		t.Fatalf("should start when Rosetta is available: %v\n%s", err, out)
	}
	e.waitDaemons(1)
}

func TestUnsupportedArchitectureFailsLoudlyWithoutDownloading(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	e.writeShim("uname", "if [ \"$1\" = -m ]; then echo i386; else exec /usr/bin/uname \"$@\"; fi\n")
	out, err := e.load()
	if err == nil || !strings.Contains(out, "Unsupported architecture") || !strings.Contains(out, "i386") {
		t.Fatalf("should report an error explicitly: %v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("an unsupported architecture must not make any download request: %v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestDownloadFailureIsLoudAndDoesNotCompileOrChangeArchitecture(t *testing.T) {
	for name, setup := range map[string]func(*env) *releaseServer{
		"asset 404": func(e *env) *releaseServer {
			d := copyDir(t, dist)
			must(t, os.Remove(filepath.Join(d, fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, hostArch()))))
			return newReleaseServer(t, d)
		},
		"server unreachable": func(e *env) *releaseServer {
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
			if err == nil || !strings.Contains(out, "Download failed") {
				t.Fatalf("should report a download failure explicitly: %v\n%s", err, out)
			}
			if strings.Contains(out, "go build") && !strings.Contains(out, "no local build fallback") {
				t.Fatalf("the message must not suggest automatic compilation: %s", out)
			}
			other := "amd64"
			if hostArch() == "amd64" {
				other = "arm64"
			}
			if rs.count("_darwin_"+other) != 0 {
				t.Fatalf("must not switch to a fallback architecture: %v", rs.reqs)
			}
			e.assertNothingInstalledOrStarted()
		})
	}
}

func TestVerificationFailureRefusesToInstallOrStart(t *testing.T) {
	asset := fmt.Sprintf("tmux-opencode-status_%s_darwin_%s.tar.gz", testVersion, hostArch())
	cases := map[string]func(dir string){
		"asset tampered with": func(d string) {
			f, err := os.OpenFile(filepath.Join(d, asset), os.O_APPEND|os.O_WRONLY, 0)
			must(t, err)
			_, _ = f.WriteString("tampered")
			f.Close()
		},
		"checksum file lacks the asset": func(d string) {
			must(t, os.WriteFile(filepath.Join(d, "SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  other.tar.gz\n"), 0o644))
		},
		"checksum format invalid": func(d string) {
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
			if err == nil || !(strings.Contains(out, "Integrity verification failed") || strings.Contains(out, "No valid checksum")) {
				t.Fatalf("should report a verification failure explicitly: %v\n%s", err, out)
			}
			e.assertNothingInstalledOrStarted()
		})
	}
}

func TestEntryPointRejectsAnArchiveWhoseBinaryIsTheWrongArchitecture(t *testing.T) {
	// The checksum is correct but the archive holds an executable for another architecture: it must not be installed (no fallback, no silent use).
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
	if lerr == nil || !strings.Contains(out, "is not darwin/"+hostArch()) {
		t.Fatalf("should reject the wrong architecture: %v\n%s", lerr, out)
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
		t.Fatalf("all concurrent loads should succeed: %s", m)
	}
	pids := e.waitDaemons(1)
	if n := rs.count("_darwin_"); n != 1 {
		t.Fatalf("concurrent loads should download the asset only once, got %d: %v", n, rs.reqs)
	}

	e.rawTmux("set-option", "-g", "side-status", "off") // the user hides the sidebar
	for i := 0; i < 3; i++ {
		e.mustLoad()
	}
	if got := e.waitDaemons(1); got[0] != pids[0] {
		t.Fatalf("repeated loads must not restart the instance: %v → %v", pids, got)
	}
	if rs.count("_darwin_") != 1 {
		t.Fatalf("must not download again when already installed and the checksum matches: %v", rs.reqs)
	}
	if got := e.sideStatus(); got != "off" {
		t.Fatalf("reloading must not reopen a sidebar the user hid: %q", got)
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
		t.Fatalf("an installed file with a mismatched checksum should be downloaded and verified again: %v", rs.reqs)
	}
	if b, _ := os.ReadFile(bin); strings.Contains(string(b), "evil") {
		t.Fatal("the modified executable should have been replaced")
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
	if err == nil || !strings.Contains(out, "missing required capabilities") || !strings.Contains(out, "side-status-width") {
		t.Fatalf("should report the missing capability explicitly: %v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("a missing capability must not trigger a download: %v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestRunningOutsideTmuxIsRefusedInsteadOfUsingTheDefaultServer(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	out, err := e.loadWith(false)
	if err == nil || !strings.Contains(out, "Run this inside tmux") {
		t.Fatalf("should refuse: %v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("must not download: %v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestDeveloperBuiltBinaryIsUsedExplicitlyWithoutAnyDownload(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	rs.Close() // any download would fail
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}

	devBin := filepath.Join(t.TempDir(), "tmux-opencode-status")
	build := exec.Command("go", "build", "-o", devBin, "./cmd/tmux-opencode-status")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("developer build: %v\n%s", err, out)
	}
	e.rawTmux("set-option", "-g", "@opencode-status-binary", devBin)
	e.mustLoad()
	e.waitDaemons(0 + len(e.daemons())) // the instance's argv points to devBin, which is not inside the plugin's bin/
	out, _ := exec.Command("ps", "-axo", "command=").Output()
	if !strings.Contains(string(out), devBin+" run") {
		t.Fatalf("the daemon should start from the specified developer executable")
	}
	_ = exec.Command("pkill", "-f", devBin).Run()
	if ents, _ := filepath.Glob(filepath.Join(e.plugin, "bin", "*")); len(ents) != 0 {
		t.Fatalf("the developer path must not download or place files: %v", ents)
	}
}

func TestDeveloperBinaryOptionPointingNowhereFailsInsteadOfDownloading(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	e.rawTmux("set-option", "-g", "@opencode-status-binary", "/nonexistent/tmux-opencode-status")
	out, err := e.load()
	if err == nil || !strings.Contains(out, "@opencode-status-binary") {
		t.Fatalf("should report an error explicitly: %v\n%s", err, out)
	}
	if rs.total() != 0 {
		t.Fatalf("must not fall back to downloading: %v", rs.reqs)
	}
	e.assertNothingInstalledOrStarted()
}

func TestFailuresAreAlsoShownThroughTmuxDisplayMessageWhenLoadedByRunShell(t *testing.T) {
	e := newEnv(t)
	rs := newReleaseServer(t, dist)
	rs.Close()
	e.extraEnv = []string{"TMUX_OPENCODE_STATUS_RELEASE_URL=" + rs.URL}
	// Same as TPM: run by the tmux server's run-shell, inheriting its $TMUX.
	cmd := exec.Command("tmux", "-L", e.sock, "-f", "/dev/null", "run-shell", filepath.Join(e.plugin, "tmux-opencode-status.tmux"))
	cmd.Env = e.environ(false)
	_, _ = cmd.CombinedOutput()
	msgs := e.rawTmux("show-messages")
	if !strings.Contains(msgs, "Download failed") {
		t.Fatalf("the failure reason should appear in the tmux messages:\n%s", msgs)
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
	t.Fatalf("daemon %d should exit after the server ends", pid)
}
