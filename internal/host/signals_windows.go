//go:build windows

package host

import (
	"os"
	"os/exec"

	"github.com/ozzyfromspace/baton/internal/pty"
)

// forwardSignals is a no-op until the ConPTY host lands (P12).
func forwardSignals(*exec.Cmd, pty.PTY, *os.File, bool) func() { return func() {} }

func signalExitCode(*exec.ExitError) int { return 1 }
