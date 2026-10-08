package hooks

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A proposal's life after `baton propose` (docs/escalation.md): the model puts baton's question to the
// human; the human answers, or nobody does and baton picks Go ahead at the deadline; or the question goes
// away unanswered, and the proposal is held for the human like Wait for me. What happens next is decided
// here, from the answer, not left to the model.

// asked records that the pending proposal's question reached Claude Code's dialog queue, and tells the
// human out of band (the question names the deadline, so the push only says one is waiting).
func asked(st *state.State, now time.Time) (event, bool) {
	p := st.PendingProposal()
	if p == nil || p.Asked {
		return event{}, false
	}
	p.Asked = true
	notice(st, "proposal", strings.TrimPrefix(p.Question, "baton: "), now)
	fields := map[string]any{"id": p.ID}
	if p.Untimed != "" {
		fields["untimed"] = p.Untimed
	} else {
		fields["deadline"] = p.Deadline.UTC().Format(time.RFC3339)
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
	if auto && answer == "Go ahead" { // baton only ever types Go ahead's key
		by = state.ByTimeout
	} else {
		st.Run.HumanAt = c.Now
	}
	v := valveAction{event: "proposal_resolved", fields: map[string]any{"id": p.ID, "by": by, "answer": answer}}
	switch answer {
	case "Go ahead":
		state.Resolve(st, p.ID, by, answer, c.Now)
		if by == state.ByHuman {
			v.say = "baton: you chose Go ahead on " + p.ID
			v.tell = fmt.Sprintf("%s The human chose Go ahead. Do what you proposed now: %s. Then carry on with %s.", NudgePrefix, p.What, st.Current)
			break
		}
		notice(st, "proceeded", p.What, c.Now)
		v.say = fmt.Sprintf("baton: nobody answered by %s, so baton went ahead with %s — %s", p.Deadline.Local().Format("15:04"), p.ID, p.What)
		v.tell = fmt.Sprintf("%s Nobody answered your proposal %s by %s, so baton chose Go ahead for the human. Do what you proposed now: %s. "+
			"If they later ask, it is undone with: %s.", NudgePrefix, p.ID, p.Deadline.Local().Format("15:04"), p.What, p.Undo)
		if n := st.Unattended(p.Phase); st.ReviewDue == "" && decide.CapReached(n, config.EscalationFromEnv(c.Env).MaxAuto) {
			state.DueReview(st, p.Phase, c.Now)
			v.also = append(v.also, event{"review_due", map[string]any{"phase": p.Phase, "decisions": n}})
			v.tell += " That was the last decision baton lets " + p.Phase + " make without the human: once it is done, end your turn, and baton stops for them to review the decisions."
		} else {
			v.tell += " Then carry on with " + st.Current + "."
		}
	case "Wait for me":
		v.also = holdProposal(st, p, state.ByHeld, answer, c.Now)
		v.say = "baton: holding " + p.ID + " for you — answer in the session when you're ready"
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
			"do what it says, and do what you proposed only if it says so. Then carry on with %s.", NudgePrefix, p.ID, p.What, strconv.Quote(answer), st.Current)
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
// (Esc), the session restarted, or Claude Code went idle with it gone. Nobody said Go ahead, so it is
// declined, which holds it for the human like Wait for me. If the human is the one who just typed, they
// are here: nothing is held, and their message is the answer (tell says so to the model).
func vanished(st *state.State, now time.Time, human bool) (events []event, tell string) {
	p := st.PendingProposal()
	if p == nil || !p.Asked || st.Run.Dialogs.Has("proposal") {
		return nil, ""
	}
	events = []event{{"proposal_resolved", map[string]any{"id": p.ID, "by": state.ByDeclined}}}
	if human {
		state.Resolve(st, p.ID, state.ByDeclined, "", now)
		return events, heldTell(*p)
	}
	return append(events, holdProposal(st, p, state.ByDeclined, "", now)...), ""
}

// released ends a hold when the human writes in the session: their message is the answer to it.
func released(st *state.State) (event, string, bool) {
	e := st.Run.Escalation
	if e == nil || e.Kind != "decision" {
		return event{}, "", false
	}
	for i := len(st.Decisions) - 1; i >= 0; i-- {
		d := st.Decisions[i]
		if r := d.Resolved; d.Kind == state.Proposal && r != nil && (r.By == state.ByHeld || r.By == state.ByDeclined) {
			return event{"hold_released", map[string]any{"id": d.ID, "by": state.ByHuman}}, heldTell(d), true
		}
	}
	return event{}, "", false
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
		v.tell = NudgePrefix + " The human reviewed the decisions " + phase + " made without them and chose Continue. Carry on with " + st.Current + "."
	default:
		state.Resume(st)
		v.say = "baton: your answer to the review went to Claude"
		v.tell = fmt.Sprintf("%s The human answered the review of the decisions %s made without them in their own words: %s. "+
			"That is their instruction: follow it, then carry on with %s.", NudgePrefix, phase, strconv.Quote(answer), st.Current)
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
// escalation.md). What the human wrote may be a late answer, "undo that", to a decision a compaction
// or a /clear has taken out of the model's context: its undo is then at hand.
func tellDecisions(st *state.State) (ids []string, tell string) {
	var untold []state.Decision
	for i := range st.Decisions {
		if d := &st.Decisions[i]; d.WithoutHuman() && !d.Told {
			d.Told = true
			untold, ids = append(untold, *d), append(ids, d.ID)
		}
	}
	if len(untold) == 0 {
		return nil, ""
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
