//go:build !windows

package pty

import (
	"os"
	"os/exec"

	cpty "github.com/creack/pty"
)

type unixPTY struct{ *os.File }

func (p unixPTY) Resize(rows, cols uint16) error {
	return cpty.Setsize(p.File, &cpty.Winsize{Rows: rows, Cols: cols})
}

func start(cmd *exec.Cmd, rows, cols uint16) (PTY, error) {
	f, err := cpty.StartWithSize(cmd, &cpty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}
	return unixPTY{f}, nil
}
