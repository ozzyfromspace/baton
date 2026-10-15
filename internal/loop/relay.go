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
			} else if r.Lapses() && !r.Asked && st.Mode == state.ModeRunning && !now.Before(r.Due(c.Lead.Timing.EscalationTimeout)) {
				c.lapsed(now, s, b, r)
			}
		default:
			c.apply(now, s, st, r)
		}
	}
	c.lapseHolds(now, s, st)
	c.stoppedAgain(now, s, st)
	c.ask(now, s, b)
}

// lapseHolds lifts each hold a phase agent's proposal put on its phase for the human (they answered its
// question Wait for me or Pause baton, or it went away unanswered or got that default, and the proposal
// waits in the agent's sub-run) once nobody has written for Timing.HoldLapse, counted from that answer or
// the human's last word in the lead's session, whichever is later: the run does not wait on them any
// longer. It goes as the lead's does (Loop.lapseHold): a proposal baton could have let go ahead does, or a
// more reversible way does; one only the human could let through is answered Not now. While the run is
// paused nothing lapses, and /baton run asks the human again (state.AskAgain), which starts it over.
func (c *Coordinator) lapseHolds(now time.Time, s *state.Store, st state.State) {
	after := c.Lead.Timing.HoldLapse
	if st.Mode != state.ModeRunning || after <= 0 {
		return
	}
	for _, r := range st.Relay {
		if r.Kind != state.RelayProposal || r.Answered == nil || r.Answered.By == state.ByWithdrawn || r.Applied.IsZero() || !r.Lapsed.IsZero() {
			continue
		}
		if o := decide.RelayOutcome(r, r.Answered.Answer); o == decide.OutcomeGo || o == decide.OutcomeWords {
			continue // not held
		}
		if now.Before(c.Lead.since(latest(r.Answered.At, st.Run.HumanAt)).Add(after)) {
			continue
		}
		c.lapseHold(now, s, st, r)
	}
}

// lapseHold lifts the hold phase agent question r put on its phase (lapseHolds), in the agent's sub-run,
// which tells the agent, and records it in the run, which tells the human. Settled there meanwhile, or
// with the agent gone, there is nothing to lift: it is only marked done with.
func (c *Coordinator) lapseHold(now time.Time, s *state.Store, st state.State, r state.Relay) {
	after := c.Lead.Timing.HoldLapse
	var say, answer string
	var n int
	var last bool
	if sub := c.subStore(st, r.Phase); sub != nil {
		if _, err := sub.Update(func(sst *state.State) error {
			say, answer, n, last = lapseRelayHold(sst, r, after, c.Fleet.MaxAuto, now)
			return nil
		}); err != nil {
			return // tried again at the next step
		}
	}
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(r); q != nil && q.Answered != nil && q.Lapsed.IsZero() {
			q.Lapsed = now
		}
		if say != "" {
			st.Run.Announce = append(st.Run.Announce, say)
		}
		return nil
	})
	if say == "" {
		return
	}
	s.Event("hold_lapsed", relayFields(r, map[string]any{"by": state.ByTimeout, "answer": answer, "after": after.String()}))
	if last {
		s.Event("review_due", map[string]any{"phase": r.Phase, "decisions": n})
	}
}

// lapseRelayHold lifts, in the agent's sub-run st, the hold phase agent question r put on its proposal,
// nobody having written for after: baton answers it for the human (state.LapseHold), and the agent is
// told, at its next tool call or stop, or woken. maxAuto caps the decisions the phase may make without
// the human (max_auto_decisions; 0: no cap). It returns what the human is told ("" when the proposal was
// settled there meanwhile), the answer, how many decisions the phase has made without the human, and
// whether that was the last it may make.
func lapseRelayHold(st *state.State, r state.Relay, after time.Duration, maxAuto int, now time.Time) (say, answer string, n int, last bool) {
	if !st.Waits(r) {
		return "", "", 0, false
	}
	if e := st.Run.Escalation; e != nil && e.Kind == "decision" {
		st.Run.Escalation = nil
	}
	p := st.PendingProposal()
	id, what, undo, untimed := p.ID, p.What, decide.UndoOf(*p), p.Untimed != ""
	answer, n, last = state.LapseHold(st, p, maxAuto, now)
	say, tell := decide.RelayHoldLapsed(id, what, undo, r.Phase, after, untimed, last)
	state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: tell, About: r.Key()}, now)
	return say, answer, n, last
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
	case !r.Dismissed.IsZero():
		return // the human dismissed it: it goes ahead at its deadline (lapsed), and the next question waits behind it
	case r.Instance == s.Instance && (r.Asked || st.Run.Dialogs.AnyOpen() || now.Sub(r.Issued) < relayLost):
		return // the mod has it
	}
	deadline, again := r.Deadline, !r.Issued.IsZero()
	if !again {
		deadline = now.Add(c.Lead.Timing.EscalationTimeout)
		if r.Lapses() {
			deadline = r.Due(c.Lead.Timing.EscalationTimeout) // the time its question names, from when it was made
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
// gave the question at its deadline, or none: the dialog went away unanswered, which goes ahead with a
// timed proposal at its deadline all the same (dismissed), and holds anything else for them like Wait for
// me.
func (c *Coordinator) asked(now time.Time, s *state.Store, r bridge.Report) {
	switch {
	case r.Dismissed && c.dismissed(now, s, r.Question):
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

// dismissed records that the dialog of the timed proposal the mod was told to ask as question went away
// unanswered: nobody chose anything, so it is not held for the human but goes ahead at its deadline, unless
// they answer first (lapsed). It reports whether question was such a proposal.
func (c *Coordinator) dismissed(now time.Time, s *state.Store, question string) bool {
	var r state.Relay
	found := false
	s.Update(func(st *state.State) error {
		q := st.RelayIssued()
		if q == nil || !q.Lapses() || decide.Normalize(q.Question) != decide.Normalize(question) {
			return nil
		}
		q.Asked, q.Dismissed = false, now
		st.Run.Dialogs.CloseExact("", "AskUserQuestion", decide.Normalize(question))
		r, found = *q, true
		return nil
	})
	if found {
		s.Event("relay_dismissed", relayFields(r, map[string]any{"deadline": r.Due(c.Lead.Timing.EscalationTimeout).UTC().Format(time.RFC3339)}))
	}
	return found
}

// lapsed settles timed proposal r, which reached its deadline with nobody's answer and not on screen: the
// lead's own question went first, the run waited on something else, or its dialog was dismissed. Nobody
// said no, so it goes ahead, as one on screen would have (state.ByTimeout); the mod's command to ask it,
// if one is open, is voided.
func (c *Coordinator) lapsed(now time.Time, s *state.Store, b *bridge.Bridge, r state.Relay) {
	due := r.Due(c.Lead.Timing.EscalationTimeout)
	done := false
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(r); q != nil && q.Answered == nil && !q.Asked {
			q.Deadline = due // the time the agent is told it went ahead
			state.AnswerRelay(q, state.ByTimeout, decide.RelayDefault(*q), now)
			if q.Question != "" {
				st.Run.Dialogs.CloseExact("", "AskUserQuestion", decide.Normalize(q.Question))
			}
			r, done = *q, true
		}
		return nil
	})
	if !done {
		return
	}
	if r.Ask != 0 && b != nil {
		b.Void(r.Instance, r.Ask)
	}
	s.Event("relay_answered", relayFields(r, map[string]any{"by": state.ByTimeout, "via": "lapsed", "answer": r.Answered.Answer}))
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
	if r.AboutAgent() { // a question of the run's roster, not of the agent's sub-run
		switch {
		case st.WorkerOn(r.Phase) == nil:
			return "its agent is gone"
		case !st.AgentWaits(r):
			return "it was settled meanwhile"
		}
		return ""
	}
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
	if r.AboutAgent() {
		c.applyStop(now, s, st, r)
		return
	}
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
