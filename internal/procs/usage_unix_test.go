//go:build darwin || linux

package procs

import (
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"
)

// group starts sh -c script in a process group of its own, and kills the group when the test ends.
func group(t *testing.T, script string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	return cmd.Process.Pid
}

// usage reads the tree of group pgid once it has n processes.
func usage(t *testing.T, pgid, n int) Usage {
	t.Helper()
	for range 100 {
		u, err := TreeUsage(pgid)
		if err != nil {
			t.Fatalf("TreeUsage(%d): %v", pgid, err)
		}
		if len(u) == n {
			return u
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("group %d never had %d processes", pgid, n)
	return nil
}

// A tree that waits reads the same twice; one at work reads more the second time. A platform binary's
// time reads too (macOS hides its environment, not its time).
func TestTreeUsage(t *testing.T) {
	idle := group(t, "/bin/sleep 60 & wait")
	busy := group(t, "while :; do :; done")
	first := usage(t, idle, 2)
	if !slices.Contains(keys(first), idle) {
		t.Fatalf("the group's leader is not in its tree: %v", first)
	}
	before := usage(t, busy, 1)
	time.Sleep(300 * time.Millisecond)
	if again := usage(t, idle, 2); !equal(first, again) {
		t.Errorf("a tree that only waits read %v, then %v", first, again)
	}
	if after := usage(t, busy, 1); after[busy] <= before[busy] {
		t.Errorf("a tree at work read %v, then %v", before, after)
	}
	syscall.Kill(-idle, syscall.SIGKILL)
	usage(t, idle, 0) // a group that is gone has nothing running
}

func keys(u Usage) []int {
	var out []int
	for pid := range u {
		out = append(out, pid)
	}
	return out
}

func equal(a, b Usage) bool {
	if len(a) != len(b) {
		return false
	}
	for pid, v := range a {
		if w, ok := b[pid]; !ok || w != v {
			return false
		}
	}
	return true
}
