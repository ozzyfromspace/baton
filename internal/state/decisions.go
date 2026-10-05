package state

import (
	"errors"
	"fmt"
	"time"
)

// A run that may not stop for something it can work around makes decisions the human did not see
// coming. Each one is kept here, with its undo, for the whole run (docs/escalation.md).

// Decision kinds.
const (
	Note     = "note"     // the model took a reversible path, and says so
	Proposal = "proposal" // the model will take one unless the human objects by a deadline
)

// How a proposal was resolved (Resolution.By).
const (
	ByHuman    = "human"    // the human answered it
	ByTimeout  = "timeout"  // nobody answered by the deadline, so baton went ahead
	ByHeld     = "held"     // the human chose Wait for me
	ByDeclined = "declined" // the question went away unanswered (Esc, an interrupted turn); treated as held
	ByPaused   = "paused"   // the human chose Pause baton
)

// Decision is one note or proposal.
type Decision struct {
	ID      string `json:"id"` // d1, d2, … in the order they were made
	Kind    string `json:"kind"`
	Phase   string `json:"phase"`
	What    string `json:"what"`              // what the model did (a note) or will do (a proposal)
	Because string `json:"because,omitempty"` // what went wrong
	Undo    string `json:"undo,omitempty"`    // how to reverse it
	Tried   string `json:"tried,omitempty"`
	// Question is a proposal's question, exactly as baton issued it.
	Question string    `json:"question,omitempty"`
	At       time.Time `json:"at"`
	// Deadline is when baton goes ahead with a proposal nobody answered. Untimed says why a proposal has
	// none: its action looks outward-facing or irreversible, so only the human can let it through.
	Deadline time.Time `json:"deadline,omitzero"`
	Untimed  string    `json:"untimed,omitempty"`
	// Asked: the question reached the human's screen.
	Asked    bool        `json:"asked,omitempty"`
	Resolved *Resolution `json:"resolved,omitempty"`
	// Announced: shown in the session. Told: put in front of the human the next time they typed.
	// Reviewed: the human went over it at a review stop.
	Announced bool `json:"announced,omitempty"`
	Told      bool `json:"told,omitempty"`
	Reviewed  bool `json:"reviewed,omitempty"`
}

// Resolution is how a proposal was settled.
type Resolution struct {
	By     string    `json:"by"`
	Answer string    `json:"answer,omitempty"`
	At     time.Time `json:"at"`
}

// WithoutHuman reports a decision the run made without the human: a note, or a proposal baton went ahead
// with because nobody answered.
func (d Decision) WithoutHuman() bool {
	return d.Kind == Note || d.Kind == Proposal && d.Resolved != nil && d.Resolved.By == ByTimeout
}

// PendingProposal is the proposal still waiting for its answer, or nil. There is at most one.
func (st *State) PendingProposal() *Decision {
	for i := range st.Decisions {
		if d := &st.Decisions[i]; d.Kind == Proposal && d.Resolved == nil {
			return d
		}
	}
	return nil
}

// Unattended counts the decisions phase made without the human that they have not reviewed: what
// max_auto_decisions caps.
func (st State) Unattended(phase string) int {
	n := 0
	for _, d := range st.Decisions {
		if d.Phase == phase && d.WithoutHuman() && !d.Reviewed {
			n++
		}
	}
	return n
}

// AddNote records a note on the current phase.
func AddNote(st *State, what, undo string, now time.Time) (Decision, error) {
	return add(st, Decision{Kind: Note, What: what, Undo: undo}, now)
}

// AddProposal records a proposal on the current phase. It fills in d's ID, kind, phase and time.
func AddProposal(st *State, d Decision, now time.Time) (Decision, error) {
	if p := st.PendingProposal(); p != nil {
		return Decision{}, fmt.Errorf("proposal %s is still waiting for its answer", p.ID)
	}
	d.Kind = Proposal
	return add(st, d, now)
}

func add(st *State, d Decision, now time.Time) (Decision, error) {
	if st.Mode != ModeRunning {
		return Decision{}, fmt.Errorf("baton is not running a plan (mode: %s)", st.Mode)
	}
	if d.What == "" {
		return Decision{}, errors.New("say what you did or will do")
	}
	d.ID, d.Phase, d.At = fmt.Sprintf("d%d", len(st.Decisions)+1), st.Current, now
	st.Decisions = append(st.Decisions, d)
	return d, nil
}

// Resolve records how proposal id was settled.
func Resolve(st *State, id, by, answer string, now time.Time) error {
	for i := range st.Decisions {
		if d := &st.Decisions[i]; d.ID == id && d.Kind == Proposal {
			d.Resolved = &Resolution{By: by, Answer: answer, At: now}
			return nil
		}
	}
	return fmt.Errorf("no proposal %s", id)
}

// DueReview stops the run for the human to review the decisions phase made without them.
func DueReview(st *State, phase string, now time.Time) { st.ReviewDue, st.ReviewAt = phase, now }

// Reviewed records that the human went over the decisions of the phase under review, and lets the run
// make decisions without them again.
func Reviewed(st *State) {
	for i := range st.Decisions {
		if d := &st.Decisions[i]; d.Phase == st.ReviewDue && d.WithoutHuman() {
			d.Reviewed = true
		}
	}
	st.ReviewDue, st.ReviewAt = "", time.Time{}
}

// Held reports a halt only the human may clear (a hard block, a review, a proposal held for them) that
// they have not taken part in since it began. While one holds, the model must not be able to clear it
// with a command of its own: if it could, blocked would stop being a signal anyone can trust.
func (st State) Held() (what string, since time.Time, held bool) {
	if b := st.Blocked; b != nil && !st.Run.HumanAt.After(b.Since) {
		return "blocked: " + b.Reason, b.Since, true
	}
	if st.ReviewDue != "" && !st.Run.HumanAt.After(st.ReviewAt) {
		return "a review of the decisions " + st.ReviewDue + " made without them", st.ReviewAt, true
	}
	if e := st.Run.Escalation; e != nil && humanDecides[e.Kind] && !st.Run.HumanAt.After(e.Since) {
		return e.Reason, e.Since, true
	}
	return "", time.Time{}, false
}
