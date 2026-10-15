package hooks

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A proposal's life after `baton propose` (docs/design.md): the model puts baton's question to the
// human; the human answers, or nobody does and baton picks Go ahead at the deadline (Not now for an
// outward one); or the question goes away unanswered, and baton still answers it so at its deadline
// unless the human writes first. What happens next is decided here, from the answer, not left to the
// model.

// asked records that the pending proposal's question reached Claude Code's dialog queue, and tells the
// human out of band (the question names the deadline, so the push only says one is waiting).
func asked(st *state.State, now time.Time) (event, bool) {
	p := st.PendingProposal()
	if p == nil || p.Asked {
		return event{}, false
	}
	p.Asked = true
	notice(st, "proposal", strings.TrimPrefix(p.Question, "baton: "), now)
	fields := map[string]any{"id": p.ID, "deadline": p.Deadline.UTC().Format(time.RFC3339)}
	if p.Outward != "" {
		fields["outward"] = p.Outward
	}
	return event{"proposal_asked", fields}, true
}

// answerTo finds the answer a question call's result gives to question.
func answerTo(c Context, question string) (string, bool) {
	resp, _ := c.Input["tool_response"].(map[string]any)
	answers, _ := resp["answers"].(map[string]any)
	for q, a := range answers {
		if decide.Normalize(q) == decide.Normalize(question) {
			s, _ := a.(string)
			return strings.TrimSpace(s), true
		}
	}
	return "", false
}

// answeredProposal acts on the answer to the pending proposal. auto says baton answered it, because
// nobody else did by the deadline. It runs before answered(), so that Pause baton also settles the
// proposal.
func answeredProposal(st *state.State, c Context, auto bool) valveAction {
	p := st.PendingProposal()
	if p == nil || p.Question == "" {
		return valveAction{}
	}
	answer, found := answerTo(c, p.Question)
	if !found {
		return valveAction{}
	}
	by := state.ByHuman
	if auto && answer == p.Default() { // baton only ever gives the default: Go ahead, or Not now
		by = state.ByTimeout
	} else {
		st.Run.HumanAt = c.Now
	}
	v := valveAction{event: "proposal_resolved", fields: map[string]any{"id": p.ID, "by": by, "answer": answer}}
	switch answer {
	case "Go ahead":
		if by == state.ByHuman {
			state.Resolve(st, p.ID, by, answer, c.Now)
			v.say = "baton: you chose Go ahead on " + p.ID
			v.tell = fmt.Sprintf("%s The human chose Go ahead. Do what you proposed now: %s. Then carry on with %s.", NudgePrefix, p.What, decide.Named(st.Current))
			break
		}
		if n, last := state.WentAhead(st, p, config.EscalationFromEnv(c.Env).MaxAuto, c.Now); last {
			v.also = append(v.also, event{"review_due", map[string]any{"phase": p.Phase, "decisions": n}})
			v.say, v.tell = decide.WentAhead(p.ID, p.What, p.Undo, p.Phase, p.Deadline, true)
		} else {
			v.say, v.tell = decide.WentAhead(p.ID, p.What, p.Undo, st.Current, p.Deadline, false)
		}
	case state.NotNow:
		deadline := p.Deadline
		if by == state.ByHuman {
			state.Resolve(st, p.ID, by, answer, c.Now)
			deadline = time.Time{}
		} else {
			state.LeftNotNow(st, p, c.Now)
		}
		v.say, v.tell = decide.AnsweredNotNow(p.ID, p.What, st.Current, deadline)
	case "Wait for me":
		v.also = holdProposal(st, p, state.ByHeld, answer, c.Now)
		v.say = decide.Holding(p.ID, decide.HoldLapse, p.Outward != "")
		v.tell = NudgePrefix + " The human chose Wait for me: do not do what you proposed (" + p.What + "). " +
			"End your turn now; the run waits for them, and their answer will reach you here."
	case "Pause baton":
		state.Resolve(st, p.ID, state.ByPaused, answer, c.Now)
		if state.Pause(st) == nil {
			v.also = append(v.also, event{"paused", nil})
		}
		v.say = "baton: paused — the human is driving"
		v.tell = NudgePrefix + " The human chose Pause baton: baton is paused, and what you proposed (" + p.What + ") is not to be done. Wait for the human's instructions."
	default:
		// The human typed an answer of their own: relay it, word for word.
		state.Resolve(st, p.ID, state.ByHuman, answer, c.Now)
		v.say = "baton: your answer to " + p.ID + " went to Claude"
		v.tell = fmt.Sprintf("%s The human answered your proposal %s (%s) in their own words: %s. That is their instruction, not a Go ahead: "+
			"do what it says, and do what you proposed only if it says so. Then carry on with %s.", NudgePrefix, p.ID, p.What, strconv.Quote(answer), decide.Named(st.Current))
	}
	if p.Resolved != nil {
		v.fields["by"] = p.Resolved.By // held, paused: what the record says
	}
	return v
}

// holdProposal settles the pending proposal without doing it, and holds the run for the human: an
// escalation of kind decision, already asked, so no second question follows (the push still goes out).
// It ends when the human next takes part.
func holdProposal(st *state.State, p *state.Decision, by, answer string, now time.Time) []event {
	state.Resolve(st, p.ID, by, answer, now)
	reason := fmt.Sprintf("proposal %s waits for you: %s", p.ID, p.What)
	st.Run.Escalation = &state.Escalation{Kind: "decision", Reason: reason, Since: now, Asked: true}
	notice(st, "decision", reason, now)
	return []event{{"escalated", map[string]any{"type": "decision", "reason": reason}}}
}

// vanished settles a proposal whose question went away without an answer: the turn was interrupted
// (Esc), the session restarted, or Claude Code went idle with it gone. If the human is the one who just
// typed, they are here: it is declined, and their message is the answer (tell says so to the model).
// Otherwise nobody chose anything: the proposal stays pending, dismissed, and baton answers it at its
// deadline (the lead's loop, Loop.lapse): Go ahead, or Not now for an outward one. One with no deadline on
// record is declined, which holds it for the human like Wait for me, until the hold lapses.
func vanished(st *state.State, now time.Time, human bool) (events []event, tell string) {
	p := st.PendingProposal()
	if p == nil || !p.Asked || st.Run.Dialogs.Has("proposal") {
		return nil, ""
	}
	declined := []event{{"proposal_resolved", map[string]any{"id": p.ID, "by": state.ByDeclined}}}
	switch {
	case human:
		state.Resolve(st, p.ID, state.ByDeclined, "", now)
		return declined, heldTell(*p)
	case !p.Deadline.IsZero():
		if !p.Dismissed.IsZero() {
			return nil, "" // dismissed already: baton answers it at its deadline
		}
		p.Dismissed = now
		st.Run.Announce = append(st.Run.Announce, decide.ProposalDismissed(*p))
		return []event{{"proposal_dismissed", map[string]any{"id": p.ID, "deadline": p.Deadline.UTC().Format(time.RFC3339)}}}, ""
	}
	return append(declined, holdProposal(st, p, state.ByDeclined, "", now)...), ""
}

// released ends a hold when the human writes in the session: their message is the answer to it, and to
// the proposal held behind it, if there is one (the run itself may be held, at their Wait for me on
// baton's question about it).
func released(st *state.State) (event, string, bool) {
	e := st.Run.Escalation
	if e == nil || e.Kind != "decision" {
		return event{}, "", false
	}
	if d := st.HeldProposal(e.Since); d != nil {
		return event{"hold_released", map[string]any{"id": d.ID, "by": state.ByHuman}}, heldTell(*d), true
	}
	return event{"hold_released", map[string]any{"reason": e.Reason, "by": state.ByHuman}}, decide.HoldReleased(e.Reason), true
}

func heldTell(p state.Decision) string {
	return fmt.Sprintf("%s Your proposal %s (%s) was not answered with Go ahead, and the human has now written to you: their message is the answer. "+
		"Do what you proposed only if they say so.", NudgePrefix, p.ID, p.What)
}

// answeredReview acts on the answer to a review of the decisions a phase made without the human.
func answeredReview(st *state.State, c Context) valveAction {
	e := st.Run.Escalation
	if e == nil || e.Kind != "review" || e.Question == "" {
		return valveAction{}
	}
	answer, found := answerTo(c, e.Question)
	if !found {
		return valveAction{}
	}
	st.Run.HumanAt = c.Now
	phase := st.ReviewDue
	v := valveAction{event: "review_resolved", fields: map[string]any{"phase": phase, "answer": answer, "by": state.ByHuman}}
	switch answer {
	case "Pause baton":
		state.Reviewed(st) // the human has taken over, and will hand back with /baton run
		if state.Pause(st) == nil {
			v.also = append(v.also, event{"paused", nil})
		}
		v.say = "baton: paused — the human is driving"
		v.tell = NudgePrefix + " The human chose Pause baton: baton is paused. Wait for the human's instructions."
	case "Continue":
		state.Resume(st)
		v.say = "baton: reviewed — the run goes on"
		v.tell = NudgePrefix + " The human reviewed the decisions " + phase + " made without them and chose Continue. Carry on with " + decide.Named(st.Current) + "."
	default:
		state.Resume(st)
		v.say = "baton: your answer to the review went to Claude"
		v.tell = fmt.Sprintf("%s The human answered the review of the decisions %s made without them in their own words: %s. "+
			"That is their instruction: follow it, then carry on with %s.", NudgePrefix, phase, strconv.Quote(answer), decide.Named(st.Current))
	}
	return v
}

// goingAhead reports a proposal the human or the timer let go ahead in the current turn: the model is
// doing it, and must be let finish even once a review is due.
func goingAhead(st *state.State) bool {
	for _, d := range st.Decisions {
		if r := d.Resolved; d.Kind == state.Proposal && r != nil && r.Answer == "Go ahead" && r.At.After(st.Run.LastStop) {
			return true
		}
	}
	return false
}

// tellDecisions hands the model, as the human writes to it, the decisions made without them that it has
// not been handed at such a moment yet, and marks them told (the S3 mechanism, docs/research/
// escalation.md): the run's own, and its workers' (from their sub-runs, which the caller marks told,
// Store.ToldWorkers). What the human wrote may be a late answer, "undo that", to a decision a compaction
// or a /clear has taken out of the model's context, or that a worker made: its undo is then at hand.
func tellDecisions(st *state.State, workers []state.Decision) (ids []string, tell string) {
	var untold []state.Decision
	for i := range st.Decisions {
		if d := &st.Decisions[i]; d.WithoutHuman() && !d.Told {
			d.Told = true
			untold = append(untold, *d)
		}
	}
	for _, d := range workers {
		if d.WithoutHuman() && !d.Told {
			untold = append(untold, d)
		}
	}
	if len(untold) == 0 {
		return nil, ""
	}
	slices.SortStableFunc(untold, func(a, b state.Decision) int { return a.At.Compare(b.At) })
	for _, d := range untold {
		ids = append(ids, d.ID)
	}
	return ids, NudgePrefix + " " + decide.LateAnswer(untold)
}

// announceNotes shows the human, in the session, each note not yet shown, and marks it shown.
func announceNotes(st *state.State) string {
	var lines []string
	for i := range st.Decisions {
		if d := &st.Decisions[i]; d.Kind == state.Note && !d.Announced {
			d.Announced = true
			lines = append(lines, "baton: noted — "+d.What)
		}
	}
	return strings.Join(lines, "\n")
}
