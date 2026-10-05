package state

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

func running(t *testing.T) State {
	t.Helper()
	pl := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0"}, {ID: "P1"}}}
	return Attach(pl, t0, Origin{})
}

func TestDecisionsAreRecordedAndCounted(t *testing.T) {
	st := running(t)
	n1, err := AddNote(&st, "committed unsigned", "git commit --amend --no-edit -S", t0)
	if err != nil || n1.ID != "d1" || n1.Kind != Note || n1.Phase != "P0" || !n1.At.Equal(t0) {
		t.Fatalf("note: %+v %v", n1, err)
	}
	p, err := AddProposal(&st, Decision{What: "skip the flaky test", Because: "it times out", Undo: "re-enable it"}, t0)
	if err != nil || p.ID != "d2" || p.Kind != Proposal || st.PendingProposal() == nil || st.PendingProposal().ID != "d2" {
		t.Fatalf("proposal: %+v %v", p, err)
	}
	if _, err := AddProposal(&st, Decision{What: "another"}, t0); err == nil {
		t.Error("a second proposal while one is pending")
	}
	// A pending proposal was not made without the human; one that went ahead on the timer was.
	if n := st.Unattended("P0"); n != 1 {
		t.Fatalf("unattended %d, want 1", n)
	}
	if err := Resolve(&st, "d2", ByTimeout, "Go ahead", t0.Add(5*time.Minute)); err != nil || st.PendingProposal() != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n := st.Unattended("P0"); n != 2 {
		t.Fatalf("unattended %d, want 2", n)
	}
	AddProposal(&st, Decision{What: "use the staging key"}, t0)
	Resolve(&st, "d3", ByHuman, "Go ahead", t0)
	st.Decisions[0].Reviewed = true
	if n := st.Unattended("P0"); n != 1 {
		t.Fatalf("a human-approved or reviewed decision was counted: %d", n)
	}
	if n := st.Unattended("P1"); n != 0 {
		t.Fatalf("P1: %d", n)
	}
	if err := Resolve(&st, "d1", ByHuman, "", t0); err == nil {
		t.Error("resolved a note")
	}

	// A new plan starts with none, and no review due.
	st.ReviewDue = "P0"
	st = Reattach(st, plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "Q0"}}}, t0, Origin{})
	if len(st.Decisions) != 0 || st.ReviewDue != "" {
		t.Fatalf("reattach kept %+v %q", st.Decisions, st.ReviewDue)
	}
}

func TestDecisionsNeedARunningPlan(t *testing.T) {
	idle := New()
	if _, err := AddNote(&idle, "x", "", t0); err == nil {
		t.Error("note with no plan")
	}
	st := running(t)
	if _, err := AddNote(&st, "", "", t0); err == nil {
		t.Error("an empty note")
	}
	Pause(&st)
	if _, err := AddProposal(&st, Decision{What: "x"}, t0); err == nil {
		t.Error("proposal while paused")
	}
}

// Only the human can clear a hard block, a held proposal or a review: until they take part, the halt
// holds against the model's own commands.
func TestHeldUntilTheHumanTakesPart(t *testing.T) {
	before, at, after := t0.Add(-time.Minute), t0, t0.Add(time.Minute)
	for _, c := range []struct {
		name    string
		set     func(st *State)
		humanAt time.Time
		held    bool
	}{
		{"nothing", func(*State) {}, time.Time{}, false},
		{"blocked", func(st *State) { SetBlocked(st, "need a key", at) }, time.Time{}, true},
		{"blocked, human earlier", func(st *State) { SetBlocked(st, "need a key", at) }, before, true},
		{"blocked, human at the same moment", func(st *State) { SetBlocked(st, "need a key", at) }, at, true},
		{"blocked, human since", func(st *State) { SetBlocked(st, "need a key", at) }, after, false},
		{"decision", func(st *State) { st.Run.Escalation = &Escalation{Kind: "decision", Reason: "deciding", Since: at} }, before, true},
		{"review", func(st *State) { st.Run.Escalation = &Escalation{Kind: "review", Since: at} }, time.Time{}, true},
		{"review, human since", func(st *State) { st.Run.Escalation = &Escalation{Kind: "review", Since: at} }, after, false},
		{"review due", func(st *State) { DueReview(st, "P0", at) }, before, true},
		{"review due, human since", func(st *State) { DueReview(st, "P0", at) }, after, false},
		{"stalled is the watchdog's", func(st *State) { st.Run.Escalation = &Escalation{Kind: "stalled", Since: at} }, time.Time{}, false},
	} {
		st := running(t)
		c.set(&st)
		st.Run.HumanAt = c.humanAt
		if _, _, held := st.Held(); held != c.held {
			t.Errorf("%s: held %v, want %v", c.name, held, c.held)
		}
	}
	st := running(t)
	SetBlocked(&st, "need a key", at)
	if what, since, _ := st.Held(); what != "blocked: need a key" || !since.Equal(at) {
		t.Errorf("%q %v", what, since)
	}
}

// A review ends when the human lets the run go on: the phase's decisions without them are marked
// reviewed, and no longer count toward the cap.
func TestResumingReviewsTheDecisions(t *testing.T) {
	st := running(t)
	AddNote(&st, "a", "", t0)
	AddNote(&st, "b", "", t0)
	DueReview(&st, "P0", t0)
	if err := Resume(&st); err != nil {
		t.Fatal(err)
	}
	if st.ReviewDue != "" || !st.ReviewAt.IsZero() || st.Unattended("P0") != 0 || !st.Decisions[1].Reviewed {
		t.Fatalf("%q %+v", st.ReviewDue, st.Decisions)
	}
	if err := Resume(&st); err == nil {
		t.Error("resumed with nothing to resume")
	}
}
