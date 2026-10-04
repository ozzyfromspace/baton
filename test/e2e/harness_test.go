//go:build !windows

// Package e2e drives real claude sessions through baton, the way a human at a terminal would. The tests
// cost a few cents each and only run with BATON_E2E=1 (make e2e). They assert on .baton/events.jsonl
// and files the scenario writes, never on screen contents, which are only kept for debugging.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	cpty "github.com/creack/pty"
)

var batonBin string // built once in TestMain

func TestMain(m *testing.M) {
	if os.Getenv("BATON_E2E") != "1" {
		fmt.Println("e2e: skipped (set BATON_E2E=1 to run real claude sessions; costs a few cents)")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "baton-e2e-bin")
	if err != nil {
		panic(err)
	}
	batonBin = filepath.Join(dir, "baton")
	if out, err := exec.Command("go", "build", "-o", batonBin, "../../cmd/baton").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]`)

// Session is one baton-hosted claude in a pseudo-terminal, with the test acting as the terminal.
type Session struct {
	t       *testing.T
	Dir     string
	cmd     *exec.Cmd
	pty     *os.File
	mu      sync.Mutex
	screen  bytes.Buffer
	lastOut time.Time
	exited  chan struct{}
}

// NewProject makes a throwaway git repository to run a session in.
func NewProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s", out)
	}
	return dir
}

// Start runs `baton <args…>` in dir. Inherited CLAUDE* variables are removed (a claude started from
// inside another Claude Code session would otherwise not save transcripts), and the directory holding
// the baton binary is put first on PATH, as the plugin's bin/ is in real use.
func Start(t *testing.T, dir string, args ...string) *Session {
	t.Helper()
	cmd := exec.Command(batonBin, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE") && !strings.HasPrefix(kv, "BATON_") && !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "PATH="+filepath.Dir(batonBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	p, err := cpty.StartWithSize(cmd, &cpty.Winsize{Rows: 50, Cols: 160})
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{t: t, Dir: dir, cmd: cmd, pty: p, lastOut: time.Now(), exited: make(chan struct{})}
	go s.pump()
	go func() { cmd.Wait(); close(s.exited) }()
	t.Cleanup(s.Close)
	return s
}

func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.screen.Write(ansi.ReplaceAll(buf[:n], nil))
			s.lastOut = time.Now()
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Screen is everything shown so far with escape sequences removed (spaces too, since the TUI positions
// words with cursor moves). For debugging and coarse waits only.
func (s *Session) Screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.ReplaceAll(s.screen.String(), " ", "")
}

// Until polls cond until it holds or timeout passes.
func (s *Session) Until(what string, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		select {
		case <-s.exited:
			s.t.Logf("session exited while waiting for %s", what)
			return cond()
		case <-time.After(250 * time.Millisecond):
		}
	}
	s.t.Logf("timed out waiting for %s", what)
	return false
}

// WaitScreen waits for text (spaces ignored) to appear on screen.
func (s *Session) WaitScreen(text string, timeout time.Duration) bool {
	want := strings.ReplaceAll(text, " ", "")
	return s.Until("screen "+text, timeout, func() bool { return strings.Contains(s.Screen(), want) })
}

// WaitQuiet waits until the screen has been still for d.
func (s *Session) WaitQuiet(d, timeout time.Duration) bool {
	return s.Until("quiet", timeout, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return time.Since(s.lastOut) >= d
	})
}

// Trust answers Claude Code's folder-trust prompt if it appears.
func (s *Session) Trust() {
	if s.WaitScreen("trust this folder", 20*time.Second) {
		time.Sleep(800 * time.Millisecond)
		s.pty.Write([]byte("\x1b[B"))
		time.Sleep(400 * time.Millisecond)
		s.pty.Write([]byte("\r"))
	}
}

// Type types text like a person, then presses Enter.
func (s *Session) Type(text string) {
	for _, r := range text {
		s.pty.Write([]byte(string(r)))
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	s.pty.Write([]byte("\r"))
}

// Events reads .baton/events.jsonl.
func (s *Session) Events() []map[string]any {
	f, err := os.Open(filepath.Join(s.Dir, ".baton", "events.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// WaitEvent waits for an event of the given kind and returns it.
func (s *Session) WaitEvent(kind string, timeout time.Duration) map[string]any {
	var found map[string]any
	s.Until("event "+kind, timeout, func() bool {
		for _, e := range s.Events() {
			if e["kind"] == kind {
				found = e
				return true
			}
		}
		return false
	})
	return found
}

// Close ends the session and keeps its screen next to the project for debugging failures.
func (s *Session) Close() {
	select {
	case <-s.exited:
	default:
		s.cmd.Process.Signal(os.Interrupt)
		select {
		case <-s.exited:
		case <-time.After(3 * time.Second):
			s.cmd.Process.Kill()
		}
	}
	s.pty.Close()
	if s.t.Failed() {
		s.t.Logf("screen tail:\n%s", tail(s.Screen(), 3000))
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
