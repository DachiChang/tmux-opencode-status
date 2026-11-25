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

// procArgv obtains the exact argv through KERN_PROCARGS2 (ps args loses the boundaries of paths that contain spaces).
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
	if i := bytes.IndexByte(rest, 0); i >= 0 { // skip the exec path and the NUL padding after it
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

// procCwd obtains a process's current working directory with lsof (in cwd mode, a directory's identity is the TUI process's cwd).
// Being unable to read it, or getting no output, is an error; an empty string is never returned as a directory.
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
	return "", errors.New("lsof did not report a working directory")
}
