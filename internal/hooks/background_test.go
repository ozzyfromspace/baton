package hooks

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// bgBash is the PostToolUse of a Bash call that left its command running in the background as task id:
// run there, or moved there at its timeout (timedOut).
func bgBash(cmd, id string, timedOut bool) map[string]any {
	in := bash(cmd)
	resp := map[string]any{"stdout": "", "stderr": "", "interrupted": false, "backgroundTaskId": id}
	if timedOut {
		resp["timedOutAfterMs"] = 120000
	} else {
		in["tool_input"].(map[string]any)["run_in_background"] = true
	}
	in["tool_response"] = resp
	return in
}

// addedContext is what a hook's output adds to the model's context.
func addedContext(out map[string]any) string {
	h, _ := out["hookSpecificOutput"].(map[string]any)
	s, _ := h["additionalContext"].(string)
	return s
}

// shells is a Stop's background_tasks: a running shell for each id.
func shells(ids ...string) []any {
	var out []any
	for _, id := range ids {
		out = append(out, map[string]any{"id": id, "type": "shell", "status": "running", "description": "task " + id})
	}
	return out
}

// Background work a tool call starts is told to the model with that call's result, as context: a command
// run in the background or moved there, a Monitor, an agent launched in the background. A call that
// started none, a subagent's, and one while baton is paused tell nothing.
func TestBackgroundWorkIsToldAtItsStart(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     map[string]any
		paused bool
		task   string // the task told, "" for none
		want   []string
	}{
		{name: "a command run in the background", in: bgBash("npm test", "b1", false), task: "b1",
			want: []string{"[baton] Your background work runs (run npm test). Keep working on what does not depend on it: Claude Code wakes you when it ends.",
				"`baton done`", "if nothing is left to do until the work ends, end your turn."}},
		{name: "a command moved to the background at its timeout", in: bgBash("go test ./...", "b2", true), task: "b2",
			want: []string{"Your background work runs (run go test ./...)"}},
		{name: "a Monitor", task: "m1", in: map[string]any{"tool_name": "Monitor", "tool_input": map[string]any{"command": "tail -F app.log", "description": "watch the log"},
			"tool_response": map[string]any{"taskId": "m1", "timeoutMs": 1800000, "persistent": false}},
			want: []string{"[baton] Your monitor runs (watch the log)", "Claude Code wakes you with what it reports", "until it reports, end your turn."}},
		{name: "an agent launched in the background", task: "a1", in: map[string]any{"tool_name": "Agent",
			"tool_input":    map[string]any{"description": "explore the parser", "prompt": "Find …", "run_in_background": true},
			"tool_response": map[string]any{"isAsync": true, "status": "async_launched", "agentId": "a1", "description": "explore the parser"}},
			want: []string{"Your background work runs (explore the parser)"}},
		{name: "a long command, named on one line", task: "b3", in: map[string]any{"tool_name": "Bash",
			"tool_input":    map[string]any{"command": "for i in 1 2 3\ndo make check-" + strings.Repeat("x", 100) + "\ndone", "run_in_background": true},
			"tool_response": map[string]any{"backgroundTaskId": "b3"}},
			want: []string{"Your background work runs (for i in 1 2 3 do make check-xxx", "x…)."}},
		{name: "a command in the foreground", in: map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
			"tool_response": map[string]any{"stdout": "a.go", "stderr": "", "interrupted": false}}},
		{name: "an agent in the foreground", in: map[string]any{"tool_name": "Agent", "tool_input": map[string]any{"description": "explore"},
			"tool_response": map[string]any{"status": "completed", "agentId": "a2"}}},
		{name: "a result that is no object", in: map[string]any{"tool_name": "mcp__docs__read", "tool_response": []any{map[string]any{"type": "text", "text": "backgroundTaskId"}}}},
		{name: "a subagent's command", in: byAgent(bgBash("npm test", "b4", false), "sub-1")},
		{name: "while baton is paused", in: bgBash("npm test", "b5", false), paused: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			if c.paused {
				f.set(func(st *state.State) { st.Mode = state.ModePaused })
			}
			out := f.fire("PostToolUse", c.in)
			told := f.state().Run.BackgroundTold
			if c.task == "" {
				if out != nil || len(told) != 0 || len(f.eventsOf("background_told")) != 0 {
					t.Fatalf("told: %v, told %v, events %s", out, told, f.eventLog())
				}
				return
			}
			got := addedContext(out)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("the reminder lacks %q:\n%s", w, got)
				}
			}
			if feedback(out) != "" || say(out) != "" {
				t.Errorf("more than context: %v", out)
			}
			if !slices.Equal(told, []string{c.task}) {
				t.Errorf("told %v", told)
			}
			if !f.logged("background_told", map[string]any{"task": c.task, "tool": c.in["tool_name"], "via": "start", "phase": "P0"}) {
				t.Errorf("events: %s", f.eventLog())
			}
			// Told once: the same result again (a hook fired twice) says nothing more.
			if out := f.fire("PostToolUse", c.in); out != nil || len(f.eventsOf("background_told")) != 1 {
				t.Errorf("told again: %v", out)
			}
		})
	}
}

// The stop on background work told at its start is allowed; work whose start baton did not see (a
// subagent of the model's started it) refuses the first stop on it, once, as before. Work that ended is
// forgotten.
func TestAStopOnBackgroundWorkToldAtItsStartIsAllowed(t *testing.T) {
	f := newFixture(t, true)
	f.fire("PostToolUse", bgBash("npm test", "b1", false))
	out := f.fire("Stop", map[string]any{"background_tasks": shells("b1")})
	if feedback(out) != "" || !strings.Contains(say(out), "P0 (First) waits on background work (task b1), which wakes Claude when it ends") {
		t.Fatalf("the stop after a told start: %v", out)
	}
	if !f.logged("idle_on_background", map[string]any{"phase": "P0", "tasks": float64(1)}) || len(f.eventsOf("stop_refused")) != 0 {
		t.Fatalf("events: %s", f.eventLog())
	}
	// A subagent's command, started where the model was not told of it: its first stop is refused once.
	f.fire("PostToolUse", byAgent(bgBash("npm run lint", "b2", false), "sub-1"))
	out = f.fire("Stop", map[string]any{"background_tasks": shells("b1", "b2")})
	if r := feedback(out); !strings.Contains(r, "You stopped while your background work runs (task b1, and 1 more)") {
		t.Fatalf("the stop on untold work: %v", out)
	}
	if !f.logged("stop_refused", map[string]any{"why": "background work", "tasks": float64(2)}) {
		t.Fatalf("events: %s", f.eventLog())
	}
	if out := f.fire("Stop", map[string]any{"background_tasks": shells("b1", "b2")}); feedback(out) != "" {
		t.Fatalf("refused twice: %v", out)
	}
	// Work that ended is forgotten as told.
	f.fire("Stop", map[string]any{"background_tasks": shells("b2")})
	if told := f.state().Run.BackgroundTold; !slices.Equal(told, []string{"b2"}) {
		t.Fatalf("told %v", told)
	}
}

// A phase agent's background work is told in its sub-run, in a phase agent's words, and its stop on that
// work goes through; the lead's run hears nothing of it.
func TestAPhaseAgentsBackgroundWorkIsToldAtItsStart(t *testing.T) {
	w, lead, _ := newWorker(t)
	out := w.fire("PostToolUse", bgBash("go test ./...", "b1", true))
	if got := addedContext(out); !strings.Contains(got, "Your background work runs (run go test ./...)") ||
		!strings.Contains(got, "`baton verify --notes \"<where the phase stands>\" -- <command>`") {
		t.Fatalf("told %v", out)
	}
	if told := w.state().Run.BackgroundTold; !slices.Equal(told, []string{"b1"}) || len(lead.state().Run.BackgroundTold) != 0 {
		t.Fatalf("told: agent %v, lead %v", told, lead.state().Run.BackgroundTold)
	}
	if !w.logged("background_told", map[string]any{"task": "b1", "via": "start", "phase": "P2"}) {
		t.Fatalf("events: %s", w.eventLog())
	}
	if out := w.fire("SubagentStop", map[string]any{"background_tasks": shells("b1")}); feedback(out) != "" {
		t.Fatalf("the agent's stop: %v", out)
	}
	if !w.logged("idle_on_background", map[string]any{"phase": "P2"}) {
		t.Fatalf("events: %s", w.eventLog())
	}
}

// Claude Code reports every background task of the session at each Stop, whoever started it. Work a phase
// agent was told of as it started is the agent's: it neither refuses the lead's stop nor keeps the lead
// waiting on it; and the lead's own work is not the agent's.
func TestAStopLeavesOutAnotherLoopsBackgroundWork(t *testing.T) {
	w, lead, _ := newWorker(t)
	w.fire("PostToolUse", bgBash("go test ./...", "b1", true))
	lead.fire("PostToolUse", bgBash("npm test", "b2", false))
	out := lead.fire("Stop", map[string]any{"background_tasks": shells("b1", "b2")})
	if feedback(out) != "" || !strings.Contains(say(out), "waits on background work (task b2)") {
		t.Fatalf("the lead's stop: %v", out)
	}
	if bg := lead.state().Run.Background; len(bg) != 1 || bg[0].ID != "b2" {
		t.Errorf("the lead's background work: %+v", bg)
	}
	// The agent's own started where baton did not see it is the agent's to be told of, not the lead's.
	out = w.fire("SubagentStop", map[string]any{"background_tasks": shells("b1", "b2", "b3")})
	if r := feedback(out); !strings.Contains(r, "(task b1, and 1 more)") {
		t.Fatalf("the agent's stop: %v", out)
	}
	if bg := w.state().Run.Background; len(bg) != 2 || bg[0].ID != "b1" || bg[1].ID != "b3" {
		t.Errorf("the agent's background work: %+v", bg)
	}
	if len(lead.eventsOf("stop_refused")) != 0 {
		t.Errorf("the lead's events: %s", lead.eventLog())
	}
}

// The record of background work told at its start keeps the command and the description the call gave,
// each on one line and clipped to 200 characters, and leaves out what the call did not give.
func TestBackgroundToldRecordsTheCommandAndDescription(t *testing.T) {
	long := "go test ./... -run '" + strings.Repeat("A", 300) + "'"
	for _, c := range []struct {
		name                 string
		in                   map[string]any
		command, description string // "" for a field the record leaves out
	}{
		{name: "a command run in the background", in: bgBash("npm test", "b1", false), command: "npm test", description: "run npm test"},
		{name: "a long command", in: bgBash(long, "b1", false),
			command: string([]rune(long)[:199]) + "…", description: string([]rune("run " + long)[:199]) + "…"},
		{name: "a command on several lines", in: map[string]any{"tool_name": "Bash",
			"tool_input":    map[string]any{"command": "cd web &&\n  pnpm test", "run_in_background": true},
			"tool_response": map[string]any{"backgroundTaskId": "b1"}}, command: "cd web && pnpm test"},
		{name: "a PowerShell command", in: map[string]any{"tool_name": "PowerShell",
			"tool_input":    map[string]any{"command": "Invoke-Pester", "description": "run the tests", "run_in_background": true},
			"tool_response": map[string]any{"backgroundTaskId": "b1"}}, command: "Invoke-Pester", description: "run the tests"},
		{name: "a Monitor", in: map[string]any{"tool_name": "Monitor", "tool_input": map[string]any{"command": "tail -F app.log", "description": "watch the log"},
			"tool_response": map[string]any{"taskId": "b1"}}, command: "tail -F app.log", description: "watch the log"},
		{name: "an agent launched in the background", in: map[string]any{"tool_name": "Agent",
			"tool_input":    map[string]any{"description": "explore the parser", "prompt": "Find …", "run_in_background": true},
			"tool_response": map[string]any{"status": "async_launched", "agentId": "b1"}}, description: "explore the parser"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.fire("PostToolUse", c.in)
			told := f.eventsOf("background_told")
			if len(told) != 1 {
				t.Fatalf("events: %s", f.eventLog())
			}
			e := told[0]
			if e["task"] != "b1" || e["tool"] != c.in["tool_name"] || e["via"] != "start" || e["phase"] != "P0" {
				t.Errorf("told %v", e)
			}
			for field, want := range map[string]string{"command": c.command, "description": c.description} {
				got, has := e[field]
				if want == "" && has || want != "" && got != want {
					t.Errorf("%s: %q, want %q", field, got, want)
				}
			}
		})
	}
}

// No hook reports a background task's end, so a Stop whose background_tasks no longer lists told work
// records its end, with how long it ran from when the model was told of it: work told at its start, and
// work told at a stop refused over it. Work still listed, and work another loop was told of, are not ended
// here; a phase agent's work ends in its sub-run.
func TestBackgroundWorkEndsWhenAStopNoLongerListsIt(t *testing.T) {
	f := newFixture(t, true)
	t0 := f.now
	f.fire("PostToolUse", bgBash("npm test", "b1", false))
	f.now = t0.Add(10 * time.Second)
	f.fire("PostToolUse", map[string]any{"tool_name": "Monitor", "tool_input": map[string]any{"command": "tail -F app.log", "description": "watch the log"},
		"tool_response": map[string]any{"taskId": "m1"}})
	f.now = t0.Add(time.Minute)
	f.fire("Stop", map[string]any{"background_tasks": shells("b1", "m1")})
	if ended := f.eventsOf("background_ended"); len(ended) != 0 {
		t.Fatalf("ended while listed: %v", ended)
	}
	f.now = t0.Add(5 * time.Minute)
	f.fire("Stop", map[string]any{"background_tasks": shells("m1", "s1")}) // s1: work baton did not see start, told now
	if ended := f.eventsOf("background_ended"); len(ended) != 1 || !f.logged("background_ended", map[string]any{"task": "b1", "seconds": float64(300), "phase": "P0"}) {
		t.Fatalf("events: %s", f.eventLog())
	}
	f.now = t0.Add(7 * time.Minute)
	f.fire("Stop", map[string]any{"background_tasks": []any{}})
	if ended := f.eventsOf("background_ended"); len(ended) != 3 ||
		!f.logged("background_ended", map[string]any{"task": "m1", "seconds": float64(410)}) ||
		!f.logged("background_ended", map[string]any{"task": "s1", "seconds": float64(120)}) {
		t.Fatalf("events: %s", f.eventLog())
	}
	if st := f.state(); len(st.Run.BackgroundTold) != 0 || st.Run.BackgroundToldAt != nil {
		t.Fatalf("still told: %v at %v", st.Run.BackgroundTold, st.Run.BackgroundToldAt)
	}

	w, lead, _ := newWorker(t)
	w.fire("PostToolUse", bgBash("go test ./...", "b1", true))
	lead.fire("Stop", map[string]any{"background_tasks": []any{}})
	if len(lead.eventsOf("background_ended")) != 0 {
		t.Fatalf("the lead ended the agent's work: %s", lead.eventLog())
	}
	w.now = w.now.Add(90 * time.Second)
	w.fire("SubagentStop", map[string]any{"background_tasks": []any{}})
	if !w.logged("background_ended", map[string]any{"task": "b1", "seconds": float64(90), "phase": "P2"}) {
		t.Fatalf("the agent's events: %s", w.eventLog())
	}
}

// Telling background work at its start fails open: a panic in it exits 0 and writes nothing.
func TestTellingBackgroundWorkFailsOpen(t *testing.T) {
	failsOpen(t, []failCase{
		{"the lead's background start", "PostToolUse", func(string) map[string]any { return bgBash("npm test", "b1", false) }, nil},
		{"a phase agent's background start", "PostToolUse", func(tree string) map[string]any { return phaseAgent(bgBash("npm test", "b1", true), tree) }, nil},
	})
}
