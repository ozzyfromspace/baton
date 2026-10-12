package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's questions reach the human in Claude Code's own dialog, which baton's mod opens in the
// lead's terminal (state.Relay, docs/research/visible-workers.md). The coordinator has the mod ask the
// front question, takes the mod's report of its answer (the human's, its default at the deadline, or
// none: the dialog went away), applies each answer to the agent's sub-run as the agent's own answer
// would have been, gives it to the agent like a job result (state.Tell), and withdraws the questions
// nobody needs answered any more. No model takes part: the lead's never sees them.

// relayLate is how long past its deadline a question the mod put to the human may stay unanswered before
// the coordinator takes its default for them all the same: the mod settles it at the deadline, so one
// still open then has lost its mod (a reload, or its hook failed). relayLost is how long one the mod was
// told to ask may go undrawn while no dialog is in front of it before it is told again: the mod asks
// from its band, which is drawn about every second while no dialog is open, and a reloaded mod has
// forgotten what it was to ask.
const (
	relayLate = time.Minute
	relayLost = 30 * time.Second
)

// relayOnScreen is the phase agent's question whose dialog key is key: the one the mod was told to ask,
// or one settled meanwhile whose dialog is still open (its answer then changes nothing).
func relayOnScreen(st state.State, key string) *state.Relay {
	for i := len(st.Relay) - 1; i >= 0; i-- {
		if r := &st.Relay[i]; r.Question != "" && decide.Normalize(r.Question) == key {
			return r
		}
	}
	return nil
}

// relay looks over the run's phase agents' questions once: it applies the answers to their sub-runs,
// withdraws the questions nobody needs answered any more (settled in the agent's sub-run, or whose agent
// is gone), takes the default of one the mod lost past its deadline, and has the mod ask the front one
// when it is due, or again when it lost it.
func (c *Coordinator) relay(now time.Time, s *state.Store, b *bridge.Bridge) {
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
			} else if r.Asked && now.After(latest(r.Deadline, r.AskedAt.Add(decide.MinWait)).Add(relayLate)) {
				c.answered(now, s, r.Question, decide.RelayDefault(r), state.ByTimeout, "late")
			}
		default:
			c.apply(now, s, st, r)
		}
	}
	c.ask(now, s, b)
}

// ask has the mod put the front question of the run's phase agents to the human (bridge.Ask): once it is
// due (state.RelayDue), and again when the mod lost it: the host that had it asked is gone, and the claude
// it ran with it, or it was never drawn although nothing has been in front of it for relayLost. A timed
// proposal goes ahead at the time its question names, and anything else gets its default (it waits for
// the human) after the escalation timeout; asked again, it keeps its deadline.
func (c *Coordinator) ask(now time.Time, s *state.Store, b *bridge.Bridge) {
	st, err := s.Load()
	if err != nil {
		return
	}
	r := st.FrontRelay()
	switch {
	case r == nil:
		return
	case r.Issued.IsZero():
		if st.RelayDue() == nil {
			return // the plan does not run, or the lead's own question goes first
		}
	case r.Instance == s.Instance && (r.Asked || st.Run.Dialogs.AnyOpen() || now.Sub(r.Issued) < relayLost):
		return // the mod has it
	}
	deadline, again := r.Deadline, !r.Issued.IsZero()
	if !again {
		deadline = now.Add(c.Lead.Timing.EscalationTimeout)
		if r.Timed() {
			deadline = decide.Deadline(now, c.Lead.Timing.EscalationTimeout) // the time its question names
		}
	}
	q := decide.RelayQuestion(*r, deadline)
	cmd, err := b.Issue(bridge.Command{Kind: bridge.Ask, Instance: s.Instance, Phase: r.Phase, Question: q.Text, Options: labels(q.Options), Header: "baton"})
	if err != nil {
		c.Lead.logf("loop: asking the question of %s: %v", r.Phase, err)
		return
	}
	issued := false
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(*r); q != nil && q.Answered == nil {
			state.IssueRelay(q, decide.RelayQuestion(*q, deadline).Text, cmd.ID, s.Instance, deadline, now)
			issued = true
		}
		return nil
	})
	if !issued {
		b.Void(s.Instance, cmd.ID) // answered or withdrawn meanwhile
		return
	}
	fields := map[string]any{"via": "mod", "deadline": deadline.UTC().Format(time.RFC3339), "command": cmd.ID}
	if r.Timed() {
		fields["timed"] = true
	}
	if again {
		fields["again"] = true
	}
	s.Event("relay_issued", relayFields(*r, fields))
}

// labels are the labels of options, as the mod's dialog shows them.
func labels(options []decide.Option) []string {
	var out []string
	for _, o := range options {
		out = append(out, o.Label)
	}
	return out
}

// asked takes the mod's report r of the answer to a question it put to the human: theirs, the default it
// gave the question at its deadline, or none (the dialog went away unanswered, which holds it for them
// like Wait for me).
func (c *Coordinator) asked(now time.Time, s *state.Store, r bridge.Report) {
	switch {
	case r.Dismissed:
		c.answered(now, s, r.Question, "", state.ByDeclined, "mod")
	case r.Deadline:
		c.answered(now, s, r.Question, r.Answer, state.ByTimeout, "mod")
	default:
		c.answered(now, s, r.Question, r.Answer, state.ByHuman, "mod")
	}
}

// answered records answer, by by, to the phase agent's question the mod was told to ask as question, and
// marks its dialog closed (it is, once answered: the result of a call the mod answered itself fires no
// hook). The next step applies it. A question settled meanwhile is left as it was.
func (c *Coordinator) answered(now time.Time, s *state.Store, question, answer, by, via string) {
	var r state.Relay
	found := false
	s.Update(func(st *state.State) error {
		q := st.RelayIssued()
		if q == nil || decide.Normalize(q.Question) != decide.Normalize(question) {
			return nil
		}
		state.AnswerRelay(q, by, answer, now)
		if by == state.ByHuman {
			st.Run.HumanAt = now
		}
		st.Run.Dialogs.CloseExact("", "AskUserQuestion", decide.Normalize(question))
		r, found = *q, true
		return nil
	})
	if !found {
		return
	}
	fields := map[string]any{"by": by, "via": via}
	if answer != "" {
		fields["answer"] = answer
	}
	s.Event("relay_answered", relayFields(r, fields))
}

// subStore is the sub-run of the live phase agent on phase, or nil.
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

// unneeded says why phase agent question r needs no answer any more, or "" while it does.
func (c *Coordinator) unneeded(st state.State, r state.Relay) string {
	sub := c.subStore(st, r.Phase)
	if sub == nil {
		return "its agent is gone"
	}
	sst, err := sub.Load()
	if err != nil {
		return ""
	}
	if !sst.Waits(r) {
		return "it was settled meanwhile"
	}
	return ""
}

// withdraw settles phase agent question r, which nobody needs answered any more (why).
func (c *Coordinator) withdraw(now time.Time, s *state.Store, r state.Relay, why string) {
	done := false
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(r); q != nil && q.Answered == nil {
			state.AnswerRelay(q, state.ByWithdrawn, "", now)
			q.Applied, done = now, true
			st.Run.Announce = append(st.Run.Announce, decide.RelayWithdrawn(r.Phase, why))
		}
		return nil
	})
	if done {
		s.Event("relay_withdrawn", relayFields(r, map[string]any{"why": why}))
	}
}

// apply applies the answer to phase agent question r to the agent's sub-run, and records that it did.
// With the agent gone, there is nothing to apply it to. Pause baton pauses the run as well.
func (c *Coordinator) apply(now time.Time, s *state.Store, st state.State, r state.Relay) {
	result := "its agent is gone"
	if sub := c.subStore(st, r.Phase); sub != nil {
		if _, err := sub.Update(func(sst *state.State) error { result = applyRelay(sst, r, c.Fleet.MaxAuto, now); return nil }); err != nil {
			return // tried again at the next step
		}
	}
	paused := false
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(r); q != nil {
			q.Applied = now
		}
		if decide.RelayOutcome(r, r.Answered.Answer) == decide.OutcomePause && state.Pause(st) == nil {
			paused = true
			st.Run.Announce = append(st.Run.Announce, decide.RelayPaused(r.Phase))
		}
		return nil
	})
	s.Event("relay_applied", relayFields(r, map[string]any{"answer": r.Answered.Answer, "by": r.Answered.By, "result": result}))
	if paused {
		s.Event("paused", map[string]any{"by": "relay", "phase": r.Phase})
	}
}

// applyRelay applies the answer to phase agent question r to the agent's sub-run st, and says what it
// did. A question settled there meanwhile is left as it is. maxAuto caps the decisions the phase may make
// without the human (max_auto_decisions; 0: no cap). A question not answered with a way on (Wait for me,
// Pause baton, none) holds the phase for the human: a proposal stays unresolved, behind an escalation,
// and a block or a review stays; /baton run asks the human again (state.AskAgain).
func applyRelay(st *state.State, r state.Relay, maxAuto int, now time.Time) string {
	if !st.Waits(r) {
		return "settled in its sub-run already"
	}
	a, phase := *r.Answered, r.Phase
	outcome := decide.RelayOutcome(r, a.Answer)
	tell := func(text string) {
		state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: text, About: r.Key()}, now)
	}
	held := func(what string) string {
		text := decide.RelayWaits(phase, what)
		if n := len(st.Run.Notices); n == 0 || st.Run.Notices[n-1].Text != text { // once, if applied twice
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "decision", Text: text, At: now})
		}
		return "waits for the human"
	}
	settled := func() {
		if e := st.Run.Escalation; e != nil && e.Kind == "decision" {
			st.Run.Escalation = nil // held for the human, who has answered now
		}
	}
	if r.Kind == state.RelayProposal {
		p := st.PendingProposal()
		switch outcome {
		case decide.OutcomeGo:
			settled()
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
			settled()
			state.Resolve(st, p.ID, state.ByHuman, a.Answer, now)
			tell(decide.RelayOwnWords(r, a.Answer, phase))
			return "the human's own words"
		}
		// Not to be done: held for the human, unresolved.
		why := "The human chose Wait for me"
		switch {
		case outcome == decide.OutcomePause:
			why = "The human paused the run"
		case a.By == state.ByTimeout:
			why = "Nobody answered it"
		case a.By == state.ByDeclined:
			why = "Its question went away unanswered"
		}
		reason := fmt.Sprintf("proposal %s waits for you: %s", p.ID, p.What)
		if e := st.Run.Escalation; e == nil || e.Kind != "decision" || e.Reason != reason {
			st.Run.Escalation = &state.Escalation{Kind: "decision", Reason: reason, Since: now, Asked: true}
			tell(decide.RelayHeld(p.ID, p.What, phase, why)) // once: held already, it knows
		}
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
	// Its block or its review stays, for the human.
	if r.Kind == state.RelayReview {
		return held("a review of the decisions it made without you")
	}
	return held("it is blocked: " + r.Because)
}

// relayFields are the fields of an event about phase agent question r.
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
