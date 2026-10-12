//go:build !windows

package host

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// hangupSignals end a session whose terminal is gone, in order, HangupGrace apart.
var hangupSignals = []os.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGKILL}

// quitSignals end a session the human is leaving baton from, or one baton restarts.
var quitSignals = []os.Signal{syscall.SIGTERM, syscall.SIGKILL}

// killSignal is the last of them, which takes claude's children with it (killTree).
var killSignal os.Signal = syscall.SIGKILL

// signalHandlers say what the signals baton gets do: resize on SIGWINCH, hangup on SIGHUP, and forward on
// SIGTERM, SIGINT and SIGQUIT.
type signalHandlers struct {
	resize  func()
	hangup  func()
	forward func(os.Signal)
}

// forwardSignals hands the signals baton gets to h until the function it returns is called. SIGWINCH
// keeps the lead's terminal size in step with the human's. SIGHUP means baton's own terminal is gone,
// and the termination signals are for claude: either way the session ends.
func forwardSignals(h signalHandlers) func() {
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case s := <-ch:
				switch s {
				case syscall.SIGWINCH:
					h.resize()
				case syscall.SIGHUP:
					h.hangup()
				default:
					h.forward(s)
				}
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

// killTree kills p and every process descended from it. They are stopped first, while their tree is
// listed, so none can start another or be orphaned (and so lost) before it is killed.
func killTree(p *os.Process) {
	stopped := map[int]bool{p.Pid: true}
	syscall.Kill(p.Pid, syscall.SIGSTOP)
	for range 5 {
		more := false
		for _, pid := range descendants(p.Pid) {
			if !stopped[pid] {
				stopped[pid], more = true, true
				syscall.Kill(pid, syscall.SIGSTOP)
			}
		}
		if !more {
			break
		}
	}
	for pid := range stopped {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// descendants lists the processes descended from pid, from ps.
func descendants(pid int) []int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		c, cerr := strconv.Atoi(f[0])
		pp, perr := strconv.Atoi(f[1])
		if cerr == nil && perr == nil {
			children[pp] = append(children[pp], c)
		}
	}
	var all []int
	queue := []int{pid}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, c := range children[next] {
			all = append(all, c)
			queue = append(queue, c)
		}
	}
	return all
}

func signalExitCode(ee *exec.ExitError) int {
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
