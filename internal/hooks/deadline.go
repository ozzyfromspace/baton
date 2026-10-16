package hooks

import (
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Where baton's mod runs, it settles baton's questions at their deadlines with no key typed (spike 34):
// its tool.call hook for AskUserQuestion asks the host how long a question may wait (`baton mod report`,
// Bound), and at the deadline answers the call itself with the answer the host gives it, which closes the
// dialog; then it reports that it did (Settle). Claude Code fires no PostToolUse for a call answered so,
// so Settle records the answer as the call's result would have. Where the mod does not run (serial mode),
// the host types the default's key instead, as v0.7 did (loop.answerOwnQuestion). The lead model's own
// questions get a deadline the same way where the mod runs (SettleOwn); in serial mode they stay the
// human's, since no key typed could pick a given option safely.

// A Wait is how long a question may wait for the human, and what baton answers then: one of baton's gets
// Answer, and a call of the lead model's own gets Answers, one per question, by its text as asked. Neither
// means baton never answers it (only the human can, or no plan runs); For 0 with an answer means now.
type Wait struct {
	For     time.Duration
	Answer  string
	Answers map[string]string
}

// Bound is how long call (an AskUserQuestion call's questions, in order) may wait, put to the human in
// the lead's session of s at asked (when the mod's hook first saw the call), as the lead's state stands
// at now: a proposal of the lead's own goes ahead at its deadline, or is answered Not now then if it is
// outward, the context question is answered Keep going after warnTimeout, baton's question about the run
// (or about a plan approved mid-run) gets its default after escTimeout, a block is answered Look again
// once the host's loop found nobody answered it in time, and a phase agent's question gets its default at
// its deadline (decide.RelayDefault). A call of the lead model's own, while the plan runs, gets an answer
// for each of its questions after escTimeout (decide.OwnAnswer): nothing waits on the human for it any
// longer. None is answered sooner than decide.MinWait after it was on screen: a question of the lead's
// model may be queued behind another dialog, so its clock runs from when it was first seen in front
// (Dialog.Front, recorded here); one of the mod's is asked only while no other dialog is open (the band
// it is asked from is not drawn then).
func Bound(s *state.Store, call []bridge.Asked, asked, now time.Time, warnTimeout, escTimeout time.Duration) (Wait, error) {
	var w Wait
	_, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, now) {
			return errNotOwner
		}
		w = bound(st, call, asked, now, warnTimeout, escTimeout)
		return nil
	})
	return w, err
}

func bound(st *state.State, call []bridge.Asked, asked, now time.Time, warnTimeout, escTimeout time.Duration) Wait {
	if len(call) == 0 {
		return Wait{}
	}
	question := call[0].Question
	for _, r := range st.Relay {
		if r.Answered != nil && r.Question != "" && decide.Normalize(r.Question) == decide.Normalize(question) {
			// A phase agent's question settled meanwhile (withdrawn, or taken past its deadline) whose dialog
			// is still open: no answer changes anything now, and it goes.
			return Wait{Answer: decide.RelayDefault(r)}
		}
	}
	if st.Mode != state.ModeRunning {
		return Wait{}
	}
	iq, ok := lookupIssued(st, question)
	if !ok || len(call) != 1 {
		return ownCall(st, call, now, escTimeout)
	}
	var due time.Time
	var answer string
	switch iq.kind {
	case "proposal":
		p := st.OwnProposal()
		if p == nil || p.Deadline.IsZero() {
			return Wait{}
		}
		shown, front := shownSince(st, question, now)
		due, answer = latest(p.Deadline, shown.Add(decide.MinWait)), p.Default()
		if !front {
			return Wait{For: min(max(due.Sub(now), time.Second), recheck), Answer: answer}
		}
	case "context_warning":
		shown, front := shownSince(st, question, now)
		due, answer = shown.Add(max(warnTimeout, decide.MinWait)), decide.WarnOptions[2].Label
		if !front {
			return Wait{For: min(max(due.Sub(now), time.Second), recheck), Answer: answer}
		}
	case "escalation", "plan_question":
		// baton's own question about the run: the plan goes on (Continue), or a plan the human approved
		// mid-run gets its default (decide.PlanQuestionDefault). A block is the model's own choice to wait, and is
		// never answered Continue for the human: once the host's loop finds nobody has answered it for its
		// BlockLook (State.LookDue), it is answered Look again, and until then the mod asks again.
		answer = "Continue"
		e := st.Run.Escalation
		switch {
		case iq.kind == "plan_question":
			answer = decide.PlanQuestionDefault(st.PlanQuestion.Same)
		case e == nil:
			return Wait{}
		case e.Kind == "blocked":
			shown, front := shownSince(st, question, now)
			if !st.LookDue() {
				return Wait{For: recheck, Answer: decide.LookAgain}
			}
			due = latest(st.Blocked.Lapsed, shown.Add(decide.MinWait))
			if !front {
				return Wait{For: min(max(due.Sub(now), time.Second), recheck), Answer: decide.LookAgain}
			}
			return Wait{For: min(max(due.Sub(now), 0), recheck), Answer: decide.LookAgain}
		}
		shown, front := shownSince(st, question, now)
		due = shown.Add(max(escTimeout, decide.MinWait))
		if !front {
			return Wait{For: min(max(due.Sub(now), time.Second), recheck), Answer: answer}
		}
	case "relay":
		r := st.RelayIssued()
		if r == nil || r.Deadline.IsZero() {
			return Wait{}
		}
		due, answer = latest(r.Deadline, asked.Add(decide.MinWait)), decide.RelayDefault(*r)
	default:
		return Wait{}
	}
	return Wait{For: min(max(due.Sub(now), 0), recheck), Answer: answer}
}

// ownCall is how long a call of the lead model's own (not baton's) may wait for the human while the plan
// runs, and what baton answers each of its questions then: escTimeout after it was first in front, as for
// baton's question about the run, its recommended option or else its first (decide.OwnAnswer). A call
// with a question that offers no option is the human's alone.
func ownCall(st *state.State, call []bridge.Asked, now time.Time, escTimeout time.Duration) Wait {
	answers := make(map[string]string, len(call))
	for _, q := range call {
		a := decide.OwnAnswer(q.Question, q.Options)
		if a == "" {
			return Wait{}
		}
		answers[q.Question] = a
	}
	shown, front := shownKey(st, callKey(call), now)
	due := shown.Add(max(escTimeout, decide.MinWait))
	if !front {
		return Wait{For: min(max(due.Sub(now), time.Second), recheck), Answers: answers}
	}
	return Wait{For: min(max(due.Sub(now), 0), recheck), Answers: answers}
}

// callKey is the key of the dialog of an AskUserQuestion call with these questions (dialogKey).
func callKey(call []bridge.Asked) string {
	texts := make([]string, len(call))
	for i, q := range call {
		texts[i] = q.Question
	}
	return questionsKey(texts)
}

// recheck bounds how long the mod waits before it asks about a question again: what baton answers may
// change meanwhile (the question settled elsewhere, the plan paused).
const recheck = 30 * time.Second

// shownSince is since when question, one of the lead model's, has been in front of the human: the first
// time baton saw its dialog at the front of the queue, recorded now if this is that time. Not in front
// (or not on record yet), it reports now and false.
func shownSince(st *state.State, question string, now time.Time) (time.Time, bool) {
	return shownKey(st, decide.Normalize(question), now)
}

// shownKey is shownSince for the AskUserQuestion call whose dialog key is key.
func shownKey(st *state.State, key string, now time.Time) (time.Time, bool) {
	q := st.Run.Dialogs
	if len(q) == 0 || q[0].Agent != "" || q[0].Tool != "AskUserQuestion" || q[0].Key != key {
		return now, false
	}
	if q[0].Front.IsZero() {
		q[0].Front = now
	}
	return q[0].Front, true
}

// Settle records that baton's mod answered question with answer at its deadline, in the lead's session
// of s, as the call's result would have: its dialog is closed, and a question of the lead's own is
// answered by baton (a proposal goes ahead or, outward, is answered Not now, the context question keeps going, the run goes on, a plan
// approved mid-run gets its default), which the model is told at its next tool call or stop. A phase
// agent's question is the coordinator's to settle, from the mod's report of its answer. It reports
// whether the question was baton's.
func Settle(s *state.Store, question, answer string, now time.Time, env func(string) string) (bool, error) {
	return settle(s, question, answer, now, env, "mod")
}

// Lapse is Settle for a question of the lead's own that reached its deadline with no dialog of it on
// screen (the model never asked it, or it went away): the host's loop answers it so, and nothing waits on
// the human for it.
func Lapse(s *state.Store, question, answer string, now time.Time, env func(string) string) (bool, error) {
	return settle(s, question, answer, now, env, "lapsed")
}

// SettleOwn records that baton's mod answered a call of the lead model's own (its questions, in order) at
// its deadline with answers (Bound), in the lead's session of s: its dialog is closed, the human is told
// baton chose for them (and pushed), and so is the model, at its next tool call or stop. Each call once:
// one whose dialog is no longer on record is left as it is.
func SettleOwn(s *state.Store, call []bridge.Asked, answers map[string]string, now time.Time) error {
	var qas []decide.QA
	var after time.Duration
	_, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, now) {
			return errNotOwner
		}
		d, closed := st.Run.Dialogs.CloseExact("", "AskUserQuestion", callKey(call))
		if !closed {
			return nil
		}
		for _, q := range call {
			qas = append(qas, decide.QA{Question: q.Question, Answer: answers[q.Question]})
		}
		after = now.Sub(latest(d.Since, d.Front)).Round(time.Second)
		say, tell := decide.AnsweredOwn(qas, now)
		st.Run.Announce = append(st.Run.Announce, say)
		notice(st, "answered", strings.TrimPrefix(say, "baton: "), now)
		state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: tell}, now)
		return nil
	})
	if err != nil || len(qas) == 0 {
		return err
	}
	s.Event("question_answered", map[string]any{"by": state.ByTimeout, "via": "mod", "own": true, "questions": len(qas),
		"question": qas[0].Question, "answer": qas[0].Answer, "after": after.String()})
	return nil
}

func settle(s *state.Store, question, answer string, now time.Time, env func(string) string, via string) (bool, error) {
	c := Context{Event: "PostToolUse", Now: now, Env: env, Input: map[string]any{"tool_name": "AskUserQuestion",
		"tool_response": map[string]any{"answers": map[string]any{question: answer}}}}
	w := words(c, s)
	var v valveAction
	var own bool
	_, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, now) {
			return errNotOwner
		}
		st.Run.Dialogs.CloseExact("", "AskUserQuestion", decide.Normalize(question))
		var iq issued
		if iq, own = lookupIssued(st, question); !own || iq.kind == "relay" {
			return nil
		}
		switch {
		case iq.kind == "plan_question":
			v = answeredPlanQuestion(st, s, c, w, true)
		default:
			if v = answeredProposal(st, c, true); v.event == "" {
				v = answered(st, c, true, w)
			}
		}
		if v.say != "" {
			st.Run.Announce = append(st.Run.Announce, v.say)
		}
		if v.tell != "" {
			state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: v.tell}, now)
		}
		return nil
	})
	if err != nil || v.event == "" {
		return own, err
	}
	fields := map[string]any{"via": via}
	for k, x := range v.fields {
		fields[k] = x
	}
	s.Event(v.event, fields)
	for _, e := range v.also {
		s.Event(e.kind, e.fields)
	}
	return own, nil
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}
