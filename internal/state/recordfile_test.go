package state

import (
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The phases whose record is due are those done with none, in the order they were done, and only for a
// plan that names a record file.
func TestUnrecorded(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	p := plan.Plan{Record: "RECORD.md", Phases: []plan.Phase{{ID: "P0"}, {ID: "P1"}, {ID: "P2"}, {ID: "P3"}, {ID: "P4"}}}
	st := New()
	st.Phases = map[string]*PhaseState{
		"P0": {Status: PhaseDone, DoneAt: now.Add(-time.Hour)},
		"P1": {Status: PhaseDone, DoneAt: now},
		"P2": {Status: PhaseDone, DoneAt: now.Add(-time.Minute)},
		"P3": {Status: PhaseVerifying},
		"P4": {Status: PhaseDone, DoneAt: now.Add(-2 * time.Hour)},
	}
	st.Records = []RecordNote{{Phase: "P4", Path: "RECORD.md", Commit: "abc", At: now}}
	if got := st.Unrecorded(p); !slices.Equal(got, []string{"P0", "P2", "P1"}) {
		t.Errorf("Unrecorded = %v, want P0, P2, P1", got)
	}
	if !st.HasRecord("P4") || st.HasRecord("P0") {
		t.Error("HasRecord")
	}
	if got := st.RecordCommits(); !slices.Equal(got, []string{"abc"}) {
		t.Errorf("RecordCommits = %v", got)
	}
	p.Record = ""
	if got := st.Unrecorded(p); got != nil {
		t.Errorf("with no record file: %v", got)
	}
}

// The lead's model is told of each record once, and none while a compaction is owed.
func TestTakeRecords(t *testing.T) {
	st := New()
	st.Records = []RecordNote{{Phase: "P0", Told: true}, {Phase: "P1"}, {Phase: "P2"}}
	st.BoundaryOwed = true
	if got := TakeRecords(&st); got != nil {
		t.Errorf("told with a compaction owed: %+v", got)
	}
	st.BoundaryOwed = false
	got := TakeRecords(&st)
	if len(got) != 2 || got[0].Phase != "P1" || got[1].Phase != "P2" {
		t.Errorf("told %+v", got)
	}
	if again := TakeRecords(&st); again != nil {
		t.Errorf("told again: %+v", again)
	}
}
