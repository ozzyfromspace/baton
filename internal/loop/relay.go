package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A worker's questions reach the human through the lead (state.Relay, docs/research/workers.md). The
// lead's hooks give the front question to its model, and record the answer. The host does the rest:
// the lead's Loop types a line into the idle lead to have the question asked, and answers it if nobody
// does (answerOwnQuestion); the coordinator applies each answer to the worker's sub-run as the worker's
// own answer would have been, and gives it to the worker like a job result (state.Tell), and withdraws
// the questions nobody needs answered any more.

// MaxRelayLines is how many times baton types its line into the idle lead for one worker's question.
// Its stop then asks for it (hooks), and leaves it for the human in the worker's own session after that.
const MaxRelayLines = 2

// relayLine wakes the idle lead to put a worker's question to the human: it types a short line, and the
// prompt's hook gives the model the question. It waits for the typing gates like anything baton types,
// and reports whether it typed.
func (l *Loop) relayLine(v host.View, st state.State, in host.Injector) bool {
	r := st.RelayToAsk()
	if l.Role != RoleLead || r == nil {
		return false
	}
	key := fmt.Sprintf("%s/%s/%s@%s", r.Phase, r.Kind, r.Decision, r.Queued.Format(time.RFC3339Nano))
	if key != l.relayFor {
		l.relayFor, l.relayLines = key, 0
	}
	if l.relayLines >= MaxRelayLines || l.relayLines > 0 && v.Now.Sub(l.since(l.relayAt)) < l.Timing.ResumeNudge {
		return false // typed already: its prompt has not arrived yet, or the model would not ask it
	}
	if l.why != "" {
		l.held(v, st, "put a worker's question to the human")
		return false
	}
	l.proceed()
	l.relayLines++
	l.relayAt = v.Now
	fields := map[string]any{"phase": r.Phase, "type": r.Kind, "count": l.relayLines}
	if r.Decision != "" {
		fields["decision"] = r.Decision
	}
	l.Store.Event("relay_typed", fields)
	if err := in.Type(decide.RelayTyped(r.Phase), true); err != nil {
		l.logf("loop: typing the worker's question: %v", err)
	}
	return true
}

// leaveRelays leaves the workers' questions the lead has not put to the human for them in each worker's
// own session (state.ByLeadStopped): the lead's last turn failed in a way baton cannot retry, so it asks
// nothing until the human acts, and a worker must not wait on it unseen. The coordinator applies each as
// a question that went unanswered: the worker's phase waits for the human there, and they are pushed
// about it. A question on the lead's screen stays, for the human to answer there.
func (l *Loop) leaveRelays(now time.Time) {
	if l.Role != RoleLead {
		return
	}
	var left []state.Relay
	l.update(func(st *state.State) {
		for i := range st.Relay {
			if r := &st.Relay[i]; r.Answered == nil && !(r.Asked && st.Run.Dialogs.Has("relay")) {
				state.AnswerRelay(r, state.ByLeadStopped, "", now)
				left = append(left, *r)
			}
		}
	})
	for _, r := range left {
		l.Store.Event("relay_answered", relayFields(r, map[string]any{"by": state.ByLeadStopped}))
	}
}

// relayOnScreen is the worker's question whose dialog key is key: the one the lead was given, or one
// settled meanwhile whose dialog is still open (its answer then changes nothing, but it must go).
func relayOnScreen(st state.State, key string) *state.Relay {
	for i := len(st.Relay) - 1; i >= 0; i-- {
		if r := &st.Relay[i]; r.Question != "" && decide.Normalize(r.Question) == key {
			return r
		}
	}
	return nil
}

// relay applies the human's answers to the run's workers' questions to their sub-runs, and withdraws the
// questions nobody needs answered any more: settled in the worker's own session, or whose worker is gone.
func (c *Coordinator) relay(now time.Time, s *state.Store) {
	st, err := s.Load()
	if err != nil {
		return
	}
	for _, r := range st.Relay {
		switch {
		case !r.Applied.IsZero():
		case r.Answered == nil:
			if why := c.unneeded(st, r); why != "" {
				c.withdraw(now, s, r, why)
			}
		default:
			c.apply(now, s, st, r)
		}
	}
}

// subStore is the sub-run of the live worker on phase, or nil.
func (c *Coordinator) subStore(st state.State, phase string) *state.Store {
	e := st.WorkerOn(phase)
	if e == nil {
		return nil
	}
	if w := c.session(*e); w != nil {
		return w.store
	}
	sub, err := c.Project.ExistingRun(e.Ref, "")
	if err != nil {
		return nil
	}
	return sub
}

// unneeded says why worker question r needs no answer any more, or "" while it does.
func (c *Coordinator) unneeded(st state.State, r state.Relay) string {
	sub := c.subStore(st, r.Phase)
	if sub == nil {
		return "its worker is gone"
	}
	sst, err := sub.Load()
	if err != nil {
		return ""
	}
	if !sst.Waits(r) {
		return "it was settled in the worker's own session"
	}
	return ""
}

// withdraw settles worker question r, which nobody needs answered any more (why).
func (c *Coordinator) withdraw(now time.Time, s *state.Store, r state.Relay, why string) {
	done := false
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(r); q != nil && q.Answered == nil {
			state.AnswerRelay(q, state.ByWithdrawn, "", now)
			q.Applied, done = now, true
			st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: the question of the worker on %s needs no answer any more: %s", r.Phase, why))
		}
		return nil
	})
	if done {
		s.Event("relay_withdrawn", relayFields(r, map[string]any{"why": why}))
	}
}

// apply applies the answer to worker question r to the worker's sub-run, and records that it did. With
// the worker gone, there is nothing to apply it to.
func (c *Coordinator) apply(now time.Time, s *state.Store, st state.State, r state.Relay) {
	result := "its worker is gone"
	if sub := c.subStore(st, r.Phase); sub != nil {
		if _, err := sub.Update(func(sst *state.State) error { result = applyRelay(sst, r, c.Fleet.MaxAuto, now); return nil }); err != nil {
			return // tried again at the next step
		}
	}
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(r); q != nil {
			q.Applied = now
		}
		return nil
	})
	s.Event("relay_applied", relayFields(r, map[string]any{"answer": r.Answered.Answer, "by": r.Answered.By, "result": result}))
}

// applyRelay applies the answer to worker question r to the worker's sub-run st, and says what it did. A
// question settled there meanwhile is left as it is. maxAuto caps the decisions the phase may make
// without the human (max_auto_decisions; 0: no cap).
func applyRelay(st *state.State, r state.Relay, maxAuto int, now time.Time) string {
	if !st.Waits(r) {
		return "settled in the worker's session already"
	}
	a, phase := *r.Answered, r.Phase
	outcome := decide.RelayOutcome(r, a.Answer)
	tell := func(text string) { state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: text}, now) }
	held := func(what string) string {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "decision", Text: fmt.Sprintf("the worker on %s waits for you in its session (baton view %s): %s", phase, phase, what), At: now})
		return "waits for the human in the worker's session"
	}
	if r.Kind == state.RelayProposal {
		p := st.PendingProposal()
		switch outcome {
		case decide.OutcomeGo:
			if a.By != state.ByTimeout {
				state.Resolve(st, p.ID, state.ByHuman, a.Answer, now)
				tell(decide.RelayGoAhead(p.ID, p.What, phase))
				return "goes ahead"
			}
			state.Resolve(st, p.ID, state.ByTimeout, a.Answer, now)
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "proceeded", Text: p.What, At: now})
			last := st.ReviewDue == "" && decide.CapReached(st.Unattended(phase), maxAuto)
			if last {
				state.DueReview(st, phase, now)
			}
			tell(decide.RelayWentAhead(p.ID, p.What, p.Undo, phase, r.Deadline, last))
			return "went ahead, as nobody answered"
		case decide.OutcomeWords:
			state.Resolve(st, p.ID, state.ByHuman, a.Answer, now)
			tell(decide.RelayOwnWords(r, a.Answer, phase))
			return "the human's own words"
		}
		// Not to be done: held for the human, who answers in the worker's own session (released).
		by, why := state.ByHeld, "The human chose Wait for me"
		switch {
		case outcome == decide.OutcomePause:
			why = "The human paused the run"
		case a.By == state.ByTimeout:
			by, why = state.ByDeclined, "Nobody answered it"
		case a.By == state.ByDeclined:
			by, why = state.ByDeclined, "Its question went away unanswered"
		case a.By == state.ByLeadStopped:
			by, why = state.ByDeclined, "The lead session stopped before it could put it to the human"
		}
		state.Resolve(st, p.ID, by, a.Answer, now)
		reason := fmt.Sprintf("proposal %s waits for you: %s", p.ID, p.What)
		st.Run.Escalation = &state.Escalation{Kind: "decision", Reason: reason, Since: now, Asked: true}
		tell(decide.RelayHeld(p.ID, p.What, phase, why))
		return held(reason)
	}
	switch outcome {
	case decide.OutcomeOn, decide.OutcomeWords:
		because := ""
		if st.Blocked != nil {
			because = st.Blocked.Reason
		}
		state.Resume(st)
		if outcome == decide.OutcomeOn {
			tell(decide.RelayContinue(r.Kind, because, phase))
		} else {
			tell(decide.RelayOwnWords(r, a.Answer, phase))
		}
		return "goes on"
	}
	// Its block or its review stays, for the human to answer in the worker's own session (hooks).
	if r.Kind == state.RelayReview {
		return held("a review of the decisions it made without you")
	}
	return held("it is blocked: " + r.Because)
}

// relayFields are the fields of an event about worker question r.
func relayFields(r state.Relay, more map[string]any) map[string]any {
	fields := map[string]any{"phase": r.Phase, "type": r.Kind}
	if r.Decision != "" {
		fields["decision"] = r.Decision
	}
	for k, v := range more {
		fields[k] = v
	}
	return fields
}
