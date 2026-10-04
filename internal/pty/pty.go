// Package pty starts a child process attached to a pseudo-terminal, behind an interface so the host
// does not care whether it runs on a Unix PTY or (later) a Windows ConPTY.
package pty

import (
	"errors"
	"io"
	"os/exec"
)

// ErrUnsupported is returned on platforms without a pseudo-terminal implementation yet.
var ErrUnsupported = errors.New("pty: not supported on this platform yet")

// PTY is the controlling side of a pseudo-terminal whose other side is the child's terminal.
// Reads return what the child writes to its terminal; writes arrive as the child's keyboard input.
type PTY interface {
	io.ReadWriteCloser
	// Resize sets the child's terminal size; the child receives SIGWINCH (or the platform equivalent).
	Resize(rows, cols uint16) error
}

// Start runs cmd attached to a new pseudo-terminal of the given size.
func Start(cmd *exec.Cmd, rows, cols uint16) (PTY, error) {
	return start(cmd, rows, cols)
}
