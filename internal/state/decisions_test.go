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

// A hold for the human that lapsed is settled by baton for them: Go ahead, a decision without the human
// that may make a review due, or Not now for one only they could let through, which is none. A notice
// says so either way.
func TestAHoldLapses(t *testing.T) {
	for _, c := range []struct {
		name    string
		untimed string
		held    bool // the lead's: resolved held already; otherwise an agent's, pending in its sub-run
		maxAuto int
		answer  string
		without bool
		last    bool
	}{
		{"the lead's, held", "", true, 0, "Go ahead", true, false},
		{"an agent's, pending", "", false, 0, "Go ahead", true, false},
		{"the last one before a review", "", true, 1, "Go ahead", true, true},
		{"outward: Not now, never a review", "'push' looks outward-facing", true, 1, NotNow, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := running(t)
			p, _ := AddProposal(&st, Decision{What: "use a fake clock", Untimed: c.untimed}, t0)
			if c.held {
				Resolve(&st, p.ID, ByHeld, "Wait for me", t0)
				if h := st.HeldProposal(t0); h == nil || h.ID != p.ID {
					t.Fatalf("held proposal %+v", h)
				}
				if h := st.HeldProposal(t0.Add(time.Minute)); h != nil {
					t.Fatalf("a hold raised later holds %+v, held before it", h)
				}
			}
			at := t0.Add(20 * time.Minute)
			answer, n, last := LapseHold(&st, &st.Decisions[0], c.maxAuto, at)
			d := st.Decisions[0]
			if answer != c.answer || last != c.last || d.Resolved == nil || d.Resolved.By != ByTimeout || d.Resolved.Answer != c.answer || !d.Lapsed.Equal(at) {
				t.Fatalf("answer %q last %v: %+v %+v", answer, last, d, d.Resolved)
			}
			if d.WithoutHuman() != c.without || (n == 1) != c.without {
				t.Errorf("without the human %v, %d unattended", d.WithoutHuman(), n)
			}
			if (st.ReviewDue == "P0") != c.last {
				t.Errorf("review due %q", st.ReviewDue)
			}
			if len(st.Run.Notices) != 1 || st.Run.Notices[0].Kind != "hold_lapsed" || st.Run.Notices[0].Text != "use a fake clock" {
				t.Errorf("notices %+v", st.Run.Notices)
			}
			if h := st.HeldProposal(t0); h != nil {
				t.Errorf("still held: %+v", h)
			}
		})
	}
}

// A worker's proposal is put to the human by the lead: the worker owes no question of its own.
func TestOwnProposalLeavesRelayedOnesToTheLead(t *testing.T) {
	for _, c := range []struct {
		name    string
		relayed bool
		own     bool
	}{{"the session's own proposal", false, true}, {"a worker's relayed proposal", true, false}} {
		st := running(t)
		AddProposal(&st, Decision{What: "use a fake", Relayed: c.relayed}, t0)
		if p := st.OwnProposal(); (p != nil) != c.own || st.PendingProposal() == nil {
			t.Errorf("%s: OwnProposal %+v", c.name, p)
		}
	}
}
