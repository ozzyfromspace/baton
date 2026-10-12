//go:build !windows

package host

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hidden is a hidden session started on fakeclaude, and the directory where it records what it got.
type hidden struct {
	t   *testing.T
	s   *Session
	out string
}

// spawn starts a hidden session named name on fakeclaude, in a directory of its own.
func spawn(t *testing.T, sv *Supervisor, name string, ctl Controller, env ...string) *hidden {
	t.Helper()
	h := &hidden{t: t, out: t.TempDir()}
	s, err := sv.Spawn(Spec{
		Name: name, Dir: h.out, Args: []string{"--model", "haiku"}, Instance: "inst-1-" + name, Controller: ctl,
		Env: append(os.Environ(), append([]string{"FAKE_OUT=" + h.out, "FAKE_EXIT=7"}, env...)...),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	t.Cleanup(func() { s.End(); s.Quit(); <-s.Done() })
	h.waitTail("fake-claude ready")
	return h
}

func (h *hidden) waitTail(want string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(string(h.s.Tail()), want) {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s never showed %q; got %q", h.s.Name, want, h.s.Tail())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *hidden) input() string {
	b, _ := os.ReadFile(filepath.Join(h.out, "input.bin"))
	return string(b)
}

func (h *hidden) wait() int {
	h.t.Helper()
	select {
	case <-h.s.Done():
	case <-time.After(5 * time.Second):
		h.t.Fatalf("%s did not end", h.s.Name)
	}
	return h.s.ExitCode()
}

// A worker's screen is nobody's terminal: baton reads all of it, so claude never blocks on output, and
// keeps the tail and the modes it set for a human who attaches.
func TestAHiddenSessionIsDrainedAndKeepsItsTail(t *testing.T) {
	r := startRig(t, nil)
	h := spawn(t, r.sv, "P3", nil, "FAKE_FLOOD=30000", "FAKE_MODES=1")
	h.waitTail("flood done\r\n")
	tail := string(h.s.Tail())
	if len(tail) != TailSize || !strings.HasSuffix(tail, "line 30000\r\nflood done\r\n") || strings.Contains(tail, "line 00001\r\n") {
		t.Fatalf("tail: %d bytes, from %q to %q", len(tail), tail[:24], tail[len(tail)-24:])
	}
	if got, want := string(h.s.Modes()), "\x1b[?1004h\x1b[?2004h\x1b[?25l"; got != want {
		t.Errorf("modes %q, want %q", got, want)
	}
	if !h.s.Hidden() || r.sv.Session("P3") != h.s || r.sv.Lead().Hidden() {
		t.Error("the supervisor lists its sessions wrong")
	}
	r.quit()
	if code := h.wait(); code != 0 || !strings.HasSuffix(h.input(), "/exit\r") {
		t.Errorf("the worker did not end with the lead: exit %d, input %q", code, h.input())
	}
	r.screenMu.Lock()
	defer r.screenMu.Unlock()
	if strings.Contains(r.screen.String(), "line 0") {
		t.Error("a hidden session's output reached the human's terminal")
	}
}

// BATON_SCREEN_DUMP=1: a hidden session's latest screen goes into its dump file as it ends, after the
// screens of the sessions that ended before it, to diagnose a failure with.
func TestAHiddenSessionKeepsItsScreenAsItEnds(t *testing.T) {
	sv := NewSupervisor(Config{Claude: fakeClaude, TypeDelay: time.Millisecond, EnterDelay: 5 * time.Millisecond, HangupGrace: 200 * time.Millisecond})
	dump := filepath.Join(t.TempDir(), "screen.log")
	var codes []int
	for i := 0; i < 2; i++ {
		h := &hidden{t: t, out: t.TempDir()}
		s, err := sv.Spawn(Spec{Name: "P3", Dir: h.out, Instance: "inst-1-P3", Dump: dump,
			Env: append(os.Environ(), "FAKE_OUT="+h.out)})
		if err != nil {
			t.Fatal(err)
		}
		h.s = s
		h.waitTail("fake-claude ready")
		s.End()
		codes = append(codes, h.wait())
	}
	b, err := os.ReadFile(dump)
	got := string(b)
	if err != nil || strings.Count(got, "fake-claude ready") != 2 {
		t.Fatalf("the screens kept: %v\n%s", err, got)
	}
	first, second := strings.Index(got, "the screen of P3 (pid "), strings.LastIndex(got, "the screen of P3 (pid ")
	if first == second || !strings.Contains(got[first:second], fmt.Sprintf("exited with %d", codes[0])) ||
		!strings.Contains(got[second:], fmt.Sprintf("exited with %d", codes[1])) {
		t.Errorf("the screens kept, in order:\n%s", got)
	}
}

// End asks claude to exit as a human would, and makes sure it does: SIGTERM, then SIGKILL with the
// processes it started, which would outlive it otherwise (spike 23).
func TestEndFallsBackToSignals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   []string
		draft string // typed into the session before it is ended
		want  int
		input string // what claude got, from the draft on
		child bool   // claude started a child, which must not outlive it
	}{
		{"claude exits on /exit", nil, "", 0, "/exit\r", false},
		{"a draft is cleared first", nil, "half", 0, "half\x03/exit\r", false},
		{"claude ignores /exit", []string{"FAKE_SLASH=ignore"}, "", 128 + int(syscall.SIGTERM), "/exit\r", false},
		{"claude ignores /exit and SIGTERM", []string{"FAKE_SLASH=ignore", "FAKE_TERM=ignore", "FAKE_CHILD=1"}, "", 128 + int(syscall.SIGKILL), "/exit\r", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sv := NewSupervisor(Config{Claude: fakeClaude, TypeDelay: time.Millisecond, EnterDelay: 5 * time.Millisecond, HangupGrace: 200 * time.Millisecond})
			h := spawn(t, sv, "P3", nil, tc.env...)
			child := 0
			if tc.draft != "" {
				h.s.Keys([]byte(tc.draft))
				h.waitTail("got:")
			}
			if tc.child {
				child = h.childPid()
			}
			h.s.End()
			if code := h.wait(); code != tc.want {
				t.Errorf("exit %d, want %d", code, tc.want)
			}
			if got := h.input(); got != tc.input {
				t.Errorf("claude got %q, want %q", got, tc.input)
			}
			if child != 0 {
				deadline := time.Now().Add(2 * time.Second)
				for syscall.Kill(child, 0) == nil {
					if time.Now().After(deadline) {
						syscall.Kill(child, syscall.SIGKILL)
						t.Fatalf("claude's child %d outlived it", child)
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
		})
	}
}

func (h *hidden) childPid() int {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(h.out, "child.pid")); err == nil {
			if pid, err := strconv.Atoi(string(b)); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("claude started no child")
	return 0
}

// A hidden session baton ends says so; one whose claude exits by itself says that.
func TestAHiddenSessionSaysWhatEndedIt(t *testing.T) {
	r := startRig(t, func(c *Config) { c.HangupGrace = 200 * time.Millisecond })
	ended, exited := spawn(t, r.sv, "P3", nil), spawn(t, r.sv, "P4", nil)
	ended.s.End()
	exited.s.Keys([]byte("Q"))
	if code := ended.wait(); code != 0 || ended.s.EndedBy() != EndedQuit {
		t.Errorf("ended: %d by %q, want 0 by %q", code, ended.s.EndedBy(), EndedQuit)
	}
	if code := exited.wait(); code != 7 || exited.s.EndedBy() != EndedSelf {
		t.Errorf("exited: %d by %q, want 7 by %q", code, exited.s.EndedBy(), EndedSelf)
	}
	r.quit()
}

// A hang-up or a termination signal ends the lead as before, and every hidden session with it.
func TestSignalsEndEverySession(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
		env  []string
		want int    // the lead's exit code
		by   string // and what ended it
	}{
		{"SIGHUP", syscall.SIGHUP, []string{"FAKE_HUP=loop"}, 129, EndedHangup},
		{"SIGTERM", syscall.SIGTERM, nil, 128 + int(syscall.SIGTERM), EndedSignal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := startRig(t, func(c *Config) { c.HangupGrace = 200 * time.Millisecond }, tc.env...)
			workers := []*hidden{spawn(t, r.sv, "P3", nil), spawn(t, r.sv, "P4", nil)}
			// Registered here too, so a signal that arrives before the host's own handler cannot end the
			// test binary.
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, tc.sig)
			defer signal.Stop(ch)
			syscall.Kill(os.Getpid(), tc.sig)
			for _, h := range workers {
				h.wait()
			}
			// The workers end with the host: none starts in their place, even before the lead has gone.
			if _, err := r.sv.Spawn(Spec{Name: "P5", Instance: "inst-1-P5"}); !errors.Is(err, ErrClosed) {
				t.Errorf("a session started after the signal: %v", err)
			}
			select {
			case <-r.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the host outlived the signal")
			}
			if r.code != tc.want || r.by != tc.by {
				t.Errorf("the lead ended with %d by %q, want %d by %q", r.code, r.by, tc.want, tc.by)
			}
			for _, h := range workers {
				if code := h.s.ExitCode(); code != 0 || h.input() != "/exit\r" {
					t.Errorf("%s: exit %d, input %q", h.s.Name, code, h.input())
				}
			}
		})
	}
}

func TestSpawnRefuses(t *testing.T) {
	r := startRig(t, nil)
	spawn(t, r.sv, "P3", nil)
	for _, tc := range []struct {
		name string
		spec Spec
	}{
		{"a name already running", Spec{Name: "P3", Instance: "inst-1-P3"}},
		{"no name", Spec{Instance: "inst-1-P5"}},
		{"no instance", Spec{Name: "P5"}},
	} {
		if _, err := r.sv.Spawn(tc.spec); err == nil {
			t.Errorf("%s: spawned", tc.name)
		}
	}
	r.quit()
	if _, err := r.sv.Spawn(Spec{Name: "P5", Instance: "inst-1-P5"}); !errors.Is(err, ErrClosed) {
		t.Errorf("after the lead ended: %v", err)
	}
}
