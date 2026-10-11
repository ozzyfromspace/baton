//go:build darwin || linux

package procs

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"
	"testing"
	"time"
)

// List finds a process left running, with what marks it as a session's and where it runs.
func TestList(t *testing.T) {
	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	dir := t.TempDir()
	cmd := helper(t, "sleep", dir, "BATON_INSTANCE=procs-test", "BATON_DIR="+dir)
	real, _ := filepath.EvalSymlinks(dir)
	var p *Proc
	for range 50 {
		got, _ = List()
		if i := slices.IndexFunc(got, func(p Proc) bool { return p.PID == cmd.Process.Pid }); i >= 0 && got[i].Cwd != "" && got[i].Env != nil {
			p = &got[i]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p == nil {
		t.Fatalf("List never found pid %d", cmd.Process.Pid)
	}
	if p.PPID != os.Getpid() || p.PGID != syscallPgid(t) || p.Cwd != real || p.Env["BATON_INSTANCE"] != "procs-test" || p.Env["BATON_DIR"] != dir || p.Comm == "" {
		t.Errorf("found %+v (want parent %d, cwd %s)", *p, os.Getpid(), real)
	}
	if _, ok := p.Env["PATH"]; ok {
		t.Error("only BATON_* variables are kept")
	}
	if slices.ContainsFunc(got, func(p Proc) bool { return p.PID == os.Getpid() }) {
		t.Error("List lists the process that reads it")
	}
}

func syscallPgid(t *testing.T) int {
	pgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return pgid
}

// Stop ends each process on its own: SIGTERM, then SIGKILL for one that ignores it.
func TestStop(t *testing.T) {
	polite, stubborn := helper(t, "sleep", t.TempDir()), helper(t, "stubborn", t.TempDir())
	time.Sleep(200 * time.Millisecond) // the stubborn one ignores SIGTERM by now
	start := time.Now()
	if left := Stop([]int{polite.Process.Pid, stubborn.Process.Pid, 0, -1}, 300*time.Millisecond); !reflect.DeepEqual(left, []int{0, -1}) {
		t.Errorf("Stop left %v", left)
	}
	if took := time.Since(start); took < 300*time.Millisecond {
		t.Errorf("the stubborn one was killed after %v, before its grace", took)
	}
}
