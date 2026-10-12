package state

import "time"

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
}

// Message kinds.
const (
	MessageLand   = "land"   // a worker's work did not land, and the worker must act on why
	MessageAnswer = "answer" // the human answered a question of baton's, or baton did for them at its deadline
	MessageRewind = "rewind" // the human rewound the conversation past what baton recorded, which stands
)

// Tell leaves m for the session's model.
func Tell(st *State, m Message, now time.Time) {
	m.At = now
	st.Inbox = append(st.Inbox, m)
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
