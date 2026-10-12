//go:build !windows

package lane

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// running reports whether a process with this pid exists (a zombie counts until it is reaped).
func running(pid int) bool { return syscall.Kill(pid, 0) == nil }

func pgid(pid int) int {
	g, _ := syscall.Getpgid(pid)
	return g
}

func TestProcessHelpers(t *testing.T) {
	start := func() *exec.Cmd {
		cmd := exec.Command("sleep", "300")
		setGroup(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		return cmd
	}
	for _, pid := range []int{-1, 0, 1} {
		if Alive(pid) || Terminate(pid) != nil || KillGroup(pid) != nil {
			t.Errorf("pid %d is taken for a process baton may signal", pid)
		}
	}
	term := start()
	if !Alive(term.Process.Pid) {
		t.Fatal("a running process is not alive")
	}
	if err := Terminate(term.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(); err == nil || !strings.Contains(err.Error(), "terminated") {
		t.Errorf("after Terminate: %v", err)
	}
	if Alive(term.Process.Pid) {
		t.Error("a reaped process is alive")
	}
	kill := start()
	if err := KillGroup(kill.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := kill.Wait(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("after KillGroup: %v", err)
	}
}

// A job's runner is told from the process table: a pid on record that now belongs to another program is
// not one.
func TestIsRunner(t *testing.T) {
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for _, pid := range []int{-1, 0, 1, cmd.Process.Pid} {
		if IsRunner(pid) {
			t.Errorf("pid %d is taken for a runner", pid)
		}
	}
}

// A command given as words runs as those words: sh reads each back as it was given.
func TestJoin(t *testing.T) {
	for _, words := range [][]string{
		{"sh", "hold.sh"},
		{"go", "test", "./...", "-run=TestX", "-count=1"},
		{"sh", "-c", "go test ./... && echo it's ok"},
		{"printf", "%s", "a $HOME `b` \\c \"d\" *", ""},
		{"gh", "run", "watch", "7", "--exit-status"},
	} {
		line := Join(words)
		got, err := exec.Command("sh", "-c", `show() { for a in "$@"; do printf '[%s]' "$a"; done; }; show `+line).Output()
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		want := ""
		for _, w := range words {
			want += "[" + w + "]"
		}
		if string(got) != want {
			t.Errorf("%q runs as %s, want %s", line, got, want)
		}
	}
	if got := Join([]string{"go", "test", "./..."}); got != "go test ./..." {
		t.Errorf("plain words are quoted: %q", got)
	}
}
