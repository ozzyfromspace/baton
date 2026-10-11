package state

import "time"

// A worker never puts a question to the human itself: nobody watches its session. Its proposal, its
// block, or a review of its decisions joins the run's relay queue (Store.Relay), and the lead session
// puts the questions to the human in their terminal, one at a time, and only while nothing of the lead's
// own waits on them (OwnQuestion). The lead's model is given the front question (IssueRelay) at its next
// tool call, at its stop, or with a line the host types into the idle session; the host answers it by
// itself if nobody does; and the answer goes back to the worker's sub-run as its own answer would have
// (docs/research/workers.md).

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
	// (What), what went wrong (Because) and why it has no deadline (Untimed); a block's reason (Because);
	// how many decisions a review covers (Count).
	What    string `json:"what,omitempty"`
	Because string `json:"because,omitempty"`
	Untimed string `json:"untimed,omitempty"`
	Count   int    `json:"count,omitempty"`
	// Question is the question exactly as the lead is to ask it, written when the lead is given it.
	Question string    `json:"question,omitempty"`
	Queued   time.Time `json:"queued"`
	// Issued is when the lead's model was given the question, and Deadline when the host answers it if
	// nobody has: a timed proposal goes ahead then (the time its question names), and anything else waits
	// for the human in the worker's own session. Asked: it reached the human's screen.
	Issued   time.Time   `json:"issued,omitzero"`
	Deadline time.Time   `json:"deadline,omitzero"`
	Asked    bool        `json:"asked,omitempty"`
	Answered *Resolution `json:"answered,omitempty"`
	// Applied is when the answer reached the worker's sub-run.
	Applied time.Time `json:"applied,omitzero"`
}

// Relay kinds.
const (
	RelayProposal = "proposal"
	RelayBlocked  = "blocked"
	RelayReview   = "review"
)

// relayWhat names each kind of question for the human.
var relayWhat = map[string]string{RelayProposal: "a proposal", RelayBlocked: "it is blocked", RelayReview: "a review of its decisions"}

// ByWithdrawn settles a worker's question that no longer needs the human's answer: it was settled in the
// worker's own session, or the worker is gone.
const ByWithdrawn = "withdrawn"

// Same reports the same question: the same phase's question of the same kind, about the same proposal,
// block or review.
func (r Relay) Same(o Relay) bool {
	return r.Phase == o.Phase && r.Kind == o.Kind && r.Decision == o.Decision && r.Since.Equal(o.Since)
}

// Timed reports a proposal that goes ahead if nobody answers it.
func (r Relay) Timed() bool { return r.Kind == RelayProposal && r.Untimed == "" }

// QueueRelay queues r, a question of r.Phase's worker for the human, unless it was queued already: the
// worker's commands and hooks may each queue the same question, and each must be able to retry. A
// question answered once is never asked again, even if its answer was to leave it for the human in the
// worker's own session. It reports whether r joined the queue.
func QueueRelay(st *State, r Relay, now time.Time) bool {
	if st.RelayOf(r) != nil {
		return false
	}
	r.Queued, r.Question, r.Issued, r.Deadline, r.Asked, r.Answered, r.Applied = now, "", time.Time{}, time.Time{}, false, nil, time.Time{}
	st.Relay = append(st.Relay, r)
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

// RelayIssued is the worker's question the lead's model was given and the human has not answered, or nil.
func (st *State) RelayIssued() *Relay {
	if r := st.FrontRelay(); r != nil && !r.Issued.IsZero() {
		return r
	}
	return nil
}

// RelayUnasked is the worker's question the lead's model was given and has not put to the human yet, or
// nil.
func (st *State) RelayUnasked() *Relay {
	if r := st.RelayIssued(); r != nil && !r.Asked {
		return r
	}
	return nil
}

// RelayDue is the worker's question the lead's model is to be given now, or nil: the front one, not given
// yet, while the lead may ask (relayAskable).
func (st *State) RelayDue() *Relay {
	if r := st.FrontRelay(); r != nil && r.Issued.IsZero() && st.relayAskable() {
		return r
	}
	return nil
}

// RelayToAsk is the worker's question the lead is to put to the human now, given to its model already or
// not, or nil: the front one, not asked yet, while the lead may ask (relayAskable).
func (st *State) RelayToAsk() *Relay {
	if r := st.FrontRelay(); r != nil && !r.Asked && st.relayAskable() {
		return r
	}
	return nil
}

// relayAskable reports a lead that may put a worker's question to the human: the plan runs, no
// compaction is owed or underway (the question would be lost in it), and nothing of the lead's own
// waits on the human, which goes first.
func (st *State) relayAskable() bool {
	return st.Mode == ModeRunning && !st.compacting() && !st.OwnQuestion()
}

// OwnQuestion reports something of the lead session's own that the human is asked, or is to be asked:
// an escalation, a block, its own proposal, a review of its decisions, the context question, or a plan
// they approved mid-run.
func (st *State) OwnQuestion() bool {
	return st.Run.Escalation != nil || st.Blocked != nil || st.OwnProposal() != nil || st.ReviewDue != "" || st.Run.WarnQuestion != "" || st.Replan != nil
}

// IssueRelay records that r was given to the lead's model, to put to the human as question; deadline is
// when the host answers it if nobody has.
func IssueRelay(r *Relay, question string, deadline, now time.Time) {
	r.Question, r.Issued, r.Deadline, r.Asked = question, now, deadline, false
}

// AnswerRelay settles r: by is who answered (ByHuman, ByTimeout: the host, as nobody did; ByDeclined:
// the question went away unanswered; ByWithdrawn), and answer what.
func AnswerRelay(r *Relay, by, answer string, now time.Time) {
	r.Answered = &Resolution{By: by, Answer: answer, At: now}
}

// Waits reports worker question r still waiting for its answer in the worker's sub-run st: its proposal
// unresolved, or its block or its review still the one that began when r asked about it.
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

// Unapplied lists the answered questions whose answers have not reached their workers' sub-runs.
func (st State) Unapplied() []Relay {
	var out []Relay
	for _, r := range st.Relay {
		if r.Answered != nil && r.Applied.IsZero() {
			out = append(out, r)
		}
	}
	return out
}
