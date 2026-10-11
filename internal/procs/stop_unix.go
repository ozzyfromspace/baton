//go:build !windows

package procs

import (
	"errors"
	"slices"
	"syscall"
	"time"
)

// Stop asks each of pids to stop (SIGTERM), each on its own and never its process group, and kills
// (SIGKILL) those still running after grace. It returns those it could not stop.
func Stop(pids []int, grace time.Duration) []int {
	var asked, left []int
	for _, pid := range pids {
		if pid <= 1 { // 0 and -1 would signal a whole group, or every process
			left = append(left, pid)
			continue
		}
		switch err := syscall.Kill(pid, syscall.SIGTERM); {
		case err == nil:
			asked = append(asked, pid)
		case !errors.Is(err, syscall.ESRCH): // gone already
			left = append(left, pid)
		}
	}
	asked = running(asked, grace)
	for _, pid := range asked {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	return append(left, running(asked, time.Second)...)
}

// running waits up to d for pids to end, and returns those still running.
func running(pids []int, d time.Duration) []int {
	deadline := time.Now().Add(d)
	for {
		pids = slices.DeleteFunc(pids, func(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
		if len(pids) == 0 || time.Now().After(deadline) {
			return pids
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Group is this process's process group.
func Group() int { return syscall.Getpgrp() }
