package loop

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// waiting makes the agent on phase idle, waiting on its job: its turn is over, and the job runs. It
// returns the job's id.
func (x *crew) waiting(phase string) string {
	var id string
	x.sub(phase).Update(func(st *state.State) error {
		q, _, _ := state.QueueJob(st, state.Job{Phase: phase, Kind: state.JobVerify, Command: "make test", Commit: "c1"}, x.now)
		id = q.ID
		j := st.Job(id)
		j.Status, j.Started, j.PID = state.JobRunning, x.now, 4242
		st.Run.TurnOpen, st.Run.LastStop = false, x.now
		return nil
	})
	return id
}

// passed ends job id of phase's sub-run: it passed.
func (x *crew) passed(phase, id string) {
	x.sub(phase).Update(func(st *state.State) error {
		j := st.Job(id)
		j.Status, j.Ended = state.JobPassed, x.now
		return nil
	})
}

// red ends job id of phase's sub-run: it failed.
func (x *crew) red(phase, id string) {
	x.sub(phase).Update(func(st *state.State) error {
		j := st.Job(id)
		j.Status, j.Exit, j.Ended = state.JobFailed, 1, x.now
		return nil
	})
}

// done reports phase done in its sub-run, as its agent's `baton done` does while its gate runs: the phase
// verifies on it.
func (x *crew) done(phase string) {
	x.t.Helper()
	sub := x.sub(phase)
	pl, err := sub.LoadPlan()
	if err != nil {
		x.t.Fatal(err)
	}
	if _, err := sub.Update(func(st *state.State) error { _, err := state.Done(st, pl, phase, x.now, state.DoneOpts{}); return err }); err != nil {
		x.t.Fatal(err)
	}
	if st, _ := sub.Load(); st.Phases[phase].Status != state.PhaseVerifying {
		x.t.Fatalf("%s %+v", phase, st.Phases[phase])
	}
}

// wait ticks for d, a minute at a time at most: a longer gap between ticks is the machine sleeping, and
// every deadline starts over from the wake.
func (x *crew) wait(d time.Duration) {
	for d > 0 {
		step := min(d, time.Minute)
		x.tick(step)
		d -= step
	}
}

// subLogged reports an event of kind in phase's sub-run's log with every one of fields.
func (x *crew) subLogged(phase, kind string, fields map[string]any) bool {
	for _, line := range strings.Split(x.subEvents(phase), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil || e["kind"] != kind {
			continue
		}
		match := true
		for k, v := range fields {
			if fmt.Sprint(e[k]) != fmt.Sprint(v) {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

// An idle phase agent's result is not lost on it: the agent's loop sends it a message saying the result
// has come, which starts it again, and it is given the result as it ends that turn (Claude Code gives a
// resumed agent nothing of its SubagentStart), once. Nothing is sent again: the result was given.
func TestAnIdleAgentIsWokenForItsResult(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	id := x.waiting("P1")
	x.tick(5 * time.Second)
	if len(x.sent) != 0 {
		t.Fatalf("a message while the job ran: %+v", x.sent)
	}
	x.passed("P1", id)
	x.tick(5 * time.Second)
	a := x.agent("P1")
	if len(x.sent) != 1 || x.sent[0].agent != a.id || !strings.Contains(x.sent[0].text, "[baton] Job "+id+" (P1) finished") {
		t.Fatalf("sent %+v", x.sent)
	}
	if len(x.resumes) != 1 || !strings.Contains(x.sent[0].text, "End this turn, and baton gives you its result then") {
		t.Fatalf("the agent started again %d times, with %q", len(x.resumes), x.sent[0].text)
	}
	if got := x.endTurn("P1"); !strings.Contains(got, id) {
		t.Fatalf("its stop gave it %q", got)
	}
	st, _ := x.sub("P1").Load()
	if st.Job(id).Delivered.IsZero() || !x.subLogged("P1", "results_delivered", map[string]any{"via": "stop"}) {
		t.Errorf("the result was not given: %+v\n%s", st.Job(id), x.subEvents("P1"))
	}
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastStop = false, x.now; return nil })
	for range 4 {
		x.tick(30 * time.Second)
	}
	if len(x.sent) != 1 {
		t.Errorf("sent again: %+v", x.sent)
	}
}

// A phase agent's gate that fails after its `baton done` brings the agent back to its phase: it is sent
// one message, and given the result as it ends that turn, once. Nothing compacts a phase agent, so its
// sub-run owes no boundary, and the human is told in the lead's session.
func TestAnAgentsRedGateAfterDoneBringsItBack(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	id := x.waiting("P1")
	x.done("P1")
	x.tick(5 * time.Second)
	x.red("P1", id)
	x.tick(5 * time.Second)
	a := x.agent("P1")
	if len(x.sent) != 1 || x.sent[0].agent != a.id || !strings.Contains(x.sent[0].text, "[baton] Job "+id+" (P1) finished") {
		t.Fatalf("sent %+v", x.sent)
	}
	st, _ := x.sub("P1").Load()
	if ps := st.Phases["P1"]; st.Current != "P1" || st.BoundaryOwed || st.Run.Compaction.InFlight() || ps.Status != state.PhaseActive || !st.InHand() {
		t.Fatalf("in hand %q, owed %v, compaction %+v, P1 %+v", st.Current, st.BoundaryOwed, st.Run.Compaction, ps)
	}
	if !x.subLogged("P1", "phase_resumed", map[string]any{"phase": "P1", "reason": "red", "by": "settle"}) || x.subLogged("P1", "compact_queued", nil) {
		t.Errorf("the sub-run's events:\n%s", x.subEvents("P1"))
	}
	if want := "→ its agent takes P1 up again"; !strings.Contains(x.announced(), want) {
		t.Errorf("told the lead %q", x.announced())
	}
	if got := x.endTurn("P1"); !strings.Contains(got, "Job "+id+" (P1) failed (exit 1)") {
		t.Fatalf("its stop gave it %q", got)
	}
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastStop = false, x.now; return nil })
	for range 4 {
		x.tick(30 * time.Second)
	}
	if len(x.sent) != 1 {
		t.Errorf("sent again: %+v", x.sent)
	}
}

// A fresh agent after a checkpoint is woken for its results as any other: the checkpoint it ended is over
// at the instant its start records, which is no compaction of baton's for its loop to wait out.
func TestAFreshAgentAfterACheckpointIsWokenForItsResult(t *testing.T) {
	for _, status := range []string{state.JobPassed, state.JobFailed} {
		t.Run(status, func(t *testing.T) {
			x := newCrew(t, 1)
			x.atWork("P1")
			first := x.agent("P1")
			x.sub("P1").Update(func(st *state.State) error { return state.SetCheckpoint(st, x.now) })
			x.stops("P1")
			for range 4 {
				x.tick(time.Second)
			}
			fresh := x.agent("P1")
			if fresh.id == first.id || x.status("P1") != state.WorkerWorking {
				t.Fatalf("no fresh agent: %+v", x.agents)
			}
			id := x.waiting("P1")
			x.tick(5 * time.Second)
			if status == state.JobPassed {
				x.passed("P1", id)
			} else {
				x.red("P1", id)
			}
			x.tick(5 * time.Second)
			if len(x.sent) != 1 || x.sent[0].agent != fresh.id || !strings.Contains(x.sent[0].text, "[baton] Job "+id+" (P1) finished") {
				t.Fatalf("sent %+v", x.sent)
			}
			if got := x.endTurn("P1"); !strings.Contains(got, id) {
				t.Errorf("its stop gave it %q", got)
			}
		})
	}
}

// A phase agent that ended before its red result could reach it (Claude Code cannot start it again) is
// replaced by a fresh agent in its worktree, whose brief carries the result and says the verification
// failed: given there once, and never again with a tool call or a stop.
func TestAFreshAgentIsBriefedWithTheRedResult(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	first := x.agent("P1")
	id := x.waiting("P1")
	x.done("P1")
	x.sendErr = "Agent " + first.id + " could not be resumed: No transcript found for agent ID"
	x.red("P1", id)
	x.tick(5 * time.Second)
	x.sendErr = ""
	for range 4 {
		x.tick(time.Second)
	}
	fresh := x.agent("P1")
	if fresh.id == first.id || x.status("P1") != state.WorkerWorking {
		t.Fatalf("no fresh agent: %+v; P1's worker %+v", x.agents, x.worker("P1"))
	}
	brief := x.briefs[fresh.id]
	for _, want := range []string{"Now: continue P1 — one. It was finished, but its verification failed", "The result of the job P1 was set aside for", "Job " + id + " (P1) failed (exit 1)"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, brief)
		}
	}
	st, _ := x.sub("P1").Load()
	if st.Job(id).Delivered.IsZero() || !x.subLogged("P1", "results_delivered", map[string]any{"via": "brief"}) || len(st.Due()) != 0 {
		t.Errorf("the result was not given in the brief: %+v, due %+v\n%s", st.Job(id), st.Due(), x.subEvents("P1"))
	}
	_, out := x.hook("PostToolUse", map[string]any{"agent_id": fresh.id, "agent_type": state.PhaseAgent, "cwd": fresh.tree, "tool_name": "Bash",
		"tool_use_id": "toolu_1", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
	if strings.Contains(out, id) {
		t.Errorf("given again with a tool call: %s", out)
	}
	if got := x.endTurn("P1"); strings.Contains(got, id) {
		t.Errorf("given again at its stop: %q", got)
	}
	if n := strings.Count(x.subEvents("P1"), `"kind":"results_delivered"`); n != 1 {
		t.Errorf("delivered %d times\n%s", n, x.subEvents("P1"))
	}
}

// A phase agent at work is sent nothing: its next tool call gives it the result, as the lead's does.
func TestAnAgentAtWorkTakesItsResultAtItsNextToolCall(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	id := x.waiting("P1")
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = true; return nil })
	x.passed("P1", id)
	for range 3 {
		x.tick(5 * time.Second)
	}
	if len(x.sent) != 0 {
		t.Fatalf("a message to an agent at work: %+v", x.sent)
	}
	a := x.agent("P1")
	_, out := x.hook("PostToolUse", map[string]any{"agent_id": a.id, "agent_type": state.PhaseAgent, "cwd": a.tree, "tool_name": "Bash",
		"tool_use_id": "toolu_1", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
	if !strings.Contains(out, id) || !x.subLogged("P1", "results_delivered", map[string]any{"via": "tool"}) {
		t.Errorf("the tool call gave %s\n%s", out, x.subEvents("P1"))
	}
}

// A message the mod has not acked when it is due again is taken back, and sent again: one is open at a
// time, and the agent gets it once.
func TestAMessageTheModDoesNotAckIsSentAgain(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	id := x.waiting("P1")
	x.modOff = true
	x.passed("P1", id)
	x.tick(5 * time.Second)
	sends := func() []bridge.Command {
		cmds, _ := x.bridge().Commands()
		var out []bridge.Command
		for _, c := range cmds {
			if c.Kind == bridge.Send {
				out = append(out, c)
			}
		}
		return out
	}
	first := sends()
	if len(first) != 1 {
		t.Fatalf("messages: %+v", first)
	}
	x.tick(30 * time.Second)
	if s := sends(); len(s) != 1 || s[0].ID != first[0].ID {
		t.Fatalf("sent again before its time: %+v", s)
	}
	x.tick(30 * time.Second)
	again := sends()
	if len(again) != 1 || again[0].ID == first[0].ID || !x.logged("mod_voided", map[string]any{"ids": []int{first[0].ID}}) {
		t.Fatalf("not taken back and sent again: %+v", again)
	}
	x.modOff = false
	x.tick(time.Second)
	if len(x.sent) != 1 || len(x.resumes) != 1 {
		t.Errorf("sent %+v; the agent started again %d times", x.sent, len(x.resumes))
	}
	if got := x.endTurn("P1"); !strings.Contains(got, id) {
		t.Errorf("its stop gave it %q", got)
	}
}

// A phase agent that stops with its phase unreported, and no job to wait on, is reminded by the lead's
// watchdog, with the lead's words, and once reminders do not move it, the human is told.
func TestAnIdleAgentWithItsPhaseUnreportedIsReminded(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	idle := func() {
		x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastStop = false, x.now; return nil })
	}
	idle()
	x.wait(DefaultTiming.IdleNudge - time.Minute)
	if len(x.sent) != 0 {
		t.Fatalf("reminded before its time: %+v", x.sent)
	}
	x.wait(2 * time.Minute)
	if len(x.sent) != 1 || !strings.Contains(x.sent[0].text, "P1 is not reported done") {
		t.Fatalf("sent %+v", x.sent)
	}
	for range 6 {
		idle()
		x.wait(DefaultTiming.IdleNudge)
	}
	st, _ := x.sub("P1").Load()
	if e := st.Run.Escalation; e == nil || e.Kind != "stalled" || len(x.sent) > MaxIdleNudges+1 {
		t.Errorf("escalation %+v after %d reminders", e, len(x.sent))
	}
}

// A permission prompt of a phase agent shows in the lead's terminal; when nobody has answered it for a
// while, the human is pushed, and told which phase waits.
func TestAnAgentsPromptIsPushedWithItsPhase(t *testing.T) {
	x := newCrew(t, 1)
	p := &pushes{}
	x.c.Lead.Notify = p
	x.atWork("P1")
	a := x.agent("P1")
	x.lead.Update(func(st *state.State) error {
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: a.id, Key: "k", Since: x.now})
		return nil
	})
	x.wait(DefaultTiming.DialogNotify + time.Second)
	if want := decide.AgentWaits("P1", "Bash"); !strings.Contains(strings.Join(p.details, "\n"), want) || !x.logged("dialog_waiting", map[string]any{"phase": "P1"}) {
		t.Errorf("pushed %q, want %q", p.details, want)
	}
}
