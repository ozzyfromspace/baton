package state

import (
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// Each stage a rerun runs whole says why, with its last time from the record it is rerun from: a failed
// stage its own reason (lane.Stage.Unnarrowed, or no failure ids for a record older than it), a skipped one
// the job it was skipped in, one --stages names that it was brought back, and one a later run showed a
// failure in and never ended that it is not on record, with no time. Carried and narrowed stages are not
// whole.
func TestWholeStagesSayWhyAndTook(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	ran := func(name, status string, from, took time.Duration) lane.Stage {
		return lane.Stage{Name: name, Status: status, Exit: 1, Started: t0.Add(from), Ended: t0.Add(from + took)}
	}
	unnarrowed := func(s lane.Stage, why string) lane.Stage { s.Unnarrowed = why; return s }
	failures := func(s lane.Stage, ids ...string) lane.Stage { s.Failures = ids; return s }
	m := time.Minute
	recs := map[string][]lane.Stage{
		"j1": {
			ran("unit", lane.StagePassed, 0, 2*m),
			unnarrowed(ran("browser", lane.StageFailed, 2*m, 78*m), lane.UnnarrowedNoIDs),
			unnarrowed(ran("api", lane.StageFailed, 80*m, 5*m), lane.UnnarrowedWhole),
			unnarrowed(ran("load", lane.StageFailed, 85*m, 4*m), lane.UnnarrowedMany),
			ran("old", lane.StageFailed, 89*m, 3*m), // recorded before Unnarrowed
			failures(ran("lint", lane.StageFailed, 92*m, time.Minute), "TestLint"),
			failures(ran("e2e", lane.StageFailed, 93*m, 30*m), "TestE2E"),
			{Name: "docs", Status: lane.StageSkipped, Exit: 1},
		},
		"j2": {ran("unit", lane.StagePassed, 200*m, 2*m)},
	}
	jobs := []Job{
		{ID: "j1", Phase: "P0", Kind: JobVerify, Command: "bin/gate", Commit: "c1", Status: JobFailed, Exit: 1},
		// stopped in e2e after a failure, with no record of how e2e went
		{ID: "j2", Phase: "P0", Kind: JobVerify, Command: "bin/gate", Commit: "c2", Status: JobInterrupted, Failing: &Failing{Line: "--- FAIL: TestE2E", Stage: "e2e"}},
	}
	st := State{Jobs: jobs}
	r, why := st.RerunOf("P0", func(j Job) []lane.Stage { return recs[j.ID] })
	if why != "" {
		t.Fatalf("RerunOf: %s", why)
	}
	for _, c := range []struct {
		name  string
		bring []string
		whole []WholeStage
	}{
		{"as recorded", nil, []WholeStage{
			{Name: "browser", Why: lane.UnnarrowedNoIDs, Took: 78 * m},
			{Name: "api", Why: lane.UnnarrowedWhole, Took: 5 * m},
			{Name: "load", Why: lane.UnnarrowedMany, Took: 4 * m},
			{Name: "old", Why: lane.UnnarrowedNoIDs, Took: 3 * m},
			{Name: "docs", Why: "skipped in job j1"},
			{Name: "e2e", Why: WholeNotOnRecord},
		}},
		{"brought back", []string{"unit", "lint"}, []WholeStage{
			{Name: "unit", Why: WholeBroughtBack, Took: 2 * m},
			{Name: "browser", Why: lane.UnnarrowedNoIDs, Took: 78 * m},
			{Name: "api", Why: lane.UnnarrowedWhole, Took: 5 * m},
			{Name: "load", Why: lane.UnnarrowedMany, Took: 4 * m},
			{Name: "old", Why: lane.UnnarrowedNoIDs, Took: 3 * m},
			{Name: "lint", Why: WholeBroughtBack, Took: time.Minute},
			{Name: "docs", Why: "skipped in job j1"},
			{Name: "e2e", Why: WholeNotOnRecord},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := PlannedRerun(Job{ID: "j3", Commit: "c3"}, r, RerunRecord(r.Stages, c.bring))
			if !slices.Equal(v.Whole, c.whole) {
				t.Errorf("whole:\n%+v\nwant\n%+v", v.Whole, c.whole)
			}
			for _, w := range v.Whole {
				if slices.ContainsFunc(v.Narrowed, func(s lane.Stage) bool { return s.Name == w.Name }) ||
					slices.ContainsFunc(v.Carried, func(s CarriedStage) bool { return s.Name == w.Name }) {
					t.Errorf("%s is whole and carried or narrowed: %+v", w.Name, v)
				}
			}
		})
	}
	if got := WholeSkipped(""); got != "skipped" {
		t.Errorf("skipped in no job: %q", got)
	}
	// As it ran, a rerun's view lists no whole stage: its result says what it carried and narrowed.
	if v := RanRerun(Job{ID: "j3", Reruns: "j2"}, recs["j1"], r.Stages); v.Whole != nil {
		t.Errorf("ran: %+v", v.Whole)
	}
}
