package hooks

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The lead session puts the run's workers' questions to the human (state.Relay), one at a time, and only
// while nothing of its own waits on them. Its model is given the front question as hook feedback, which
// it asks word for word (spike 24): with the result of its next tool call, by a refusal of its stop, or
// with the prompt of a line the host types into the idle session. The question's dialog opening is the
// question asked; its answer, found by its exact text, is recorded here, and the host's coordinator
// applies it to the worker's sub-run. A worker's own hooks only queue its questions (worker.go).

// relayToGive is the worker's question the lead's model is to be given now, and its exact question: the
// front question, issued now if it is due (state.RelayDue), which starts its deadline; or one it was
// given already and has not put to the human (a compaction, or an interrupted turn, may have taken it
// away). fresh says it was issued now.
func relayToGive(st *state.State, now time.Time, timeout time.Duration) (r *state.Relay, q decide.Question, fresh bool) {
	if r = st.RelayDue(); r != nil {
		deadline := now.Add(timeout) // when baton leaves it for the human in the worker's session
		if r.Timed() {
			deadline = decide.Deadline(now, timeout) // the time its question names
		}
		q = decide.RelayQuestion(*r, deadline)
		state.IssueRelay(r, q.Text, deadline, now)
		return r, q, true
	}
	if r = st.RelayUnasked(); r != nil {
		return r, relayQuestion(*r), false
	}
	return nil, decide.Question{}, false
}

// relayQuestion is worker question r as baton issued it.
func relayQuestion(r state.Relay) decide.Question {
	return decide.Question{Text: r.Question, Options: decide.RelayOptions(r)}
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

// relayIssued is the event of worker question r given to the lead's model through via, with its
// deadline.
func relayIssued(r state.Relay, via string) event {
	more := map[string]any{"via": via, "deadline": r.Deadline.UTC().Format(time.RFC3339)}
	if r.Timed() {
		more["timed"] = true
	}
	return event{"relay_issued", relayFields(r, more)}
}

// relayGiven is what giving worker question r (exactly q) to the lead's model mid-turn does: the model is
// told to ask it, and the human that it comes.
func relayGiven(r state.Relay, q decide.Question) valveAction {
	e := relayIssued(r, "tool")
	return valveAction{event: e.kind, fields: e.fields, say: relayComing(r.Phase), tell: decide.RelayAsk(r.Phase, q.Call())}
}

// relayComing tells the human that the question of the worker on phase is about to be put to them.
func relayComing(phase string) string {
	return "baton: the worker on " + phase + " has a question for you — Claude puts it to you now"
}

// relayAsked records that the worker's question the lead was given reached Claude Code's dialog queue,
// and pushes the human about a proposal (its question names the deadline). A worker's block or review
// pushed them already, as it began.
func relayAsked(st *state.State, now time.Time) (event, bool) {
	r := st.RelayIssued()
	if r == nil || r.Asked {
		return event{}, false
	}
	r.Asked = true
	if r.Kind == state.RelayProposal {
		notice(st, "proposal", strings.TrimPrefix(r.Question, "baton: "), now)
	}
	return event{"relay_asked", relayFields(*r, map[string]any{"deadline": r.Deadline.UTC().Format(time.RFC3339)})}, true
}

// answeredRelay records the answer to a worker's question the lead put to the human, found by its exact
// text: the human's, or the one baton picked because nobody answered by its deadline (auto). The host's
// coordinator applies it to the worker's sub-run; the lead's model is told it has nothing to do about
// it, unless the human paused the run. A question settled meanwhile (in the worker's session, or as the
// worker went) is left as it was.
func answeredRelay(st *state.State, c Context, auto bool) valveAction {
	var r *state.Relay
	var answer string
	for i := len(st.Relay) - 1; i >= 0 && r == nil; i-- {
		if q := &st.Relay[i]; q.Question != "" {
			if a, found := answerTo(c, q.Question); found {
				r, answer = q, a
			}
		}
	}
	if r == nil {
		return valveAction{}
	}
	if r.Answered != nil {
		return valveAction{event: "relay_answered", fields: relayFields(*r, map[string]any{"answer": answer, "by": state.ByHuman, "settled": r.Answered.By}),
			say: "baton: the question of the worker on " + r.Phase + " was settled already — your answer changed nothing", tell: decide.RelaySettled(r.Phase, st.Current)}
	}
	by := state.ByHuman
	if auto && answer == decide.RelayDefault(*r) { // baton only ever types the third option's key
		by = state.ByTimeout
	} else {
		st.Run.HumanAt = c.Now
	}
	state.AnswerRelay(r, by, answer, c.Now)
	v := valveAction{event: "relay_answered", fields: relayFields(*r, map[string]any{"answer": answer, "by": by}),
		tell: decide.RelayPassed(r.Phase, answer, by == state.ByTimeout, st.Current)}
	in := fmt.Sprintf("answer it in its session: baton view %s, in another terminal", r.Phase)
	switch decide.RelayOutcome(*r, answer) {
	case decide.OutcomeGo:
		v.say = "baton: you chose Go ahead — the worker on " + r.Phase + " does it"
		if by == state.ByTimeout {
			notice(st, "proceeded", r.Phase+": "+r.What, c.Now)
			v.say = fmt.Sprintf("baton: nobody answered by %s, so the worker on %s goes ahead — %s", r.Deadline.Local().Format("15:04"), r.Phase, r.What)
		}
	case decide.OutcomeWait:
		v.say = "baton: the worker on " + r.Phase + " waits for you — " + in
		if by == state.ByTimeout {
			v.say = "baton: nobody answered, so the worker on " + r.Phase + " waits for you — " + in
		}
	case decide.OutcomePause:
		if state.Pause(st) == nil {
			v.also = append(v.also, event{"paused", nil})
		}
		v.say = "baton: paused — the human is driving; the worker on " + r.Phase + " waits for you — " + in
		v.tell = decide.RelayPaused()
	case decide.OutcomeOn:
		v.say = "baton: the worker on " + r.Phase + " carries on"
	default:
		v.say = "baton: your answer went to the worker on " + r.Phase
	}
	return v
}

// relayVanished settles the worker's question whose dialog went away unanswered (Esc, a denied or failed
// call, an interrupted turn, a restart): it waits for the human in the worker's own session, as Wait for
// me would have it.
func relayVanished(st *state.State, now time.Time) []event {
	r := st.RelayIssued()
	if r == nil || !r.Asked || st.Run.Dialogs.Has("relay") {
		return nil
	}
	state.AnswerRelay(r, state.ByDeclined, "", now)
	st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: the question of the worker on %s went unanswered — it waits for you in its session: baton view %s, in another terminal", r.Phase, r.Phase))
	return []event{{"relay_answered", relayFields(*r, map[string]any{"by": state.ByDeclined})}}
}

// relayStop is the lead's stop with a worker's question it has not put to the human: the stop is refused
// with the question, MaxStopBlocks times at most. After that the question is left for the human in the
// worker's own session, and they are pushed about it: the lead's run does not stop for a worker's
// question.
func relayStop(st *state.State, d *decision, now time.Time, timeout time.Duration) {
	r, q, fresh := relayToGive(st, now, timeout)
	if fresh {
		e := relayIssued(*r, "stop")
		d.emit(e.kind, e.fields)
	}
	st.Run.StopBlocks++
	if st.Run.StopBlocks <= MaxStopBlocks {
		d.refuse(decide.RelayStopped(r.Phase, q.Call()))
		if fresh {
			d.say(relayComing(r.Phase))
		}
		d.emit("stop_refused", map[string]any{"phase": st.Current, "count": st.Run.StopBlocks, "why": "worker question not asked"})
		return
	}
	st.Run.StopBlocks = 0
	state.AnswerRelay(r, state.ByDeclined, "", now)
	notice(st, "relay", fmt.Sprintf("the worker on %s has a question for you that this session did not put to you: answer it in its session (baton view %s)", r.Phase, r.Phase), now)
	d.emit("relay_answered", relayFields(*r, map[string]any{"by": state.ByDeclined, "why": "not asked"}))
	d.say(fmt.Sprintf("baton: Claude did not put the question of the worker on %s to you — answer it in its session: baton view %s, in another terminal", r.Phase, r.Phase))
}

// answeredHere goes on with a worker's phase that waits for the human, who has now written in the
// worker's own session (baton view): their message answers its block or its review. A proposal held for
// them is released as the lead's is (released).
func answeredHere(st *state.State, phase string) (event, string, bool) {
	var tell string
	switch {
	case st.Blocked != nil:
		tell = decide.WorkerAnsweredHere(state.RelayBlocked, st.Blocked.Reason, phase)
	case st.ReviewDue != "":
		tell = decide.WorkerAnsweredHere(state.RelayReview, "", phase)
	default:
		return event{}, "", false
	}
	was := "blocked"
	if st.Blocked == nil {
		was = "review"
	}
	state.Resume(st)
	return event{"resumed", map[string]any{"by": state.ByHuman, "in": "worker session", "was": was}}, tell, true
}
