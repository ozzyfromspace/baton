//go:build windows

package pty

import "os/exec"

// start is not implemented on Windows yet; ConPTY support is planned (P12).
func start(*exec.Cmd, uint16, uint16) (PTY, error) {
	return nil, ErrUnsupported
}
