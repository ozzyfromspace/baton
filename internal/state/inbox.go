package state

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"
)

// Message is something baton has for the session's model that no hook could tell it when it happened,
// such as why a worker's work could not land. It reaches the model once, with whichever comes first: the
// result of its next tool call, its stop, or the prompt of a line the host types into the idle session.
type Message struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// Phase is the phase it is about, if only that one: once the run has moved on to another, it is
	// dropped untold.
	Phase string `json:"phase,omitempty"`
	// About names what it answers (a question's Relay.Key): a newer message about it replaces one not
	// given yet, which would only contradict it.
	About string `json:"about,omitempty"`
}

// Message kinds.
const (
	MessageLand   = "land"   // a worker's work did not land, and the worker must act on why
	MessageAnswer = "answer" // the human answered a question of baton's, or baton did for them at its deadline
	MessageRewind = "rewind" // the human rewound the conversation past what baton recorded, which stands
	MessagePlan   = "plan"   // the human changed the plan, and a phase agent's part of it reads differently
	MessageCommit = "commit" // the lead's uncommitted changes have held a phase's landing long: it is to commit them
	MessageWords  = "words"  // the human's words for a phase's agent reached none, and are the lead's (HumanWords)
)

// PlanText is what a worker's agent was given of the plan, as hashes (TextSum): the standing rules, and
// its phase's section. It is kept in the worker's sub-run, from when it was made and each time its agent
// is told of a change (loop's replan), so that a change is told once.
type PlanText struct {
	Rules   string `json:"rules,omitempty"`
	Section string `json:"section,omitempty"`
}

// TextSum is the hash PlanText keeps of text: "" for none.
func TextSum(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:16])
}

// Tell leaves m for the session's model, in place of a message about the same thing not given yet.
func Tell(st *State, m Message, now time.Time) {
	m.At = now
	if m.About != "" {
		st.Inbox = slices.DeleteFunc(st.Inbox, func(o Message) bool { return o.About == m.About })
	}
	st.Inbox = append(st.Inbox, m)
}

// DropMail drops the messages of kind not given yet: a fresh agent's brief says what they would have.
func DropMail(st *State, kind string) {
	st.Inbox = slices.DeleteFunc(st.Inbox, func(m Message) bool { return m.Kind == kind })
}

// Mail lists the messages the model should be given now. None is given while a compaction is owed or
// underway: it would be lost in it, so it comes after. One about a phase the run is no longer on is not
// given at all.
func (st State) Mail() []Message {
	if st.Compacting() {
		return nil
	}
	var mail []Message
	for _, m := range st.Inbox {
		if m.Phase == "" || m.Phase == st.Current {
			mail = append(mail, m)
		}
	}
	return mail
}

// TakeMail returns the messages the model should be given now (Mail), and records them as given. Those
// about a phase the run is no longer on are dropped.
func TakeMail(st *State) []Message {
	if st.Compacting() {
		return nil
	}
	mail := st.Mail()
	st.Inbox = nil
	return mail
}

// Compacting reports a compaction owed or underway.
func (st State) Compacting() bool {
	return st.BoundaryOwed || st.CheckpointOwed || st.CheckpointDue || st.Run.Compaction.InFlight()
}
