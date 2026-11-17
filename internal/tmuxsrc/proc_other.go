//go:build !darwin

package tmuxsrc

import "errors"

func procArgv(pid int) ([]string, error) { return nil, errors.New("僅支援 macOS") }
func procCwd(pid int) string             { return "" }
