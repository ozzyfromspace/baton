//go:build windows

package host

import (
	"os"
	"os/exec"

	"github.com/ozzyfromspace/baton/internal/pty"
)

// hangupSignals end a session whose terminal is gone; Windows can only kill.
var hangupSignals = []os.Signal{os.Kill}

// forwardSignals is a no-op until the ConPTY host lands (P12).
func forwardSignals(*exec.Cmd, pty.PTY, *os.File, bool, func()) func() { return func() {} }

func signalExitCode(*exec.ExitError) int { return 1 }
