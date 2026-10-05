//go:build !windows

package host

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ozzyfromspace/baton/internal/pty"
	"golang.org/x/term"
)

// hangupSignals end a session whose terminal is gone, in order, HangupGrace apart.
var hangupSignals = []os.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGKILL}

// forwardSignals keeps claude's terminal size in step with the user's (SIGWINCH) and passes
// termination signals sent to baton on to claude. SIGHUP means baton's own terminal is gone, so it
// calls hangup, which makes sure claude ends too. It returns a function that stops forwarding.
func forwardSignals(cmd *exec.Cmd, p pty.PTY, stdout *os.File, interactive bool, hangup func()) func() {
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case s := <-ch:
				if s == syscall.SIGWINCH {
					if interactive {
						if c, r, err := term.GetSize(int(stdout.Fd())); err == nil {
							p.Resize(uint16(r), uint16(c))
						}
					}
					continue
				}
				if s == syscall.SIGHUP {
					hangup()
					continue
				}
				if cmd.Process != nil {
					cmd.Process.Signal(s)
				}
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

func signalExitCode(ee *exec.ExitError) int {
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
