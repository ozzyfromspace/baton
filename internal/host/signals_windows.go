//go:build windows

package host

import (
	"os"
	"os/exec"
)

// hangupSignals end a session whose terminal is gone; Windows can only kill.
var hangupSignals = []os.Signal{os.Kill}

// quitSignals end a session the human is leaving baton from, or one baton ends.
var quitSignals = []os.Signal{os.Kill}

var killSignal = os.Kill

type signalHandlers struct {
	resize  func()
	hangup  func()
	forward func(os.Signal)
}

// forwardSignals is a no-op until the ConPTY host lands.
func forwardSignals(signalHandlers) func() { return func() {} }

func killTree(p *os.Process) { p.Kill() }

func signalExitCode(*exec.ExitError) int { return 1 }
