package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// tooLong is the error a turn ends in when its conversation outgrew the model's context, as Claude Code
// reports it (StopFailure's error, error_details and last_assistant_message).
func tooLong(at time.Time) *state.StopError {
	return &state.StopError{Error: "invalid_request", Details: "prompt is too long: 213456 tokens > 200000 maximum", Message: "Prompt is too long", At: at}
}

// A lead whose last turn failed because the API refused its conversation itself is started afresh: its
// loop ends its claude, for the host to start a fresh session from the brief, says why, and neither types
// into it nor escalates it.
func TestTheLeadStartsAfresh(t *testing.T) {
	r := newIdleRig(t)
	r.loop.StartsAfresh = true
	r.set(func(st *state.State) { st.Run.LastError = tooLong(r.now) })
	r.tick(10 * time.Second)
	if r.in.quit != 1 || len(r.in.text) != 0 {
		t.Fatalf("quit %d, typed %q", r.in.quit, r.in.text)
	}
	why, waited, ok := r.loop.Afresh()
	if !ok || why != decide.FailedTurn(*tooLong(r.now)) || waited != 0 {
		t.Fatalf("afresh: %q %s %v", why, waited, ok)
	}
	if e := r.state().Run.Escalation; e != nil {
		t.Fatalf("escalated: %+v", e)
	}
	ev := r.events()
	for _, want := range []string{`"kind":"afresh_due"`, `"kind":"lead_afresh"`, `"error":"invalid_request"`, `"waited":"0s"`} {
		if !strings.Contains(ev, want) {
			t.Fatalf("no %s in the events:\n%s", want, ev)
		}
	}
	r.run(time.Minute)
	if r.in.quit != 1 || len(r.in.text) != 0 {
		t.Fatalf("after: quit %d, typed %q", r.in.quit, r.in.text)
	}
}

// Past the cap, the lead waits before it starts afresh, and the human is told when, once.
func TestTheLeadStartsAfreshAfterAWait(t *testing.T) {
	r := newIdleRig(t)
	r.loop.StartsAfresh = true
	r.set(func(st *state.State) {
		st.Run.Recoveries = []state.Recovery{{At: r.now.Add(-20 * time.Minute), Fresh: true}, {At: r.now.Add(-5 * time.Minute), Fresh: true}}
		st.Run.LastError = tooLong(r.now)
	})
	r.run(4*time.Minute + 50*time.Second)
	if r.in.quit != 0 {
		t.Fatal("started afresh before its wait")
	}
	if n := slices.Index(r.push.kinds, "afresh_later"); n < 0 || strings.Count(strings.Join(r.push.kinds, " "), "afresh_later") != 1 ||
		!strings.HasPrefix(r.push.details[n], "P1: the last turn failed with an API error (invalid_request: prompt is too long") ||
		!strings.Contains(r.push.details[n], "so it starts the next at") {
		t.Fatalf("pushed %v %q", r.push.kinds, r.push.details)
	}
	r.run(20 * time.Second)
	if _, waited, ok := r.loop.Afresh(); r.in.quit != 1 || !ok || waited != 5*time.Minute {
		t.Fatalf("quit %d, waited %s", r.in.quit, waited)
	}
}

// Nothing on screen or in the box is lost to a fresh session: a dialog and the human typing hold it, and
// background agents of the model's own for a while; a draft is saved where `baton drafts` hands it back.
func TestTheLeadStartsAfreshWhenNothingIsLost(t *testing.T) {
	for _, c := range []struct {
		name  string
		on    func(r *rig)
		off   func(r *rig)
		after time.Duration // it starts afresh anyway, this long after (0: never while on)
	}{
		{name: "a dialog open", on: func(r *rig) {
			r.set(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Since: r.now}) })
		},
			off: func(r *rig) { r.set(func(st *state.State) { st.Run.Dialogs.Clear() }) }},
		{name: "the human typing", on: func(r *rig) { r.view.LastHumanKey = r.now.Add(time.Hour) }, off: func(r *rig) { r.view.LastHumanKey = r.now.Add(-time.Hour) }},
		{name: "typing a draft", on: func(r *rig) {
			r.view.Draft, r.view.DraftText, r.view.LastHumanKey = true, "half a", r.now.Add(time.Hour)
		},
			off: func(r *rig) { r.view.LastHumanKey = r.now.Add(-time.Hour) }},
		{name: "a background agent of the model's", on: func(r *rig) {
			r.set(func(st *state.State) { st.Run.Background = []state.Task{{ID: "t1", Type: "subagent", Since: r.now}} })
		}, after: DefaultTiming.BackgroundMax},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newIdleRig(t)
			r.loop.StartsAfresh = true
			c.on(r)
			r.set(func(st *state.State) { st.Run.LastError = tooLong(r.now) })
			r.run(5 * time.Minute)
			if r.in.quit != 0 {
				t.Fatal("started afresh over it")
			}
			if c.after > 0 {
				r.run(c.after)
			} else {
				c.off(r)
				r.tick(10 * time.Second)
			}
			if r.in.quit != 1 {
				t.Fatal("never started afresh")
			}
		})
	}

	r := newIdleRig(t)
	r.loop.StartsAfresh = true
	r.view.Draft, r.view.DraftText = true, "a thought, half typed"
	r.set(func(st *state.State) { st.Run.LastError = tooLong(r.now) })
	r.tick(10 * time.Second)
	drafts, err := r.loop.Store.Drafts()
	if r.in.quit != 1 || err != nil || len(drafts) != 1 || drafts[0].Text != "a thought, half typed" || !strings.Contains(r.events(), `"kind":"draft_rescued"`) {
		t.Fatalf("quit %d; drafts %+v %v", r.in.quit, drafts, err)
	}
}

// Where the host cannot start the lead afresh (no exec), the error is the human's, as before; and a turn
// that started after it (the human wrote) supersedes it.
func TestALeadThatCannotStartAfresh(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) { st.Run.LastError = tooLong(r.now) })
	r.tick(10 * time.Second)
	if e := r.state().Run.Escalation; r.in.quit != 0 || e == nil || e.Kind != "api_error" || !strings.Contains(e.Reason, "prompt is too long") {
		t.Fatalf("quit %d, escalation %+v", r.in.quit, e)
	}

	r = newIdleRig(t)
	r.loop.StartsAfresh = true
	r.set(func(st *state.State) {
		st.Run.LastError = tooLong(r.now.Add(-time.Minute))
		st.Run.TurnStarted, st.Run.TurnOpen, st.Run.LastActivity = r.now, true, r.now
	})
	r.run(time.Minute)
	if r.in.quit != 0 {
		t.Fatal("started afresh over a turn the human started")
	}
}

// A phase agent whose last turn failed because the API refused its conversation itself is replaced by a
// fresh agent, in its worktree, with the recovery recorded in its sub-run; nothing is sent to the agent
// before it, and nothing is escalated. Past the cap, the next waits, and the human is told when.
func TestAnAgentStartsAfresh(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	first := x.agent("P1")
	failed := func() *state.StopError {
		e := tooLong(x.now)
		x.sub("P1").Update(func(st *state.State) error {
			st.Run.TurnOpen, st.Run.LastStop, st.Run.LastError = false, x.now, e
			return nil
		})
		return e
	}
	e := failed()
	for range 4 {
		x.tick(time.Second)
	}
	fresh := x.agent("P1")
	if fresh.id == first.id || fresh.tree != first.tree || x.status("P1") != state.WorkerWorking || !slices.Contains(x.stopped, first.id) {
		t.Fatalf("agents %+v, stopped %v, %s", x.agents, x.stopped, x.status("P1"))
	}
	if slices.ContainsFunc(x.sent, func(s sent) bool { return s.agent == first.id }) {
		t.Errorf("sent to the agent before it: %+v", x.sent)
	}
	why := decide.AgentFailedTurn(decide.FailedTurn(*e))
	if !x.logged("worker_resumed", map[string]any{"phase": "P1", "why": why}) || !x.subLogged("P1", "agent_afresh", map[string]any{"agent": first.id, "error": "invalid_request", "waited": "0s"}) {
		t.Fatalf("events:\n%s\n%s", x.events(), x.subEvents("P1"))
	}
	sub, _ := x.sub("P1").Load()
	if len(sub.Run.Recoveries) != 1 || !sub.Run.Recoveries[0].Fresh || sub.Run.Escalation != nil {
		t.Fatalf("its sub-run: %+v, %+v", sub.Run.Recoveries, sub.Run.Escalation)
	}

	// Twice in the window: the next waits.
	x.sub("P1").Update(func(st *state.State) error {
		st.Run.Recoveries = append([]state.Recovery{{At: x.now.Add(-10 * time.Minute), Fresh: true}}, st.Run.Recoveries...)
		return nil
	})
	x.tick(time.Second)
	failed()
	x.wait(4 * time.Minute)
	if a := x.agent("P1"); a.id != fresh.id || !x.subLogged("P1", "notified", map[string]any{"notice": "afresh_later"}) {
		t.Fatalf("started afresh before its wait: %+v; events:\n%s", a, x.subEvents("P1"))
	}
	x.wait(2 * time.Minute)
	for range 3 {
		x.tick(time.Second)
	}
	if a := x.agent("P1"); a.id == fresh.id || x.status("P1") != state.WorkerWorking ||
		!x.subLogged("P1", "agent_afresh", map[string]any{"agent": fresh.id, "waited": "5m0s"}) {
		t.Fatalf("not started afresh after its wait: %+v, %s; events:\n%s", a, x.status("P1"), x.subEvents("P1"))
	}
}

// An agent's error that a fresh agent does not avoid stays the human's, as before.
func TestAnAgentsOtherErrorsStayTheHumans(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	first := x.agent("P1")
	x.sub("P1").Update(func(st *state.State) error {
		st.Run.TurnOpen, st.Run.LastStop = false, x.now
		st.Run.LastError = &state.StopError{Error: "invalid_request", Details: "bad request", At: x.now}
		return nil
	})
	x.wait(3 * time.Minute)
	sub, _ := x.sub("P1").Load()
	if a := x.agent("P1"); a.id != first.id || sub.Run.Escalation == nil || sub.Run.Escalation.Kind != "api_error" {
		t.Fatalf("agent %+v, escalation %+v", a, sub.Run.Escalation)
	}
}
