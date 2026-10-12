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
// has come, which starts it again, and its SubagentStart gives it the result, once. Nothing is sent
// again: the result was given.
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
	if len(x.resumes) != 1 || !strings.Contains(x.resumes[0], id) {
		t.Fatalf("the agent started again with %q", x.resumes)
	}
	st, _ := x.sub("P1").Load()
	if st.Job(id).Delivered.IsZero() || !x.subLogged("P1", "results_delivered", map[string]any{"via": "resume"}) {
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
	if len(x.sent) != 1 || len(x.resumes) != 1 || !strings.Contains(x.resumes[0], id) {
		t.Errorf("sent %+v; the agent was given %q", x.sent, x.resumes)
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
