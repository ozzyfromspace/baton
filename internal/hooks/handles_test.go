package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// handlesRun is newWorker's run with P2's agent at work, which baton's mod has: the lead is on P1, P0 is
// done, and agent-p2 works on P2.
func handlesRun(t *testing.T) (w, lead *fixture) {
	t.Helper()
	w, lead, _ = newWorker(t)
	lead.set(func(st *state.State) { st.WorkerOn("P2").Status = state.WorkerWorking })
	if err := bridge.Open(lead.store.Dir, lead.clock, nil).Track("agent-p2", "P2"); err != nil {
		t.Fatal(err)
	}
	return w, lead
}

// prompted is UserPromptSubmit's input for the human's prompt text.
func prompted(text string) map[string]any { return map[string]any{"prompt": text} }

// systemMessage is what a hook's output shows the human.
func systemMessageOf(out map[string]any) string {
	s, _ := out["systemMessage"].(string)
	return s
}

// The human's `@P2 <words>` at the lead's prompt, while P2's agent is at work, is queued for that agent and
// kept from the lead's model: the prompt is blocked (no turn starts, nothing reaches the model), and why
// says what baton did. The same words sent again, while still queued, are queued once. The phase's id is
// matched ignoring case, as Claude Code matches an agent's name.
func TestTheHumansWordsGoToAPhasesAgentAtWork(t *testing.T) {
	for _, c := range []struct{ name, prompt, words string }{
		{"as written", "@P2 please also write notes.md", "please also write notes.md"},
		{"in another case", "@p2 please also write notes.md", "please also write notes.md"},
		{"over lines", "@P2   first this,\nthen that\n", "first this,\nthen that"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead := handlesRun(t)
			out := lead.fire("UserPromptSubmit", prompted(c.prompt))
			if out["decision"] != "block" || out["reason"] != decide.SentToAgent("P2") {
				t.Fatalf("answered %v", out)
			}
			if out["hookSpecificOutput"] != nil || out["systemMessage"] != nil {
				t.Errorf("the model or the human was given more: %v", out)
			}
			st := lead.state()
			if len(st.ToAgents) != 1 {
				t.Fatalf("queued %+v", st.ToAgents)
			}
			q := st.ToAgents[0]
			if q.Phase != "P2" || q.Agent != "agent-p2" || q.Text != c.words || q.Prompt != c.prompt || !q.At.Equal(lead.now) {
				t.Errorf("queued %+v", q)
			}
			if st.Run.TurnOpen || st.Run.TurnBy != "" || !st.Run.HumanAt.IsZero() {
				t.Errorf("a turn of the lead's: %+v", st.Run)
			}
			if !lead.logged("human_to_agent", map[string]any{"phase": "P2", "agent": "agent-p2", "routed": "agent", "step": "queued"}) || len(lead.eventsOf("turn_started")) != 0 {
				t.Errorf("events:\n%s", lead.eventLog())
			}
			lead.now = lead.now.Add(time.Second)
			if out := lead.fire("UserPromptSubmit", prompted(c.prompt)); out["decision"] != "block" {
				t.Fatalf("sent again: %v", out)
			}
			if st := lead.state(); len(st.ToAgents) != 1 {
				t.Errorf("queued twice: %+v", st.ToAgents)
			}
			if !lead.logged("human_to_agent", map[string]any{"phase": "P2", "step": "queued", "again": true}) {
				t.Errorf("events:\n%s", lead.eventLog())
			}
		})
	}
}

// A phase no agent of baton's is at work on takes the human's `@<phase>` prompt through the lead: nothing is
// woken or queued, the prompt goes on to the lead's model with context saying where the phase stands,
// and the human is told why.
func TestTheHumansWordsForAPhaseWithNoAgentGoToTheLead(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   func(st *state.State)
		phase string
		to    string
	}{
		{"done", nil, "P0", state.ToDone},
		{"done, its agent's work landed", func(st *state.State) {
			st.Landed = append(st.Landed, state.Landing{Phase: "P0", Commits: []string{"c1"}, Head: "c1"})
		}, "P0", state.ToLanded},
		{"the lead's own", nil, "P1", state.ToLead},
		{"its agent stopped by the human", func(st *state.State) { st.WorkerOn("P2").Status = state.WorkerHeld }, "P2", state.ToStopped},
		{"its agent starting", func(st *state.State) { st.WorkerOn("P2").Status = state.WorkerStarting }, "P2", state.ToStarting},
		{"its agent being let go", func(st *state.State) { st.WorkerOn("P2").Retiring = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }, "P2", state.ToRetiring},
		{"its work waiting to land", func(st *state.State) { st.WorkerOn("P2").Status = state.WorkerLanding }, "P2", state.ToLanding},
		{"its agent none of the mod's", func(st *state.State) { st.WorkerOn("P2").AgentID = "agent-gone" }, "P2", state.ToUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead := handlesRun(t)
			if c.set != nil {
				lead.set(c.set)
			}
			out := lead.fire("UserPromptSubmit", prompted("@"+c.phase+" how is it going?"))
			if out["decision"] != nil {
				t.Fatalf("blocked: %v", out)
			}
			why := decide.NoAgent(c.phase, c.to)
			if got := additionalContext(out); !strings.HasSuffix(got, decide.WordsForLead(c.phase, why)) {
				t.Errorf("the model was given %q", got)
			}
			if got := systemMessageOf(out); !strings.Contains(got, decide.WordsToLead(c.phase, why)) {
				t.Errorf("the human was told %q", got)
			}
			st := lead.state()
			if len(st.ToAgents) != 0 || !st.Run.TurnOpen || st.Run.TurnBy != "human" {
				t.Errorf("queued %+v, turn open %v by %q", st.ToAgents, st.Run.TurnOpen, st.Run.TurnBy)
			}
			if !lead.logged("human_to_agent", map[string]any{"phase": c.phase, "routed": "lead", "stands": c.to, "why": why}) {
				t.Errorf("events:\n%s", lead.eventLog())
			}
		})
	}
}

// Only a prompt the human typed that starts with one of the run's phases' handles is routed: one that
// names another id, mentions a phase further on, names a phase with no words, or that baton or Claude
// Code submitted, is an ordinary prompt; so is any prompt of a run with no plan.
func TestOtherPromptsAreOrdinary(t *testing.T) {
	for _, c := range []struct {
		name string
		in   map[string]any
	}{
		{"an id none of the plan's", prompted("@P9 hurry up")},
		{"a mention mid-text", prompted("tell @P2 to hurry up")},
		{"a handle with no words", prompted("@P2")},
		{"a handle and punctuation", prompted("@P2, hurry up")},
		{"a file mention", prompted("@internal/plan/names.go what does this do?")},
		{"one Claude Code submitted", map[string]any{"prompt": "@P2 hurry up", "source": "agent"}},
		{"baton's own line", prompted(NudgePrefix + " @P2 is waiting")},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead := handlesRun(t)
			out := lead.fire("UserPromptSubmit", c.in)
			if out["decision"] != nil || strings.Contains(additionalContext(out), "@P2, which writes to") {
				t.Errorf("routed: %v", out)
			}
			if st := lead.state(); len(st.ToAgents) != 0 || !st.Run.TurnOpen {
				t.Errorf("queued %+v, turn open %v", st.ToAgents, st.Run.TurnOpen)
			}
			if n := len(lead.eventsOf("human_to_agent")); n != 0 {
				t.Errorf("logged %d:\n%s", n, lead.eventLog())
			}
		})
	}
	t.Run("no plan", func(t *testing.T) {
		f := newFixture(t, false)
		if out := f.fire("UserPromptSubmit", prompted("@P0 hello")); out["decision"] != nil || len(f.eventsOf("human_to_agent")) != 0 {
			t.Errorf("routed: %v", out)
		}
	})
}

// letGo records agent-p2, the phase agent of newWorker's P2, as one baton let go of.
func letGo(t *testing.T, lead *fixture) {
	t.Helper()
	p, err := state.OpenProject(lead.dir, lead.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.LetGo(state.WorkerRef(lead.store.Ref, "P2"), "agent-p2"); err != nil {
		t.Fatal(err)
	}
}

// A phase agent baton let go of, woken all the same (the human typed in its own view), is refused every
// tool call, with why; its stop and its other hooks are let through with nothing recorded, so it ends its
// turn having changed nothing. Its waking is logged once, in the run its phase was of. Another agent on
// the phase works as ever.
func TestAnAgentBatonLetGoIsRefusedEveryToolCall(t *testing.T) {
	w, lead := handlesRun(t)
	lead.set(func(st *state.State) {
		e := st.WorkerOn("P2")
		e.Status = state.WorkerLanded
		st.Phases["P2"].Status = state.PhaseDone
		st.Landed = append(st.Landed, state.Landing{Phase: "P2", Commits: []string{"c2"}, Head: "c2"})
	})
	letGo(t, lead)
	before := w.state()
	want := decide.LetGoRefused("P2", decide.LetGoWhy("P2", state.ToLanded))
	if !strings.Contains(want, "P2 is done, and its work landed on the run's branch") {
		t.Fatalf("why: %q", want)
	}
	for _, in := range []map[string]any{bash("git status"), edit("Write", "notes.md"), bash("baton done P2 --notes again")} {
		if why := denied(w, in); why != want {
			t.Errorf("%v: refused with %q", in["tool_input"], why)
		}
	}
	for _, event := range []string{"PostToolUse", "SubagentStart", "SubagentStop", "Notification", "PermissionRequest"} {
		in := bash("git status")
		if event == "SubagentStop" {
			in = map[string]any{"stop_hook_active": false}
		}
		if out := w.fire(event, in); len(out) != 0 {
			t.Errorf("%s answered %v", event, out)
		}
	}
	after := w.state()
	if after.Run.TurnOpen != before.Run.TurnOpen || after.Run.Briefed != before.Run.Briefed || !after.Run.LastActivity.Equal(before.Run.LastActivity) {
		t.Errorf("its sub-run changed: %+v", after.Run)
	}
	if n := len(lead.eventsOf("retired_agent_woke")); n != 1 || !lead.logged("retired_agent_woke", map[string]any{"agent": "agent-p2", "phase": "P2", "hook": "PreToolUse", "tool": "Bash"}) {
		t.Errorf("logged %d:\n%s", n, lead.eventLog())
	}

	// A fresh agent on P2, linked to its sub-run, is not the one let go.
	p, _ := state.OpenProject(lead.dir, lead.clock)
	if err := p.LinkAgent(lead.store.Ref, "P2", "agent-p2b"); err != nil {
		t.Fatal(err)
	}
	w.agent = "agent-p2b"
	if why := denied(w, map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "notes.md"}}); why != "" {
		t.Errorf("the fresh agent was refused: %q", why)
	}
}

// Why baton let an agent go follows where its phase stands now.
func TestWhyBatonLetAnAgentGo(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(st *state.State)
		why  string
	}{
		{"a fresh agent at work on it", func(st *state.State) { st.WorkerOn("P2").AgentID = "agent-p2b" }, "another agent of baton's took P2 up in your place"},
		{"taken back by the lead", func(st *state.State) {
			st.Workers = nil
			st.Current, st.Phases["P2"].Status = "P2", state.PhaseActive
		}, "the lead took P2 up"},
		{"done", func(st *state.State) {
			st.WorkerOn("P2").Status = state.WorkerLanded
			st.Phases["P2"].Status = state.PhaseDone
		}, "P2 is done"},
		{"back for later", func(st *state.State) {
			st.Workers = nil
			st.Phases["P2"].Status = state.PhasePending
		}, "baton took P2 back"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, lead := handlesRun(t)
			lead.set(c.set)
			letGo(t, lead)
			if why := denied(w, bash("ls")); !strings.Contains(why, "baton let you go from P2 ("+c.why+")") {
				t.Errorf("refused with %q", why)
			}
		})
	}
}

// Routing the human's words and refusing an agent baton let go fail open: a panic in either exits 0, and
// lets the prompt or the call through.
func TestHandlesFailOpen(t *testing.T) {
	letGoP1 := func(f *fixture) {
		p, _ := state.OpenProject(f.dir, f.clock)
		if err := p.LetGo(state.WorkerRef(f.store.Ref, "P1"), "agent-p1"); err != nil {
			f.t.Fatal(err)
		}
	}
	atWork := func(f *fixture) {
		f.set(func(st *state.State) {
			pl, _ := f.store.LoadPlan()
			st.Phases["P0"].Status, st.Current = state.PhaseDone, ""
			e := state.Worker{Phase: "P1", Ref: state.WorkerRef(f.store.Ref, "P1"), Worktree: f.t.TempDir(), Branch: "baton/x/P1", AgentID: "agent-p1"}
			if err := state.Assign(st, pl, e, f.now); err != nil {
				f.t.Fatal(err)
			}
			st.WorkerOn("P1").Status = state.WorkerWorking
		})
		bridge.Open(f.store.Dir, f.clock, nil).Track("agent-p1", "P1")
	}
	failsOpen(t, []failCase{
		{"the human's words for an agent", "UserPromptSubmit", func(string) map[string]any { return prompted("@P1 hurry up") }, atWork},
		{"the human's words for the lead", "UserPromptSubmit", func(string) map[string]any { return prompted("@P0 how did it go?") }, nil},
		{"a call of an agent let go", "PreToolUse", func(tree string) map[string]any { return phaseAgent(bash("ls"), tree) }, letGoP1},
		{"the stop of an agent let go", "SubagentStop", func(tree string) map[string]any { return phaseAgent(map[string]any{}, tree) }, letGoP1},
	})
}
