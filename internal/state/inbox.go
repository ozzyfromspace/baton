package state

import "time"

// Message is something baton has for the session's model that no hook could tell it when it happened,
// such as why a worker's work could not land. It reaches the model once, with whichever comes first: the
// result of its next tool call, its stop, or the prompt of a line the host types into the idle session.
type Message struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Message kinds.
const (
	MessageLand   = "land"   // a worker's work did not land, and the worker must act on why
	MessageAnswer = "answer" // the human answered a worker's question, which the lead session put to them
)

// Tell leaves m for the session's model.
func Tell(st *State, m Message, now time.Time) {
	m.At = now
	st.Inbox = append(st.Inbox, m)
}

// Mail lists the messages the model should be given now. None is given while a compaction is owed or
// underway: it would be lost in it, so it comes after.
func (st State) Mail() []Message {
	if st.compacting() {
		return nil
	}
	return st.Inbox
}

// TakeMail returns the messages the model should be given now (Mail), and records them as given.
func TakeMail(st *State) []Message {
	mail := st.Mail()
	if len(mail) > 0 {
		st.Inbox = nil
	}
	return mail
}

// compacting reports a compaction owed or underway.
func (st State) compacting() bool {
	return st.BoundaryOwed || st.CheckpointOwed || st.CheckpointDue || st.Run.Compaction.InFlight()
}
