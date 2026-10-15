package state

import (
	"slices"
	"time"
)

// A phase agent never puts a question to the human itself. Its proposal, its block, or a review of its
// decisions joins the run's relay queue (Store.Relay), and baton's mod asks the human in Claude Code's own
// dialog in the lead's terminal, one question at a time, and only while nothing of the lead's own waits on
// them (OwnQuestion). The host issues the front question to the mod (IssueRelay), which settles it at its
// deadline if nobody answers; the answer goes back to the agent's sub-run as its own answer would have
// (docs/research/visible-workers.md). No model takes part in any of it.

// Relay is a question a worker's phase has for the human.
type Relay struct {
	Phase string `json:"phase"`
	// Kind is a proposal, the phase's block, or a review of its decisions; Decision is the sub-run's
	// proposal it asks about.
	Kind     string `json:"kind"`
	Decision string `json:"decision,omitempty"`
	// Since is when the block or the review it asks about began. With Decision, it tells one question of a
	// phase from the next: each is asked once.
	Since time.Time `json:"since,omitzero"`
	// What the question is about, as the worker's sub-run had it when it was queued: a proposal's action
	// (What), what went wrong (Because) and why its action looks outward-facing or irreversible (Outward,
	// answered Not now at its deadline, under the key it had when such a proposal had no deadline); a
	// block's reason (Because); how many decisions a review covers (Count).
	What    string `json:"what,omitempty"`
	Because string `json:"because,omitempty"`
	Outward string `json:"untimed,omitempty"`
	Count   int    `json:"count,omitempty"`
	// Wait is how long a proposal waits for the human, from when it was queued: the agent's own --wait,
	// or baton's default (0 in a question queued before v0.8.3: the escalation timeout).
	Wait time.Duration `json:"wait,omitempty"`
	// Question is the question exactly as the human is asked it, written when it is issued.
	Question string    `json:"question,omitempty"`
	Queued   time.Time `json:"queued"`
	// Issued is when the host had the mod ask it (command Ask, of host instance Instance), and Deadline
	// when baton answers it if nobody has: a proposal gets its default then (the time its question names:
	// Go ahead, or Not now for an outward one), a block is lifted for the agent to look again, and
	// anything else waits for the human, who has it again with /baton run. Asked: it reached Claude Code's
	// dialog queue, at AskedAt.
	Issued   time.Time   `json:"issued,omitzero"`
	Ask      int         `json:"ask,omitempty"`
	Instance string      `json:"instance,omitempty"`
	Deadline time.Time   `json:"deadline,omitzero"`
	Asked    bool        `json:"asked,omitempty"`
	AskedAt  time.Time   `json:"asked_at,omitzero"`
	Answered *Resolution `json:"answered,omitempty"`
	// Dismissed is when the dialog of a proposal, or of a block, went away unanswered: it is not asked
	// again, and gets its default at its deadline.
	Dismissed time.Time `json:"dismissed,omitzero"`
	// Applied is when the answer reached the worker's sub-run.
	Applied time.Time `json:"applied,omitzero"`
	// Lapsed is when the coordinator was done with the hold an answer with no way on (Wait for me, Pause
	// baton, none) put on a proposal's phase, or a block's: it lifted it, as nobody had answered for loop's
	// Timing.HoldLapse (a block's, Timing.BlockLook) since, or found it settled already.
	Lapsed time.Time `json:"lapsed,omitzero"`
}

// Relay kinds.
const (
	RelayProposal = "proposal"
	RelayBlocked  = "blocked"
	RelayReview   = "review"
)

// relayWhat names each kind of question for the human.
var relayWhat = map[string]string{RelayProposal: "a proposal", RelayBlocked: "it is blocked", RelayReview: "a review of its decisions"}

// ByWithdrawn settles a phase agent's question that no longer needs the human's answer: it was settled
// in its sub-run meanwhile, or the agent is gone.
const ByWithdrawn = "withdrawn"

// Same reports the same question: the same phase's question of the same kind, about the same proposal,
// block or review.
func (r Relay) Same(o Relay) bool {
	return r.Phase == o.Phase && r.Kind == o.Kind && r.Decision == o.Decision && r.Since.Equal(o.Since)
}

// Key names the question r is, the same each time it is asked (AskAgain).
func (r Relay) Key() string {
	return r.Phase + " " + r.Kind + " " + r.Decision + " " + r.Since.UTC().Format(time.RFC3339Nano)
}

// Timed reports a proposal, which gets its default at the time its question names if nobody answers it:
// Go ahead, or Not now for an outward one. Anything else waits for the human after its deadline.
func (r Relay) Timed() bool { return r.Kind == RelayProposal }

// Due is when proposal r gets its default if nobody answers it: its wait after it was queued, so that a
// question the human never got to see (the lead's own went first) runs out all the same. fallback is the
// wait of one queued before v0.8.3, which recorded none.
func (r Relay) Due(fallback time.Duration) time.Time {
	wait := r.Wait
	if wait <= 0 {
		wait = fallback
	}
	t := r.Queued.Add(wait)
	if c := t.Truncate(time.Minute); c.Before(t) {
		t = c.Add(time.Minute) // its question names it as HH:MM: never sooner
	}
	return t
}

// QueueRelay queues r, a question of r.Phase's agent for the human, unless it was queued already: the
// agent's commands and hooks may each queue the same question, and each must be able to retry. A question
// answered once is asked again only when the human asks for it (AskAgain). It reports whether r joined
// the queue.
func QueueRelay(st *State, r Relay, now time.Time) bool {
	if st.RelayOf(r) != nil {
		return false
	}
	st.Relay = append(st.Relay, fresh(r, now))
	return true
}

// fresh is r as it joins the queue at now: asked of nobody yet.
func fresh(r Relay, now time.Time) Relay {
	r.Queued, r.Question, r.Issued, r.Ask, r.Instance, r.Deadline = now, "", time.Time{}, 0, "", time.Time{}
	r.Asked, r.AskedAt, r.Answered, r.Applied, r.Dismissed, r.Lapsed = false, time.Time{}, nil, time.Time{}, time.Time{}, time.Time{}
	return r
}

// AskAgain puts r, a question answered and applied already whose phase still waits on the human (they
// chose Wait for me, nobody answered it, or it went away unanswered), back at the end of the queue, to be
// asked again: /baton run does it. It reports whether it did.
func AskAgain(st *State, r Relay, now time.Time) bool {
	i := slices.IndexFunc(st.Relay, func(q Relay) bool { return q.Same(r) })
	if i < 0 || st.Relay[i].Answered == nil || st.Relay[i].Applied.IsZero() {
		return false
	}
	q := fresh(st.Relay[i], now)
	st.Relay = append(slices.Delete(st.Relay, i, i+1), q)
	return true
}

// FrontRelay is the question at the front of the relay queue, the first one unanswered, or nil. Only it
// is ever put to the human.
func (st *State) FrontRelay() *Relay {
	for i := range st.Relay {
		if r := &st.Relay[i]; r.Answered == nil {
			return r
		}
	}
	return nil
}

// RelayIssued is the phase agent's question the mod was told to ask and the human has not answered, or
// nil.
func (st *State) RelayIssued() *Relay {
	if r := st.FrontRelay(); r != nil && !r.Issued.IsZero() {
		return r
	}
	return nil
}

// RelayDue is the phase agent's question the mod is to be told to ask now, or nil: the front one, not
// issued yet, while the plan runs and nothing of the lead's own waits on the human, which goes first.
func (st *State) RelayDue() *Relay {
	if r := st.FrontRelay(); r != nil && r.Issued.IsZero() && st.Mode == ModeRunning && !st.OwnQuestion() {
		return r
	}
	return nil
}

// OwnQuestion reports something of the lead session's own that the human is asked, or is to be asked:
// an escalation, a block, its own proposal, a review of its decisions, the context question, or a plan
// they approved mid-run.
func (st *State) OwnQuestion() bool {
	return st.Run.Escalation != nil || st.Blocked != nil || st.OwnProposal() != nil || st.ReviewDue != "" || st.Run.WarnQuestion != "" || st.Replan != nil
}

// IssueRelay records that r was issued to the mod by host instance as command ask, to put to the human as
// question; deadline is when baton answers it if nobody has.
func IssueRelay(r *Relay, question string, ask int, instance string, deadline, now time.Time) {
	r.Question, r.Issued, r.Ask, r.Instance, r.Deadline, r.Asked, r.AskedAt = question, now, ask, instance, deadline, false, time.Time{}
}

// AnswerRelay settles r: by is who answered (ByHuman, ByTimeout: the host, as nobody did; ByDeclined:
// the question went away unanswered; ByWithdrawn), and answer what.
func AnswerRelay(r *Relay, by, answer string, now time.Time) {
	r.Answered = &Resolution{By: by, Answer: answer, At: now}
}

// Waits reports phase agent question r still waiting for its answer in the agent's sub-run st: its
// proposal unresolved, or its block or its review still the one that began when r asked about it.
func (st *State) Waits(r Relay) bool {
	switch r.Kind {
	case RelayProposal:
		p := st.PendingProposal()
		return p != nil && p.ID == r.Decision
	case RelayBlocked:
		return st.Blocked != nil && st.Blocked.Since.Equal(r.Since)
	case RelayReview:
		return st.ReviewDue != "" && st.ReviewAt.Equal(r.Since)
	}
	return false
}

// RelayOf finds the queue's item r is, or nil.
func (st *State) RelayOf(r Relay) *Relay {
	for i := range st.Relay {
		if q := &st.Relay[i]; q.Same(r) {
			return q
		}
	}
	return nil
}

// Unapplied lists the answered questions whose answers have not reached their agents' sub-runs.
func (st State) Unapplied() []Relay {
	var out []Relay
	for _, r := range st.Relay {
		if r.Answered != nil && r.Applied.IsZero() {
			out = append(out, r)
		}
	}
	return out
}
