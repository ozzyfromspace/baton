package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// withAgent gives f's run a phase agent on P1: its sub-run, in a worktree of its own, linked to the agent
// id "agent-p1" (state.Project.LinkAgent). It returns the sub-run and the worktree.
func (f *fixture) withAgent() (*state.Store, string) {
	f.t.Helper()
	tree := f.t.TempDir()
	os.MkdirAll(filepath.Join(tree, "pkg"), 0o755)
	p, err := state.OpenProject(f.dir, f.clock)
	if err != nil {
		f.t.Fatal(err)
	}
	sub, err := p.NewWorker(f.store.Ref, "P1", f.inst, tree)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := p.LinkAgent(f.store.Ref, "P1", "agent-p1"); err != nil {
		f.t.Fatal(err)
	}
	return sub, tree
}

// phaseAgent is a hook's input as the phase agent on P1 fires it, in its worktree.
func phaseAgent(in map[string]any, tree string) map[string]any {
	in["agent_id"], in["agent_type"], in["cwd"] = "agent-p1", state.PhaseAgent, filepath.Join(tree, "pkg")
	return in
}

func loaded(t *testing.T, s *state.Store) state.State {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Every hook knows its loop before anything else: a phase agent's hooks are its sub-run's, a subagent the
// lead's model started is the lead's, one the phase agent's model started (an id baton did not link, with
// a type, working in the agent's worktree) is the phase's, and Claude Code's own fork of a phase agent (the
// same, with no type) is nobody's. Agents are found by id, so a /clear, which gives
// their hooks the next session's id, changes nothing. Only the session's own hooks say what the session is.
func TestEveryHookKnowsItsLoop(t *testing.T) {
	for _, c := range []struct {
		name           string
		agent, typ     string
		session        string // "" for the lead's
		inTree         bool   // its working directory is in the phase agent's worktree
		lead, phase    bool   // whose state its hooks change
		subagents      [2]int // the lead's and the phase's counts of their models' subagents after SubagentStart
		sessionWritten bool   // it says what the lead's session is (its permission mode)
	}{
		{"the lead's own", "", "", "", false, true, false, [2]int{}, true},
		{"a phase agent", "agent-p1", state.PhaseAgent, "", true, false, true, [2]int{}, false},
		{"a phase agent after /clear", "agent-p1", state.PhaseAgent, "sess-cleared", true, false, true, [2]int{}, false},
		{"a subagent of the lead's model", "sub-1", "Explore", "", false, true, false, [2]int{1, 0}, false},
		{"a subagent of the phase agent's model", "sub-2", "Explore", "", true, false, true, [2]int{0, 1}, false},
		{"Claude Code's fork of a phase agent", "fork-1", "", "", true, false, false, [2]int{}, false},
		{"a fork after /clear", "fork-2", "", "sess-cleared", true, false, false, [2]int{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			sub, tree := f.withAgent()
			cwd := filepath.Dir(f.dir)
			if c.inTree {
				cwd = filepath.Join(tree, "pkg")
			}
			before, subBefore := f.state(), loaded(t, sub)
			f.now = f.now.Add(time.Minute)
			in := func(m map[string]any) map[string]any {
				m["cwd"], m["permission_mode"] = cwd, "acceptEdits"
				if c.session != "" {
					m["session_id"] = c.session
				}
				if c.agent != "" {
					m["agent_id"], m["agent_type"] = c.agent, c.typ
				}
				return m
			}
			if c.agent != "" {
				f.fire("SubagentStart", in(map[string]any{}))
			}
			if out := f.fire("PreToolUse", in(bash("make"))); out != nil {
				t.Errorf("PreToolUse said %v", out)
			}
			f.fire("PostToolUse", in(bash("make")))
			after, subAfter := f.state(), loaded(t, sub)
			if touched := !after.Run.LastActivity.Equal(before.Run.LastActivity); touched != c.lead {
				t.Errorf("the lead's run touched %v, want %v", touched, c.lead)
			}
			if touched := !subAfter.Run.LastActivity.Equal(subBefore.Run.LastActivity); touched != c.phase {
				t.Errorf("the phase's sub-run touched %v, want %v", touched, c.phase)
			}
			if got := [2]int{after.Run.Subagents, subAfter.Run.Subagents}; got != c.subagents {
				t.Errorf("subagents (lead, phase): %v, want %v", got, c.subagents)
			}
			if written := after.Run.PermissionMode == "acceptEdits"; written != c.sessionWritten {
				t.Errorf("the lead's permission mode %q", after.Run.PermissionMode)
			}
		})
	}
}

// A phase agent's permission prompt is drawn on the lead's screen, so it is recorded there, under the
// agent's id and its phase (where baton's mod runs, it holds only that agent), until the call's result,
// the agent's stop or its failure says it is gone.
func TestAPhaseAgentsDialogsAreOnTheLeadsScreen(t *testing.T) {
	for _, c := range []struct {
		name, event string
		input       map[string]any
	}{
		{"its result", "PostToolUse", bash("make")},
		{"the call failed", "PostToolUseFailure", bash("make")},
		{"the call was denied", "PermissionDenied", bash("make")},
		{"its stop", "SubagentStop", map[string]any{}},
		{"its failure", "StopFailure", map[string]any{"error": "rate_limit"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			sub, tree := f.withAgent()
			f.fire("PermissionRequest", phaseAgent(bash("make"), tree))
			if got := openDialogs(f); !equal(got, []string{"agent-p1/Bash"}) {
				t.Fatalf("on the lead's screen: %v", got)
			}
			if d, _ := f.state().Run.Dialogs.Front(); d.Phase != "P1" {
				t.Errorf("recorded as %+v", d)
			}
			if !f.logged("dialog_open", map[string]any{"tool": "Bash", "agent": "agent-p1", "phase": "P1"}) {
				t.Errorf("events: %v", f.events())
			}
			f.fire(c.event, phaseAgent(c.input, tree))
			if got := openDialogs(f); len(got) != 0 {
				t.Errorf("still open: %v", got)
			}
			if st := f.state(); st.Run.LastError != nil || len(loaded(t, sub).Run.Dialogs) != 0 {
				t.Errorf("the lead's error %+v, the sub-run's dialogs %v", st.Run.LastError, loaded(t, sub).Run.Dialogs)
			}
		})
	}
}

// A subagent of the lead's model that fails ends with no SubagentStop: its failure ends it, and not the
// lead's turn.
func TestASubagentsFailureIsNotTheTurns(t *testing.T) {
	f := newFixture(t, true)
	f.fire("UserPromptSubmit", map[string]any{"prompt": "go"})
	f.fire("SubagentStart", byAgent(map[string]any{}, "sub-1"))
	f.fire("PermissionRequest", byAgent(bash("make"), "sub-1"))
	f.fire("StopFailure", byAgent(map[string]any{"error": "server_error"}, "sub-1"))
	st := f.state()
	if !st.Run.TurnOpen || st.Run.LastError != nil || st.Run.Subagents != 0 || len(st.Run.Dialogs) != 0 {
		t.Fatalf("turn %v error %+v subagents %d dialogs %v", st.Run.TurnOpen, st.Run.LastError, st.Run.Subagents, st.Run.Dialogs)
	}
	if !f.logged("stop_failure", map[string]any{"error": "server_error", "agent": "sub-1"}) {
		t.Errorf("events: %v", f.events())
	}
}

// The calls baton's mod makes in the lead's session to spawn, message and stop phase agents are how the
// host acts: nothing holds them, even while the lead's own calls are held. The same tools called by the
// model are held as before.
func TestTheModsCallsAreNotHeld(t *testing.T) {
	f := newFixture(t, true)
	pl, _ := f.store.LoadPlan()
	f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, state.DoneOpts{}); return err })
	for _, tool := range []string{"Agent", "SendMessage", "TaskStop"} {
		call := map[string]any{"tool_name": tool, "tool_input": map[string]any{}, "tool_use_id": "toolu_plugin_01abc"}
		if out := f.fire("PreToolUse", call); out != nil {
			t.Errorf("the mod's %s was held: %v", tool, out)
		}
		call["tool_use_id"] = "toolu_01abc"
		if d := denyReason(f.fire("PreToolUse", call)); !strings.Contains(d, "End your turn now") {
			t.Errorf("the model's %s was not held: %q", tool, d)
		}
	}
	if d := denyReason(f.fire("PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "make"}, "tool_use_id": "toolu_plugin_01"})); d == "" {
		t.Error("a plugin's Bash call got through the hold")
	}
}

// baton's phase agents are not the lead's background work: a stop with only them running is a stop like
// any other (here, one without a status), and they are not counted as the lead's subagents.
func TestPhaseAgentsAreNotTheLeadsBackgroundWork(t *testing.T) {
	f := newFixture(t, true)
	agents := []any{
		map[string]any{"id": "a1", "type": "subagent", "status": "running", "description": "P1 — Second", "agent_type": state.PhaseAgent},
	}
	out := f.fire("Stop", map[string]any{"background_tasks": agents})
	if r := feedback(out); out["decision"] != "block" || strings.Contains(r, "background") {
		t.Fatalf("stop: %v", out)
	}
	st := f.state()
	if st.Run.Subagents != 0 || len(st.Run.BusyBackground()) != 0 || len(st.Run.PhaseAgents()) != 1 || st.Run.Background[0].AgentType != state.PhaseAgent {
		t.Fatalf("background %+v subagents %d", st.Run.Background, st.Run.Subagents)
	}
	// The model's own subagent is its background work, as before.
	agents = append(agents, map[string]any{"id": "s1", "type": "subagent", "status": "running", "description": "explore", "agent_type": "Explore"})
	if r := feedback(f.fire("Stop", map[string]any{"background_tasks": agents})); !strings.Contains(r, "explore") {
		t.Fatalf("the model's own subagent: %q", r)
	}
	if f.state().Run.Subagents != 1 {
		t.Errorf("subagents %d", f.state().Run.Subagents)
	}
}

// failCase is one path of a hook that must fail open: event, its input (given the phase agent's worktree),
// and what the run holds first.
type failCase struct {
	name  string
	event string
	in    func(tree string) map[string]any
	set   func(f *fixture)
}

// failsOpen fires each case at a run with a phase agent, through a store whose clock panics and through an
// open that panics: each exits 0 and writes nothing.
func failsOpen(t *testing.T, cases []failCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, how := range []string{"a store that panics", "an open that panics"} {
				f := newFixture(t, true)
				_, tree := f.withAgent()
				if c.set != nil {
					c.set(f)
				}
				open := func(ctx Context) (*state.Store, error) {
					if how == "an open that panics" {
						panic("boom")
					}
					s, err := OpenRun(f.clock)(ctx)
					if s != nil {
						s.Now = func() time.Time { panic("boom") }
					}
					return s, err
				}
				in := c.in(tree)
				in["hook_event_name"], in["session_id"] = c.event, f.sid
				raw, _ := json.Marshal(in)
				var out, errb bytes.Buffer
				code := Dispatch(c.event, bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(Deps{Open: open}))
				if code != 0 || out.Len() != 0 {
					t.Errorf("%s: exit %d, stdout %q, stderr %q", how, code, out.String(), errb.String())
				}
			}
		})
	}
}

// Every path of the routing fails open: a panic in it exits 0 and writes nothing, and a fork's hook does
// nothing at all.
func TestRoutingFailsOpen(t *testing.T) {
	failsOpen(t, []failCase{
		{"the mod's call", "PreToolUse", func(string) map[string]any {
			return map[string]any{"tool_name": "Agent", "tool_input": map[string]any{}, "tool_use_id": "toolu_plugin_1"}
		}, nil},
		{"a fork's call", "PreToolUse", func(tree string) map[string]any {
			return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "make"}, "agent_id": "fork-1", "cwd": filepath.Join(tree, "pkg")}
		}, nil},
		{"a phase agent's call", "PreToolUse", func(tree string) map[string]any { return phaseAgent(bash("make"), tree) }, nil},
		{"a phase agent's result", "PostToolUse", func(tree string) map[string]any { return phaseAgent(bash("make"), tree) }, nil},
		{"a phase agent's dialog", "PermissionRequest", func(tree string) map[string]any { return phaseAgent(bash("make"), tree) }, nil},
		{"a phase agent's start", "SubagentStart", func(tree string) map[string]any { return phaseAgent(map[string]any{}, tree) }, nil},
		{"a phase agent's stop", "SubagentStop", func(tree string) map[string]any { return phaseAgent(map[string]any{}, tree) }, nil},
		{"a phase agent's failure", "StopFailure", func(tree string) map[string]any { return phaseAgent(map[string]any{"error": "rate_limit"}, tree) }, nil},
		{"a phase agent's notification", "Notification", func(tree string) map[string]any {
			return phaseAgent(map[string]any{"notification_type": "idle_prompt"}, tree)
		}, nil},
		{"a subagent's failure", "StopFailure", func(string) map[string]any { return byAgent(map[string]any{"error": "server_error"}, "sub-1") }, nil},
	})
}
