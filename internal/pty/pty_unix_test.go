//go:build !windows

package pty

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// readUntil reads from p until want appears or the deadline passes.
func readUntil(t *testing.T, p PTY, want string) string {
	t.Helper()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 4096)
		for !strings.Contains(buf.String(), want) {
			n, err := p.Read(b)
			buf.Write(b[:n])
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q; got %q", want, buf.String())
	}
	return buf.String()
}

func TestChildSeesATerminalOfTheGivenSize(t *testing.T) {
	p, err := Start(exec.Command("sh", "-c", "stty size; [ -t 0 ] && echo is-a-tty"), 33, 101)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	out := readUntil(t, p, "is-a-tty")
	if !strings.Contains(out, "33 101") {
		t.Fatalf("stty size: %q", out)
	}
}

func TestInputAndResizeReachTheChild(t *testing.T) {
	p, err := Start(exec.Command("sh", "-c", "read line; echo got:$line; sleep 0.2; stty size"), 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Resize(40, 120); err != nil {
		t.Fatal(err)
	}
	io.WriteString(p, "hello\r")
	out := readUntil(t, p, "40 120")
	if !strings.Contains(out, "got:hello") {
		t.Fatalf("input did not arrive: %q", out)
	}
}
