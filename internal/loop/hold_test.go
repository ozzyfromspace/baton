package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// heldRig is an idle run whose proposal d1 the human holds, as the hooks leave it (holdProposal): resolved
// by (held: they chose Wait for me; declined: its question went away), behind an escalation of kind
// decision. untimed makes it one only the human could let through.
func heldRig(t *testing.T, by string, untimed bool) *rig {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		d := state.Decision{What: "commit P1 unsigned", Because: "gpg times out", Undo: "re-sign it", Asked: true}
		if untimed {
			d.What, d.Untimed = "push the release tag", decide.UntimedReason("push")
		} else {
			d.Deadline = decide.Deadline(r.now, DefaultTiming.EscalationTimeout)
		}
		p, err := state.AddProposal(st, d, r.now)
		if err != nil {
			t.Fatal(err)
		}
		answer := "Wait for me"
		if by == state.ByDeclined {
			answer = ""
		}
		state.Resolve(st, p.ID, by, answer, r.now)
		st.Run.Escalation = &state.Escalation{Kind: "decision", Reason: "proposal d1 waits for you: " + p.What, Since: r.now, Asked: true}
	})
	return r
}

// A hold the human chose (Wait for me), or that a question gone unanswered left, lapses once nobody has
// written for HoldLapse, counted from the hold or the human's last word: a proposal baton could have let
// go ahead goes ahead (or a more reversible way does), and one only the human could let through is
// answered Not now. It is announced, logged by timeout, pushed, and told to the model, which is woken.
// While the run is paused, nothing lapses.
func TestTheLeadsHoldLapses(t *testing.T) {
	lapse := DefaultTiming.HoldLapse
	for _, c := range []struct {
		name    string
		by      string
		untimed bool
		maxAuto int
		setup   func(r *rig) // runs once the hold is in place
		at      func(r *rig, held time.Time) time.Time
		answer  string // "" : it never lapses
		tell    []string
	}{
		{"Wait for me", state.ByHeld, false, 0, nil, func(r *rig, held time.Time) time.Time { return held.Add(lapse) }, "Go ahead",
			[]string{"Your proposal d1 (commit P1 unsigned) was held for the human, and nobody has written for 20 minutes", "Do what you proposed now, or a more reversible way",
				"baton note", "undone with: re-sign it", "Then carry on with P1."}},
		{"its question went away", state.ByDeclined, false, 0, nil, func(r *rig, held time.Time) time.Time { return held.Add(lapse) }, "Go ahead",
			[]string{"lifted the hold for them"}},
		{"the last decision P1 may make without the human", state.ByHeld, false, 1, nil, func(r *rig, held time.Time) time.Time { return held.Add(lapse) }, "Go ahead",
			[]string{"That was the last decision baton lets P1 make without the human"}},
		{"an outward one: Not now", state.ByDeclined, true, 0, nil, func(r *rig, held time.Time) time.Time { return held.Add(lapse) }, state.NotNow,
			[]string{"baton answered Not now for them", decide.NotNowRule(), "Then carry on with P1."}},
		{"the human wrote first", state.ByHeld, false, 0, func(r *rig) {
			r.run(5 * time.Minute)
			r.set(func(st *state.State) { st.Run.HumanAt = r.now }) // they took part, and the hold stands (a question of theirs, say)
		}, func(r *rig, held time.Time) time.Time { return held.Add(5*time.Minute + lapse) }, "Go ahead", []string{"lifted the hold for them"}},
		{"after a sleep", state.ByHeld, false, 0, func(r *rig) {
			r.tick(time.Hour) // asleep: nobody could have answered
		}, func(r *rig, held time.Time) time.Time { return r.now.Add(lapse) }, "Go ahead", nil},
		{"paused", state.ByHeld, false, 0, func(r *rig) {
			r.set(func(st *state.State) { st.Mode = state.ModePaused })
		}, func(r *rig, held time.Time) time.Time { return held.Add(3 * lapse) }, "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := heldRig(t, c.by, c.untimed)
			r.loop.MaxAuto = c.maxAuto
			held := r.now
			if c.setup != nil {
				c.setup(r)
			}
			at := c.at(r, held)
			r.runTo(at.Add(-20 * time.Second))
			if st := r.state(); st.Run.Escalation == nil || st.Decisions[0].Resolved.By != c.by {
				t.Fatalf("lapsed %s early: escalation %+v, %+v", at.Sub(r.now), st.Run.Escalation, st.Decisions[0].Resolved)
			}
			if len(r.in.text) != 0 {
				t.Fatalf("typed while held: %q", r.in.text)
			}
			r.run(30 * time.Second)
			st := r.state()
			d := st.Decisions[0]
			if c.answer == "" {
				if st.Run.Escalation == nil || !d.Lapsed.IsZero() || strings.Contains(r.events(), "hold_lapsed") {
					t.Fatalf("lapsed: escalation %+v, %+v", st.Run.Escalation, d)
				}
				return
			}
			if st.Run.Escalation != nil || d.Resolved.By != state.ByTimeout || d.Resolved.Answer != c.answer || d.Lapsed.IsZero() {
				t.Fatalf("escalation %+v, proposal %+v", st.Run.Escalation, d)
			}
			if got := d.WithoutHuman(); got != (c.answer == "Go ahead") {
				t.Errorf("without the human: %v", got)
			}
			if c.maxAuto > 0 && st.ReviewDue != "P1" || c.maxAuto == 0 && st.ReviewDue != "" {
				t.Errorf("review %q", st.ReviewDue)
			}
			var told string
			for _, m := range st.Inbox {
				if m.Kind == state.MessageAnswer {
					told = m.Text
				}
			}
			for _, w := range c.tell {
				if !strings.Contains(told, w) {
					t.Errorf("the model is not told %q:\n%s", w, told)
				}
			}
			if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "baton: nobody answered in 20 minutes") {
				t.Errorf("not announced: %q", st.Run.Announce)
			}
			if !hasEvent(r.events(), "hold_lapsed", map[string]any{"id": "d1", "by": state.ByTimeout, "answer": c.answer}) {
				t.Errorf("events:\n%s", r.events())
			}
			if (c.maxAuto > 0) != strings.Contains(r.events(), `"kind":"review_due"`) {
				t.Errorf("review_due logged wrongly:\n%s", r.events())
			}
			if pushed := strings.Join(r.push.kinds, " "); !strings.Contains(pushed, "hold_lapsed") {
				t.Errorf("pushed %q", pushed)
			}
			if c.maxAuto == 0 && (len(r.in.text) != 1 || r.in.text[0] != decide.MailTyped([]string{state.MessageAnswer})) {
				t.Errorf("the idle model is not woken: %q", r.in.text)
			}
		})
	}
}

// A hold with no proposal of the model's behind it lapses all the same: the run does not wait on the human
// for anything baton can decide. A proposal held before, whose hold the human ended by writing, is not
// this hold's, and is left as it was.
func TestAHoldWithNoProposalLapses(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		p, _ := state.AddProposal(st, state.Decision{What: "an older one", Asked: true}, r.now.Add(-time.Hour))
		state.Resolve(st, p.ID, state.ByHeld, "Wait for me", r.now.Add(-time.Hour))
		st.Run.HumanAt = r.now.Add(-50 * time.Minute)
		st.Run.Escalation = &state.Escalation{Kind: "decision", Reason: "waits for you", Since: r.now, Asked: true}
	})
	r.run(DefaultTiming.HoldLapse + 20*time.Second)
	st := r.state()
	if st.Run.Escalation != nil || len(st.Inbox) != 1 || !strings.Contains(st.Inbox[0].Text, "Carry on with P1") {
		t.Fatalf("escalation %+v, inbox %+v", st.Run.Escalation, st.Inbox)
	}
	if d := st.Decisions[0]; d.Resolved.By != state.ByHeld || !d.Lapsed.IsZero() {
		t.Errorf("the older proposal was taken for this hold's: %+v", d)
	}
	if !hasEvent(r.events(), "hold_lapsed", map[string]any{"by": state.ByTimeout, "reason": "waits for you"}) {
		t.Errorf("events:\n%s", r.events())
	}
}

// A hold's reminders stop where it lapses: it is not pushed as still waiting at the moment it is lifted.
func TestAHoldIsNotRemindedAsItLapses(t *testing.T) {
	r := heldRig(t, state.ByHeld, false)
	r.run(DefaultTiming.HoldLapse + time.Minute)
	n := 0
	for _, k := range r.push.kinds {
		if k == "decision" {
			n++
		}
	}
	if want := 1; n != want { // Reminders[0], 10 minutes in; the one at 20 minutes is the lapse's
		t.Errorf("reminded %d times, want %d: %q", n, want, r.push.kinds)
	}
}

// holdCrew is a crew whose phase agent on P1 put a proposal to the human, which they held: they chose
// Wait for me on its question (by human), or nobody answered one only they could let through (untimed),
// and its default held it. The answer is applied: the agent waits.
func holdCrew(t *testing.T, untimed bool) *crew {
	t.Helper()
	x := newCrew(t, 1)
	x.atWork("P1")
	d := state.Decision{What: "use a fake clock", Because: "drift", Undo: "revert", Relayed: true}
	q := state.Relay{Kind: state.RelayProposal, Decision: "P1.d1", What: d.What, Because: d.Because}
	if untimed {
		d.Untimed, q.Untimed = "outward", "outward"
	}
	x.sub("P1").Update(func(st *state.State) error {
		state.Start(st, x.now, state.Origin{})
		_, err := state.AddProposal(st, d, x.now)
		st.Run.TurnOpen, st.Run.LastActivity, st.Run.LastStop = false, x.now, x.now
		return err
	})
	if err := x.sub("P1").Relay(q); err != nil {
		t.Fatal(err)
	}
	x.lead.Update(func(st *state.State) error {
		r := st.FrontRelay()
		state.IssueRelay(r, "baton: the agent on P1: q", 1, "inst", x.now.Add(10*time.Minute), x.now)
		if untimed {
			state.AnswerRelay(r, state.ByTimeout, decide.RelayDefault(*r), x.now)
		} else {
			state.AnswerRelay(r, state.ByHuman, "Wait for me", x.now)
		}
		return nil
	})
	x.tick(time.Second)
	if sst, _ := x.sub("P1").Load(); sst.Run.Escalation == nil || sst.Run.Escalation.Kind != "decision" || sst.PendingProposal() == nil {
		t.Fatalf("not held: %+v\n%s", sst.Run.Escalation, x.events())
	}
	return x
}

// A phase agent's hold lapses as the lead's does, counted from the answer that held it or the human's
// last word in the lead's session: the agent is told and woken, and the human told in the lead's
// session. While the run is paused, nothing lapses.
func TestAnAgentsHoldLapses(t *testing.T) {
	lapse := DefaultTiming.HoldLapse
	for _, c := range []struct {
		name    string
		untimed bool
		setup   func(x *crew)
		wait    time.Duration // from the hold, before it lapses
		answer  string        // "": it never lapses
		tell    string
	}{
		{"Wait for me", false, nil, lapse, "Go ahead", "Do what you proposed now, or a more reversible way"},
		{"an outward one, nobody answered: Not now", true, nil, lapse, state.NotNow, decide.NotNowRule()},
		{"the human wrote in the lead's session", false, func(x *crew) {
			x.wait(5 * time.Minute)
			x.lead.Update(func(st *state.State) error { st.Run.HumanAt = x.now; return nil })
		}, 5*time.Minute + lapse, "Go ahead", "lifted the hold for them"},
		{"paused", false, func(x *crew) {
			x.lead.Update(func(st *state.State) error { st.Mode = state.ModePaused; return nil })
		}, 3 * lapse, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := holdCrew(t, c.untimed)
			held := x.state().Relay[0].Answered.At
			if c.setup != nil {
				c.setup(x)
			}
			x.wait(held.Add(c.wait - time.Minute).Sub(x.clock()))
			if r := x.state().Relay[0]; !r.Lapsed.IsZero() {
				t.Fatalf("lapsed early: %+v", r)
			}
			x.wait(3 * time.Minute) // past it, and a step more for the agent to be woken
			sst, _ := x.sub("P1").Load()
			d, r := sst.Decisions[0], x.state().Relay[0]
			if c.answer == "" {
				if !r.Lapsed.IsZero() || d.Resolved != nil || sst.Run.Escalation == nil {
					t.Fatalf("lapsed: relay %+v, proposal %+v", r, d)
				}
				return
			}
			if r.Lapsed.IsZero() || d.Resolved == nil || d.Resolved.By != state.ByTimeout || d.Resolved.Answer != c.answer || d.Lapsed.IsZero() || sst.Run.Escalation != nil {
				t.Fatalf("relay %+v, proposal %+v, escalation %+v", r, d, sst.Run.Escalation)
			}
			told := false
			for _, m := range sst.Inbox {
				told = told || m.Kind == state.MessageAnswer && strings.Contains(m.Text, c.tell) && strings.Contains(m.Text, "Your proposal P1.d1")
			}
			if !told || !x.subLogged("P1", "mail_typed", nil) {
				t.Errorf("the agent is not told %q, or not woken for it: inbox %+v\n%s", c.tell, sst.Inbox, x.subEvents("P1"))
			}
			if !x.logged("hold_lapsed", map[string]any{"phase": "P1", "decision": "P1.d1", "by": state.ByTimeout, "answer": c.answer}) {
				t.Errorf("events:\n%s", x.events())
			}
			if !strings.Contains(x.announced(), "baton: nobody answered in 20 minutes") || !strings.Contains(x.announced(), "of the agent on P1") {
				t.Errorf("announced %q", x.announced())
			}
			// Done with: it is not lifted twice, and /baton run does not ask it again, since the sub-run no
			// longer waits on it (what askAgain checks, leftHere).
			x.wait(5 * time.Minute)
			if n := x.count("hold_lapsed", nil); n != 1 {
				t.Errorf("lapsed %d times", n)
			}
			if sst.Waits(r) {
				t.Errorf("the sub-run still waits on it")
			}
		})
	}
}
