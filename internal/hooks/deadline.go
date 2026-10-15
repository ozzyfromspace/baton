package hooks

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Where baton's mod runs, it settles baton's questions at their deadlines with no key typed (spike 34):
// its tool.call hook for AskUserQuestion asks the host how long a question may wait (`baton mod report`,
// Bound), and at the deadline answers the call itself with the answer the host gives it, which closes the
// dialog; then it reports that it did (Settle). Claude Code fires no PostToolUse for a call answered so,
// so Settle records the answer as the call's result would have. Where the mod does not run (serial mode),
// the host types the default's key instead, as v0.7 did (loop.answerOwnQuestion).

// A Wait is how long a question of baton's may wait for the human, and what baton answers then: Answer ""
// means baton never answers it (it is not baton's, or only the human can answer it); For 0 with an
// Answer means now.
type Wait struct {
	For    time.Duration
	Answer string
}

// Bound is how long question may wait, put to the human in the lead's session of s at asked (when the
// mod's hook first saw the call), as the lead's state stands at now: a timed proposal of the lead's own
// goes ahead at its deadline, the context question is answered Keep going after warnTimeout, and a phase
// agent's question gets its default at its deadline (decide.RelayDefault). None is answered sooner than
// decide.MinWait after it was on screen: a question of the lead's model may be queued behind another
// dialog, so its clock runs from when it was first seen in front (Dialog.Front, recorded here); one of
// the mod's is asked only while no other dialog is open (the band it is asked from is not drawn then).
func Bound(s *state.Store, question string, asked, now time.Time, warnTimeout, escTimeout time.Duration) (Wait, error) {
	var w Wait
	_, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, now) {
			return errNotOwner
		}
		w = bound(st, question, asked, now, warnTimeout, escTimeout)
		return nil
	})
	return w, err
}

func bound(st *state.State, question string, asked, now time.Time, warnTimeout, escTimeout time.Duration) Wait {
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
	if !ok {
		return Wait{}
	}
	var due time.Time
	var answer string
	switch iq.kind {
	case "proposal":
		p := st.OwnProposal()
		if p == nil || p.Untimed != "" || p.Deadline.IsZero() {
			return Wait{}
		}
		shown, front := shownSince(st, question, now)
		due, answer = latest(p.Deadline, shown.Add(decide.MinWait)), decide.ProposalOptions[2].Label
		if !front {
			return Wait{For: min(max(due.Sub(now), time.Second), recheck), Answer: answer}
		}
	case "context_warning":
		shown, front := shownSince(st, question, now)
		due, answer = shown.Add(max(warnTimeout, decide.MinWait)), decide.WarnOptions[2].Label
		if !front {
			return Wait{For: min(max(due.Sub(now), time.Second), recheck), Answer: answer}
		}
	case "escalation", "replan":
		// baton's own question about the run: the plan goes on (Continue), or a plan the human approved
		// mid-run gets its default (decide.ReplanDefault). A block is the model's own choice to wait, and is
		// never answered for the human.
		answer = "Continue"
		if iq.kind == "replan" {
			answer = decide.ReplanDefault(st.Replan.Same)
		} else if e := st.Run.Escalation; e == nil || e.Kind == "blocked" {
			return Wait{}
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

// recheck bounds how long the mod waits before it asks about a question again: what baton answers may
// change meanwhile (the question settled elsewhere, the plan paused).
const recheck = 30 * time.Second

// shownSince is since when question, one of the lead model's, has been in front of the human: the first
// time baton saw its dialog at the front of the queue, recorded now if this is that time. Not in front
// (or not on record yet), it reports now and false.
func shownSince(st *state.State, question string, now time.Time) (time.Time, bool) {
	q := st.Run.Dialogs
	if len(q) == 0 || q[0].Agent != "" || q[0].Tool != "AskUserQuestion" || q[0].Key != decide.Normalize(question) {
		return now, false
	}
	if q[0].Front.IsZero() {
		q[0].Front = now
	}
	return q[0].Front, true
}

// Settle records that baton's mod answered question with answer at its deadline, in the lead's session
// of s, as the call's result would have: its dialog is closed, and a question of the lead's own is
// answered by baton (a proposal goes ahead, the context question keeps going, the run goes on, a plan
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
		case iq.kind == "replan":
			v = answeredReplan(st, s, c, w, true)
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
