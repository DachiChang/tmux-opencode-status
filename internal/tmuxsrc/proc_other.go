//go:build !darwin

package tmuxsrc

import "errors"

func procArgv(pid int) ([]string, error) { return nil, errors.New("only macOS is supported") }
func procCwd(pid int) (string, error)    { return "", errors.New("only macOS is supported") }
