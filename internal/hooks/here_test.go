package hooks

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// While a gate runs in the session's working tree (baton verify --here), edits there are refused, the
// model's and its subagents' alike: the gate reads those files as it runs. Edits elsewhere, reading, and
// baton's CLI go through; so do edits once the gate has ended. A gate in a lane holds nothing.
func TestAGateHereHoldsEditsInItsTree(t *testing.T) {
	f := newFixture(t, true)
	checkout := filepath.Dir(f.dir)
	gate := func(status string, here bool) {
		f.set(func(st *state.State) {
			st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Here: here, Status: status}}
		})
	}
	gate(state.JobRunning, true)
	for _, c := range []struct {
		name string
		in   map[string]any
		path string // the edit refused ("" for none)
	}{
		{"an edit in the checkout", edit("Edit", filepath.Join(checkout, "src", "a.go")), "src/a.go"},
		{"a new file there", edit("Write", filepath.Join(checkout, "notes.md")), "notes.md"},
		{"a subagent's edit there", byAgent(edit("Edit", filepath.Join(checkout, "b.go")), "sub-1"), "b.go"},
		{"an edit elsewhere", edit("Edit", filepath.Join(t.TempDir(), "a.go")), ""},
		{"reading", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(checkout, "a.go")}}, ""},
		{"baton's CLI", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "baton status"}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.in["tool_use_id"] = c.name
			out := f.fire("PreToolUse", c.in)
			got := denyReason(out)
			if c.path == "" {
				if got != "" {
					t.Fatalf("refused: %q", got)
				}
				return
			}
			if want := "[baton] Not now: job j1's gate (`make e2e`) runs in this working tree and reads its files as it runs, so baton refuses edits here until it ends."; !strings.HasPrefix(got, want) {
				t.Errorf("refused with %q, want %q", got, want)
			}
			if want := "baton: refused an edit to " + c.path + " — job j1's gate runs in this working tree until it ends"; say(out) != want {
				t.Errorf("said %q, want %q", say(out), want)
			}
			if !f.logged("edit_refused", map[string]any{"tool": c.in["tool_name"], "path": c.path, "job": "j1", "why": "a gate runs here"}) {
				t.Errorf("events:\n%s", f.eventLog())
			}
		})
	}
	// A refused edit runs nothing in the checkout, so it holds no landing.
	for _, call := range f.state().Run.InFlight(f.now) {
		if strings.Contains(call.ID, "there") || strings.Contains(call.ID, "in the checkout") {
			t.Errorf("a refused edit is in flight: %+v", call)
		}
	}
	for _, c := range []struct {
		name   string
		status string
		here   bool
	}{{"the gate ended", state.JobPassed, true}, {"a gate in a lane", state.JobRunning, false}} {
		gate(c.status, c.here)
		if d := denyReason(f.fire("PreToolUse", edit("Edit", filepath.Join(checkout, "src", "a.go")))); d != "" {
			t.Errorf("%s: an edit was refused: %q", c.name, d)
		}
	}
}

// A worker's gate run --here holds its own worktree in the same way.
func TestAWorkersGateHereHoldsItsWorktree(t *testing.T) {
	w, _, tree := newWorker(t)
	w.set(func(st *state.State) {
		st.Jobs = []state.Job{{ID: "P2.j1", Phase: "P2", Kind: state.JobVerify, Command: "make e2e", Here: true, Status: state.JobQueued}}
	})
	out := w.fire("PreToolUse", edit("Edit", filepath.Join(tree, "src", "a.go")))
	if d := denyReason(out); !strings.Contains(d, "job P2.j1's gate (`make e2e`) runs in this working tree") {
		t.Errorf("refused with %q", d)
	}
	if !w.logged("edit_refused", map[string]any{"path": "src/a.go", "job": "P2.j1"}) {
		t.Errorf("events:\n%s", w.eventLog())
	}
}

// An edit refused for a gate here fails open: a panic in the hook exits 0 and writes nothing.
func TestARefusedEditFailsOpen(t *testing.T) {
	f := newFixture(t, true)
	f.set(func(st *state.State) {
		st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Here: true, Status: state.JobRunning}}
	})
	open := func(ctx Context) (*state.Store, error) {
		s, err := OpenRun(f.clock)(ctx)
		if s != nil {
			s.Now = func() time.Time { panic("boom") }
		}
		return s, err
	}
	in := edit("Edit", filepath.Join(filepath.Dir(f.dir), "a.go"))
	in["hook_event_name"], in["session_id"] = "PreToolUse", f.sid
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	code := Dispatch("PreToolUse", bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(Deps{Open: open}))
	if code != 0 || out.Len() != 0 || !strings.Contains(errb.String(), "failed open") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}
