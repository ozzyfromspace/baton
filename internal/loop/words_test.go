package loop

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// writes has the human write prompt at the lead's prompt, and returns what the lead's UserPromptSubmit
// answered: blocked, and why.
func (x *crew) writes(prompt string) (blocked bool, why string) {
	x.t.Helper()
	code, out := x.hook("UserPromptSubmit", map[string]any{"prompt": prompt, "transcript_path": x.leadTranscript()})
	if code != 0 {
		x.t.Fatalf("the prompt's hook exited %d", code)
	}
	var res struct{ Decision, Reason string }
	json.Unmarshal([]byte(out), &res)
	return res.Decision == "block", res.Reason
}

// busy opens the turn of the agent on phase: it is at work, and takes a message at its next tool round.
func (x *crew) busy(phase string) {
	x.sub(phase).Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.TurnStarted = true, x.now; return nil })
}

// toAgent lists the messages the mod sent to agent that carry the human's words.
func (x *crew) toAgent(agent string) []string {
	var out []string
	for _, s := range x.sent {
		if s.agent == agent && strings.Contains(s.text, "The human wrote to you from the lead's terminal") {
			out = append(out, s.text)
		}
	}
	return out
}

// The human's `@P1 <words>` at the lead's prompt, while P1's agent works, is kept from the lead's model and
// sent to the agent through baton's mod, as the human's own words, at once: Claude Code holds it for the
// agent's next tool round. The mod's ack takes it off the queue, and a second later the mod empties the
// lead's input box of the prompt, which Claude Code kept. Each step is logged.
func TestTheHumansWordsReachAnAgentAtWork(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	a := x.agent("P1")
	x.busy("P1")
	prompt := "@P1 also write notes.md, please"
	if blocked, why := x.writes(prompt); !blocked || why != decide.SentToAgent("P1") {
		t.Fatalf("blocked %v: %q", blocked, why)
	}
	if !x.logged("human_to_agent", map[string]any{"phase": "P1", "agent": a.id, "routed": "agent", "step": "queued"}) {
		t.Fatalf("not queued:\n%s", x.events())
	}
	x.tick(time.Second)
	if got := x.toAgent(a.id); len(got) != 1 || got[0] != decide.HumanToAgent("P1", "also write notes.md, please") {
		t.Fatalf("sent %+v", x.sent)
	}
	if len(x.resumes) != 0 {
		t.Errorf("an agent at work was started again: %v", x.resumes)
	}
	if !slices.Equal(x.emptied, []string{prompt}) {
		t.Errorf("the input box was emptied of %q", x.emptied)
	}
	x.tick(time.Second)
	if st := x.state(); len(st.ToAgents) != 0 || len(st.Inbox) != 0 {
		t.Errorf("queued %+v, the lead's mail %+v", st.ToAgents, st.Inbox)
	}
	for _, step := range []string{"sent", "acked"} {
		if !x.logged("human_to_agent", map[string]any{"phase": "P1", "agent": a.id, "step": step}) {
			t.Errorf("not %s:\n%s", step, x.events())
		}
	}
	if !x.logged("human_to_agent", map[string]any{"step": "acked", "ok": true}) {
		t.Errorf("events:\n%s", x.events())
	}
	for range 3 {
		x.tick(time.Second)
	}
	if got := x.toAgent(a.id); len(got) != 1 || len(x.emptied) != 1 {
		t.Errorf("sent again %d, emptied again %d", len(got), len(x.emptied))
	}
}

// An idle agent is woken by the human's words, as by any message, and its phase goes on as baton has it.
func TestTheHumansWordsWakeAnIdleAgent(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	a := x.agent("P1")
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastStop = false, x.now; return nil })
	if blocked, _ := x.writes("@p1 look at the flaky test first"); !blocked {
		t.Fatal("not sent")
	}
	x.tick(time.Second)
	if got := x.toAgent(a.id); len(got) != 1 || !strings.Contains(got[0], "look at the flaky test first") {
		t.Fatalf("sent %+v", x.sent)
	}
	if !slices.Contains(x.resumes, a.id) {
		t.Errorf("not woken: %v", x.resumes)
	}
	st, _ := x.sub("P1").Load()
	if st.Current != "P1" || st.Phases["P1"].Status != state.PhaseActive || x.status("P1") != state.WorkerWorking {
		t.Errorf("its phase: in hand %q, %+v, worker %s", st.Current, st.Phases["P1"], x.status("P1"))
	}
}

// Words the agent did not get after all go to the lead's model, and the human is told: Claude Code refused
// the message (the human stopped the agent meanwhile), or the agent ended before baton could send them.
func TestTheHumansWordsNoAgentTookGoToTheLead(t *testing.T) {
	stopped := `Agent "a" was stopped by the user and was not resumed. Treat its work as cancelled.`
	for _, c := range []struct {
		name string
		then func(x *crew)
		why  string
	}{
		{"refused", func(x *crew) { x.sendErr = stopped }, decide.WordsRefused(stopped)},
		{"its agent gone", func(x *crew) { x.c.unregister(x.c.workerOn("P1")) }, decide.WordsGone},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			x.atWork("P1")
			x.busy("P1")
			if blocked, _ := x.writes("@P1 skip the docs"); !blocked {
				t.Fatal("not sent")
			}
			c.then(x)
			x.tick(time.Second)
			x.tick(time.Second)
			st := x.state()
			say, tell := decide.WordsUnsent("P1", "skip the docs", c.why)
			if len(st.ToAgents) != 0 || len(st.Inbox) != 1 || st.Inbox[0].Kind != state.MessageWords || st.Inbox[0].Text != tell {
				t.Fatalf("queued %+v, the lead's mail %+v", st.ToAgents, st.Inbox)
			}
			if !x.logged("human_to_agent", map[string]any{"phase": "P1", "routed": "lead", "step": "unsent", "why": c.why}) {
				t.Errorf("events:\n%s", x.events())
			}
			if !slices.Contains(st.Run.Announce, say) && !x.logged("message_delivered", map[string]any{"message": state.MessageWords}) {
				t.Errorf("the human was not told: %q", st.Run.Announce)
			}
		})
	}
}

// calls fires a PreToolUse of agent a, in its worktree, and returns why it was refused ("" for a call let
// through).
func (x *crew) calls(a started) string {
	_, out := x.hook("PreToolUse", map[string]any{"agent_id": a.id, "agent_type": state.PhaseAgent, "cwd": a.tree, "tool_use_id": "toolu_w1",
		"tool_name": "Read", "tool_input": map[string]any{"file_path": "a.txt"}, "transcript_path": x.leadTranscript()})
	var res struct {
		HookSpecificOutput struct{ PermissionDecision, PermissionDecisionReason string } `json:"hookSpecificOutput"`
	}
	json.Unmarshal([]byte(out), &res)
	if res.HookSpecificOutput.PermissionDecision != "deny" {
		return ""
	}
	return res.HookSpecificOutput.PermissionDecisionReason
}

// Every agent baton lets go of for good is recorded so: one a fresh agent replaces (here, after its
// checkpoint), and one whose worker is retired. Woken all the same, it is refused every tool call, and its
// waking is logged once; the agent at work is not.
func TestTheAgentsBatonLetsGoAreRecorded(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	first := x.agent("P1")
	x.sub("P1").Update(func(st *state.State) error { return state.SetCheckpoint(st, x.now) })
	x.tick(time.Second)
	x.stops("P1")
	for range 4 {
		x.tick(time.Second)
	}
	fresh := x.agent("P1")
	ref := state.WorkerRef("sess-1", "P1")
	if fresh.id == first.id || x.proj.LetGoOf(first.id) != ref || x.proj.LetGoOf(fresh.id) != "" {
		t.Fatalf("agents %+v; let go of: first %q, fresh %q", x.agents, x.proj.LetGoOf(first.id), x.proj.LetGoOf(fresh.id))
	}
	want := decide.LetGoRefused("P1", decide.LetGoWhy("P1", state.ToAgent))
	if why := x.calls(first); why != want {
		t.Errorf("the first agent's call: %q", why)
	}
	x.calls(first)
	if why := x.calls(fresh); why != "" {
		t.Errorf("the fresh agent's call: %q", why)
	}
	if n := x.count("retired_agent_woke", map[string]any{"agent": first.id, "phase": "P1"}); n != 1 {
		t.Errorf("logged %d:\n%s", n, x.events())
	}

	w := x.worker("P1")
	x.c.Ended("the baton session ended", nil)
	x.proj.Unbind("inst")
	r := x.retirement(x.worker("P1"))
	r.Host, r.By, r.Why = true, "inst", "the run was dropped"
	if got := RetireWorker(r); got.Skipped != "" {
		t.Fatalf("not retired: %s", got.Skipped)
	}
	if x.proj.LetGoOf(w.AgentID) != ref {
		t.Errorf("the retired worker's agent %s is not recorded as let go", w.AgentID)
	}
}

// Words whose message the mod never acked are sent again once it is taken back (sendWait), once.
func TestTheHumansWordsUnackedAreSentAgain(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	a := x.agent("P1")
	x.busy("P1")
	x.writes("@P1 rename the flag")
	x.modOff = true
	x.tick(time.Second)
	sends := func() int {
		cmds, _ := x.bridge().Commands()
		n := 0
		for _, c := range cmds {
			if c.Kind == bridge.Send && c.Summary == decide.WordsSummary {
				n++
			}
		}
		return n
	}
	if sends() != 1 {
		t.Fatalf("issued %d", sends())
	}
	x.tick(30 * time.Second)
	if sends() != 1 || x.count("human_to_agent", map[string]any{"step": "sent"}) != 1 {
		t.Fatalf("sent again before sendWait:\n%s", x.events())
	}
	x.tick(31 * time.Second)
	if sends() != 1 || x.count("human_to_agent", map[string]any{"step": "sent"}) != 2 || !x.logged("mod_voided", nil) {
		t.Fatalf("not sent again in place of the first:\n%s", x.events())
	}
	x.modOff = false
	x.runMod()
	x.tick(time.Second)
	if got := x.toAgent(a.id); len(got) != 1 {
		t.Errorf("the agent got %d", len(got))
	}
	if st := x.state(); len(st.ToAgents) != 0 {
		t.Errorf("queued %+v", st.ToAgents)
	}
}
