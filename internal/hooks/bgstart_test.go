package hooks

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// scratch is the scratchpad_dir Claude Code 2.1.318 gives every hook of session sess-1 (spike 45), and
// tasksOf the folder beside it where each background task writes its output.
const (
	scratch = "/private/tmp/claude-501/-Users-me-app/sess-1/scratchpad"
	tasksOf = "/private/tmp/claude-501/-Users-me-app/sess-1/tasks"
)

// inSession is in as a hook of the session gives it: with its scratchpad.
func inSession(in map[string]any) map[string]any {
	in["scratchpad_dir"] = scratch
	return in
}

// Every start of background work is recorded in the loop's run, whatever told the model of it: the lead's
// own, a subagent's of its model (not told, logged background_started), one started while baton does not
// drive the plan (no phase: the conversation's), and a Monitor. Each keeps its kind, tool, command,
// description, phase, plan generation and output file; a Stop that no longer lists a task forgets its start.
func TestTheStartOfBackgroundWorkIsRecorded(t *testing.T) {
	f := newFixture(t, true)
	gen := f.state().Generation
	loop := "until [ -s " + tasksOf + "/bp.output ]; do sleep 5; done"
	f.fire("PostToolUse", inSession(bgBash(loop, "b1", false)))
	st := f.state()
	want := state.BackgroundStart{Type: state.TaskShell, Tool: "Bash", Command: loop, Description: clip("run "+loop, describeClip),
		Phase: "P0", Generation: gen, Output: tasksOf + "/b1.output", At: f.now}
	if got := st.Run.BackgroundStarts["b1"]; got != want {
		t.Fatalf("the lead's start:\n%+v\nwant\n%+v", got, want)
	}
	if st.Run.TasksDir != tasksOf || !slices.Equal(st.Run.BackgroundTold, []string{"b1"}) {
		t.Fatalf("tasks dir %q, told %v", st.Run.TasksDir, st.Run.BackgroundTold)
	}
	if !f.logged("background_told", map[string]any{"task": "b1", "phase": "P0", "output": tasksOf + "/b1.output"}) || len(f.eventsOf("background_started")) != 0 {
		t.Fatalf("events: %s", f.eventLog())
	}
	if got := addedContext(f.fire("PostToolUse", inSession(bgBash("npm run lint", "b9", false)))); !strings.Contains(got, "baton's reviewer looks at a background shell that writes nothing for 30 minutes") {
		t.Errorf("a shell's start does not say when baton may stop it:\n%s", got)
	}

	// A subagent of the model's: recorded, its agent named, not told.
	if out := f.fire("PostToolUse", inSession(byAgent(bgBash("pnpm dev", "b2", false), "sub-1"))); out != nil {
		t.Fatalf("a subagent was told: %v", out)
	}
	if got := f.state().Run.BackgroundStarts["b2"]; got.Agent != "sub-1" || got.Phase != "P0" || got.Type != state.TaskShell || slices.Contains(f.state().Run.BackgroundTold, "b2") {
		t.Fatalf("the subagent's start: %+v", got)
	}
	if !f.logged("background_started", map[string]any{"task": "b2", "agent": "sub-1", "phase": "P0", "command": "pnpm dev", "output": tasksOf + "/b2.output"}) {
		t.Fatalf("events: %s", f.eventLog())
	}

	// A Monitor, and a shell started while the human has the wheel: no phase.
	f.fire("PostToolUse", inSession(map[string]any{"tool_name": "Monitor", "tool_input": map[string]any{"command": "tail -F app.log", "description": "watch the log"},
		"tool_response": map[string]any{"taskId": "m1"}}))
	f.set(func(st *state.State) { st.Mode = state.ModePaused })
	f.fire("PostToolUse", inSession(bgBash("make watch", "b3", false)))
	st = f.state()
	if m := st.Run.BackgroundStarts["m1"]; m.Type != state.TaskMonitor || m.Tool != "Monitor" {
		t.Errorf("the Monitor's start: %+v", m)
	}
	if b := st.Run.BackgroundStarts["b3"]; b.Phase != "" || b.Generation != gen || slices.Contains(st.Run.BackgroundTold, "b3") {
		t.Errorf("a start while paused: %+v", b)
	}
	if !f.logged("background_started", map[string]any{"task": "b3", "phase": ""}) {
		t.Errorf("events: %s", f.eventLog())
	}

	// The Stop's list keeps the commands Claude Code gives, and forgets the starts it no longer lists.
	f.set(func(st *state.State) { st.Mode = state.ModeRunning })
	f.fire("Stop", inSession(map[string]any{"background_tasks": []any{
		map[string]any{"id": "b1", "type": "shell", "status": "running", "description": "wait", "command": loop},
		map[string]any{"id": "b2", "type": "shell", "status": "running", "command": "pnpm dev"},
	}}))
	st = f.state()
	if ids := slices.Sorted(maps.Keys(st.Run.BackgroundStarts)); !slices.Equal(ids, []string{"b1", "b2"}) {
		t.Fatalf("starts after the Stop: %v", ids)
	}
	if bg := st.Run.Background; len(bg) != 2 || bg[0].Command != loop {
		t.Fatalf("the Stop's tasks: %+v", bg)
	}
}

// A phase agent's starts, and its model's subagents', are recorded in its sub-run, in its phase: never in the
// lead's run, whose Stop counts none of them as its own.
func TestAPhaseAgentsBackgroundStartIsRecordedInItsSubRun(t *testing.T) {
	w, lead, tree := newWorker(t)
	w.fire("PostToolUse", inSession(bgBash("go test ./...", "b1", true)))
	sub := byAgent(bgBash("pnpm dev", "b2", false), "sub-x")
	sub["cwd"] = tree
	w.fire("PostToolUse", inSession(sub))
	st := w.state()
	for id, agent := range map[string]string{"b1": "", "b2": "sub-x"} {
		if b, ok := st.Run.BackgroundStarts[id]; !ok || b.Phase != "P2" || b.Agent != agent || b.Output != tasksOf+"/"+id+".output" {
			t.Errorf("%s in the sub-run: %+v", id, b)
		}
	}
	if !w.logged("background_started", map[string]any{"task": "b2", "agent": "sub-x", "phase": "P2"}) {
		t.Errorf("the agent's events: %s", w.eventLog())
	}
	if len(lead.state().Run.BackgroundStarts) != 0 {
		t.Fatalf("the lead recorded the agent's: %+v", lead.state().Run.BackgroundStarts)
	}
	lead.fire("PostToolUse", inSession(bgBash("npm test", "b3", false)))
	lead.fire("Stop", inSession(map[string]any{"background_tasks": shells("b1", "b2", "b3")}))
	if bg := lead.state().Run.Background; len(bg) != 1 || bg[0].ID != "b3" {
		t.Fatalf("the lead's background work: %+v", bg)
	}
}

// A Stop records when a task that the loop's own work waits on (its command names the other's output file)
// was first no longer listed, whoever started it.
func TestAStopRecordsWhenAWaitedTaskEnded(t *testing.T) {
	f := newFixture(t, true)
	loop := "until [ -s " + tasksOf + "/bp.output ] && tail -2 " + tasksOf + "/bp.output | grep -q passed; do sleep 5; done"
	f.fire("PostToolUse", inSession(bgBash(loop, "bl", false)))
	f.fire("Stop", inSession(map[string]any{"background_tasks": shells("bl", "bp")}))
	if gone := f.state().Run.Gone; gone != nil {
		t.Fatalf("gone while listed: %v", gone)
	}
	t1 := f.now.Add(16 * time.Minute)
	f.now = t1
	f.fire("Stop", inSession(map[string]any{"background_tasks": shells("bl")}))
	if gone := f.state().Run.Gone; len(gone) != 1 || !gone["bp"].Equal(t1) {
		t.Fatalf("gone %v", gone)
	}
	sus := state.Suspects(f.state(), nil, state.TaskQuiet, t1)
	if len(sus) != 1 || !sus[0].Has(state.SignalWaitsOnEnded) {
		t.Fatalf("suspects %+v", sus)
	}
}

// Where the session's tasks write their output: beside its scratchpad, or, where a hook names none, under
// Claude Code's temporary root, as spike 45 found it.
func TestTasksDir(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	tp := "/Users/me/.claude/projects/-Users-me-app/sess-1.jsonl"
	uid := "claude-" + strconv.Itoa(os.Getuid())
	for _, c := range []struct {
		name string
		in   map[string]any
		vars map[string]string
		want string
	}{
		{"beside the scratchpad", map[string]any{"scratchpad_dir": scratch, "transcript_path": tp, "session_id": "sess-1"}, nil, tasksOf},
		{"under /tmp", map[string]any{"transcript_path": tp, "session_id": "sess-1"}, nil, filepath.Join("/tmp", uid, "-Users-me-app", "sess-1", "tasks")},
		{"under CLAUDE_CODE_TMPDIR", map[string]any{"transcript_path": tp, "session_id": "sess-1"}, map[string]string{"CLAUDE_CODE_TMPDIR": "/x"},
			filepath.Join("/x", uid, "-Users-me-app", "sess-1", "tasks")},
		{"nothing to go on", map[string]any{}, nil, ""},
	} {
		if got := tasksDir(Context{Input: c.in, Env: env(c.vars)}); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
