package state

import "testing"

// A message about one phase is given while the run is on it, and dropped untold once it has moved on to
// another; one about no phase in particular is given whatever the run is on.
func TestMailAboutAPhaseTheRunLeft(t *testing.T) {
	st := New()
	st.Mode, st.Current = ModeRunning, "P2"
	Tell(&st, Message{Kind: MessageRewind, Text: "about P2", Phase: "P2"}, t0)
	Tell(&st, Message{Kind: MessageRewind, Text: "about P1", Phase: "P1"}, t0)
	Tell(&st, Message{Kind: MessageLand, Text: "any"}, t0)
	if got := st.Mail(); len(got) != 2 || got[0].Text != "about P2" || got[1].Text != "any" {
		t.Errorf("mail = %+v", got)
	}
	if got := TakeMail(&st); len(got) != 2 || len(st.Inbox) != 0 {
		t.Errorf("taken %+v, left %+v", got, st.Inbox)
	}
	Tell(&st, Message{Kind: MessageRewind, Text: "about P2", Phase: "P2"}, t0)
	st.Current = "P3"
	if got := TakeMail(&st); len(got) != 0 || len(st.Inbox) != 0 {
		t.Errorf("given about a phase the run left: %+v, left %+v", got, st.Inbox)
	}
	// While a compaction is owed, nothing is given and nothing is dropped.
	Tell(&st, Message{Kind: MessageRewind, Text: "about P2", Phase: "P2"}, t0)
	st.BoundaryOwed = true
	if got := TakeMail(&st); len(got) != 0 || len(st.Inbox) != 1 {
		t.Errorf("with a compaction owed: %+v, left %+v", got, st.Inbox)
	}
}
