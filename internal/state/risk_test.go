package state

import (
	"reflect"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The ledger adds each phase's points once; a passing full check resets it once, keeping the phases done
// after the check began, and takes the crossings, the review and a hold with it.
func TestRiskAccumulatesAndResets(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	var st State
	for i, pr := range []PhaseRisk{{Phase: "P1", Points: 7, At: t0}, {Phase: "P2", Points: 5, At: t0.Add(time.Hour)}, {Phase: "P3", Points: 4, At: t0.Add(2 * time.Hour)}} {
		if total := AddRisk(&st, pr); total != []int{7, 12, 16}[i] {
			t.Fatalf("AddRisk(%s) = %d", pr.Phase, total)
		}
	}
	if total := AddRisk(&st, PhaseRisk{Phase: "P2", Points: 9}); total != 16 || len(st.Risk.Phases) != 3 {
		t.Errorf("a phase scored again: total %d, %d phases", total, len(st.Risk.Phases))
	}
	if !st.Scored("P2") || st.Scored("P4") {
		t.Errorf("Scored = %v", st.Risk.Scored)
	}
	st.Risk.Crossed = RiskReview
	if !RiskVerdict(&st, VerdictFull, t0.Add(3*time.Hour)) || !st.RiskHeld() {
		t.Fatal("a full verdict holds nothing")
	}

	// A check queued between P2 and P3 checked P1's and P2's work.
	if !ResetRisk(&st, "j7", t0.Add(4*time.Hour), "abc", t0.Add(90*time.Minute)) {
		t.Fatal("ResetRisk refused")
	}
	r := st.Risk
	if r.Total != 4 || len(r.Phases) != 1 || r.Phases[0].Phase != "P3" || !r.Since.Equal(t0.Add(4*time.Hour)) || r.Commit != "abc" {
		t.Errorf("after the reset: %+v", *r)
	}
	if r.Crossed != "" || st.RiskHeld() || r.Verdict != "" || !r.Reviewed.IsZero() {
		t.Errorf("the reset kept a crossing, a verdict or a hold: %+v", *r)
	}
	if ResetRisk(&st, "j7", t0.Add(5*time.Hour), "def", t0.Add(5*time.Hour)) || r.Total != 4 {
		t.Error("the same check reset the ledger twice")
	}
	if !st.Cleared("j7") || st.Cleared("j8") {
		t.Errorf("Cleared = %v", r.Cleared)
	}
	if !reflect.DeepEqual(r.Scored, []string{"P1", "P2", "P3"}) {
		t.Errorf("a reset forgot what was scored: %v", r.Scored)
	}
	MarkScored(&st, "P9")
	if !st.Scored("P9") || r.Total != 4 {
		t.Errorf("MarkScored: scored %v, total %d", r.Scored, r.Total)
	}
}

// Only a full verdict holds new phases, once; ReleaseRisk lifts the hold.
func TestRiskVerdictAndRelease(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		verdict string
		held    bool
	}{{"continue", false}, {"targeted", false}, {VerdictFull, true}} {
		var st State
		if got := RiskVerdict(&st, c.verdict, t0); got != c.held || st.RiskHeld() != c.held || st.Risk.Verdict != c.verdict || !st.Risk.Reviewed.Equal(t0) {
			t.Errorf("%s: held %v, %+v", c.verdict, got, *st.Risk)
		}
	}
	var st State
	RiskVerdict(&st, VerdictFull, t0)
	st.Risk.Told = t0
	if RiskVerdict(&st, VerdictFull, t0.Add(time.Minute)) || !st.Risk.Held.Equal(t0) {
		t.Error("a second full verdict started the hold again")
	}
	if !ReleaseRisk(&st) || st.RiskHeld() || !st.Risk.Told.IsZero() {
		t.Errorf("ReleaseRisk: %+v", *st.Risk)
	}
	if ReleaseRisk(&st) || ReleaseRisk(&State{}) {
		t.Error("released a hold that was not there")
	}
}

// The record the ledger reads: checks, phases finished over red, conflicts; and the phases done but not
// scored, in the order they were done.
func TestRiskRecordAndUnscored(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	var st State
	if st.Checks("P1") != 0 || st.FinishedDespiteRed("P1") || st.Conflicted("P1") != 0 || st.RiskTotal() != 0 {
		t.Error("an empty record says something")
	}
	CountCheck(&st, "P1")
	CountCheck(&st, "P1")
	MarkDespiteRed(&st, "P1")
	MarkDespiteRed(&st, "P1")
	CountConflict(&st, "P2")
	if st.Checks("P1") != 2 || !st.FinishedDespiteRed("P1") || len(st.Risk.DespiteRed) != 1 || st.Conflicted("P2") != 1 || st.Conflicted("P1") != 0 {
		t.Errorf("record = %+v", *st.Risk)
	}

	p := plan.Plan{Phases: []plan.Phase{{ID: "P0"}, {ID: "P1"}, {ID: "P2"}, {ID: "P3"}}}
	st.Phases = map[string]*PhaseState{
		"P0": {Status: PhaseDone, DoneAt: t0.Add(3 * time.Hour)},
		"P1": {Status: PhaseDone, DoneAt: t0.Add(time.Hour)},
		"P2": {Status: PhaseVerifying},
		"P3": {Status: PhaseDone, DoneAt: t0.Add(2 * time.Hour)},
	}
	if got := st.Unscored(p); !reflect.DeepEqual(got, []string{"P1", "P3", "P0"}) {
		t.Errorf("Unscored = %v", got)
	}
	AddRisk(&st, PhaseRisk{Phase: "P3"})
	if got := st.Unscored(p); !reflect.DeepEqual(got, []string{"P1", "P0"}) {
		t.Errorf("Unscored after P3 = %v", got)
	}
}
