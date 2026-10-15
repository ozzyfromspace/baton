package state

import (
	"testing"
	"time"
)

// Lifting a block clears it with the escalation that put it to the human, and nothing else the human
// has; a block whose question the loop found lapsed is due its Look again until the human writes.
func TestLiftBlock(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name       string
		escalation *Escalation
		review     bool
		lifted     bool
		escKept    bool
	}{
		{"blocked, put to the human", &Escalation{Kind: "blocked", Reason: "blocked on P1: x", Since: t0}, false, true, false},
		{"blocked, not put to them yet", nil, false, true, false},
		{"blocked, behind a review the human has", &Escalation{Kind: "review", Reason: "review P1", Since: t0}, true, true, true},
		{"blocked, behind a stall's escalation: the run moved", &Escalation{Kind: "stalled", Reason: "quiet", Since: t0}, false, true, false},
		{"blocked, with a review due", &Escalation{Kind: "blocked", Reason: "blocked on P1: x", Since: t0}, true, true, false},
		{"not blocked", &Escalation{Kind: "decision", Reason: "d1 waits", Since: t0}, false, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := New()
			st.Mode, st.Current = ModeRunning, "P1"
			if c.lifted {
				SetBlocked(&st, "x", t0)
			}
			st.Run.Escalation = c.escalation
			if c.review {
				DueReview(&st, "P1", t0)
			}
			reason, ok := LiftBlock(&st)
			if ok != c.lifted || ok && reason != "x" || st.Blocked != nil {
				t.Fatalf("lifted %v (%q), blocked %+v", ok, reason, st.Blocked)
			}
			if kept := st.Run.Escalation != nil; kept != c.escKept {
				t.Errorf("escalation %+v", st.Run.Escalation)
			}
			if c.review && st.ReviewDue != "P1" {
				t.Errorf("the review went with it")
			}
		})
	}

	st := New()
	st.Mode = ModeRunning
	SetBlocked(&st, "x", t0)
	if st.LookDue() {
		t.Fatal("due before the loop found it lapsed")
	}
	st.Blocked.Lapsed = t0.Add(30 * time.Minute)
	if !st.LookDue() {
		t.Fatal("not due once the loop found it lapsed")
	}
	st.Run.HumanAt = t0.Add(31 * time.Minute)
	if st.LookDue() {
		t.Fatal("still due after the human wrote")
	}
}
