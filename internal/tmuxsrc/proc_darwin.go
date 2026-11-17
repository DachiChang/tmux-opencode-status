package tmuxsrc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// procArgv 以 KERN_PROCARGS2 取得精確 argv（ps args 會丟失含空白路徑的邊界）。
func procArgv(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(raw) < 4 {
		return nil, nil
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	if i := bytes.IndexByte(rest, 0); i >= 0 { // 跳過 exec path 與其後的 NUL 填充
		rest = rest[i:]
	}
	rest = bytes.TrimLeft(rest, "\x00")
	var argv []string
	for _, p := range bytes.Split(rest, []byte{0}) {
		if len(argv) == argc {
			break
		}
		argv = append(argv, string(p))
	}
	return argv, nil
}

// procCwd 以 lsof 取得行程目前工作目錄（cwd 模式的目錄身分是 TUI 行程 cwd）。
// 讀不到或沒有輸出都是錯誤，絕不回傳空字串當成目錄。
func procCwd(pid int) (string, error) {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof：%w", err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n") && len(l) > 1 {
			return l[1:], nil
		}
	}
	return "", errors.New("lsof 沒有回報工作目錄")
}
