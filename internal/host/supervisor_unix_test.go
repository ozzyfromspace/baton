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

	"github.com/ozzyfromspace/baton/internal/state"
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

// A hang-up or a termination signal ends the lead as before, and every hidden session with it.
func TestSignalsEndEverySession(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
		env  []string
		want int // the lead's exit code
	}{
		{"SIGHUP", syscall.SIGHUP, []string{"FAKE_HUP=loop"}, 129},
		{"SIGTERM", syscall.SIGTERM, nil, 128 + int(syscall.SIGTERM)},
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
			if r.code != tc.want {
				t.Errorf("the lead ended with %d, want %d", r.code, tc.want)
			}
			for _, h := range workers {
				if code := h.s.ExitCode(); code != 0 || h.input() != "/exit\r" {
					t.Errorf("%s: exit %d, input %q", h.s.Name, code, h.input())
				}
			}
		})
	}
}

// Keys typed into a permission prompt pick one of its options, so a session with a dialog open is never
// ended by typing /exit: it gets SIGTERM.
func TestEndNeverTypesIntoADialog(t *testing.T) {
	r := startRig(t, func(c *Config) { c.HangupGrace = 200 * time.Millisecond })
	sub, err := r.proj.NewWorker("sess-1", "P3", "sess-w", "inst-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub.Update(func(st *state.State) error {
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Since: time.Now()})
		return nil
	})
	h := spawn(t, r.sv, "P3", nil)
	h.s.End()
	if code := h.wait(); code != 128+int(syscall.SIGTERM) || strings.Contains(h.input(), "/exit") {
		t.Errorf("exit %d, input %q; want SIGTERM and nothing typed", code, h.input())
	}
	r.quit()
}

// Every session has its own keys, draft and run: what is typed into one is never another's.
func TestEachSessionHasItsOwnView(t *testing.T) {
	lead, worker := &recordingController{typed: true}, &recordingController{typed: true}
	r := startRig(t, func(c *Config) { c.Controller = lead })
	if _, err := r.proj.NewWorker("sess-1", "P3", "sess-w", "inst-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	h := spawn(t, r.sv, "P3", worker)

	r.stdinW.Write([]byte("draft"))
	h.s.Keys([]byte("abc"))
	time.Sleep(3 * TickInterval)
	lv, wv := lead.last(), worker.last()
	if !lv.Draft || lv.DraftText != "draft" || lv.Store == nil || lv.Store.Ref != "sess-1" {
		t.Errorf("the lead's view: %+v", lv)
	}
	if !wv.Draft || wv.DraftText != "abc" || !wv.Owner || wv.Store == nil || wv.Store.Ref != "sess-1/P3" {
		t.Errorf("the worker's view: %+v", wv)
	}

	h.s.ClearInput()
	time.Sleep(3 * TickInterval)
	if lv, wv := lead.last(), worker.last(); !lv.Draft || wv.Draft {
		t.Errorf("clearing the worker's input: lead draft %v, worker draft %v", lv.Draft, wv.Draft)
	}
	r.quit()
	h.wait()
	if got := h.input(); got != "abc\x03/exit\r" {
		t.Errorf("the worker got %q", got)
	}
	if got := r.input(); strings.Contains(got, "abc") || !strings.HasPrefix(got, "draft") {
		t.Errorf("the lead got %q", got)
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

// A run's events stay its own: the worker's session logs to its sub-run, never to the lead's run.
func TestAWorkersHostEventsGoToItsSubRun(t *testing.T) {
	r := startRig(t, nil)
	if _, err := r.proj.NewWorker("sess-1", "P3", "sess-w", "inst-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	h := spawn(t, r.sv, "P3", nil)
	time.Sleep(3 * TickInterval)
	r.quit()
	h.wait()
	sub, _ := r.proj.Run("sess-1/P3", "")
	lead, _ := r.proj.Run("sess-1", "")
	read := func(s *state.Store) string {
		b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
		return string(b)
	}
	if got := strings.Count(read(lead), `"kind":"host_started"`); got != 1 {
		t.Errorf("the lead's run logged host_started %d times:\n%s", got, read(lead))
	}
	if ev := read(sub); !strings.Contains(ev, `"kind":"host_started"`) || !strings.Contains(ev, fmt.Sprintf(`"worker":%q`, "P3")) {
		t.Errorf("the worker's sub-run:\n%s", ev)
	}
}
