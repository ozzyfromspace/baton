package board

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase's gate and its wait on something outside run side by side, and the board names both: the lead's
// phase, a phase verifying, a phase agent's, and the lead's own line.
func TestAGateAndAWaitSideBySide(t *testing.T) {
	both := func(phase, prefix string) []state.Job {
		return []state.Job{
			{ID: prefix + "j2", Phase: phase, Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-2 * time.Minute)},
			{ID: prefix + "j3", Phase: phase, Kind: state.JobAwait, Status: state.JobQueued, Queued: now.Add(-time.Minute)},
		}
	}
	for _, c := range []struct {
		name string
		run  func() *run
		want []string // lines the board has
	}{
		{"the lead waits on both", func() *run {
			r := newRun()
			r.st.Jobs, r.st.Run.TurnOpen = both("P2", ""), false
			return r
		}, []string{"step 2  ⧗ P2  Every hook knows its loop — verifying 2m (jobs j2 and j3)", "lead: its turn over, waiting on j2 and j3"}},
		{"the lead works on while both run", func() *run {
			r := newRun()
			r.st.Jobs = both("P2", "")
			return r
		}, []string{"step 2  ▶ P2  Every hook knows its loop — the lead works on it 5m (jobs j2 and j3)"}},
		{"only a wait runs", func() *run {
			r := newRun()
			r.st.Jobs, r.st.Run.TurnOpen = both("P2", "")[1:], false
			return r
		}, []string{"step 2  ⧗ P2  Every hook knows its loop — awaiting 1m (job j3)"}},
		{"a phase agent verifying on both", func() *run {
			r := newRun()
			sub := r.agent("P3", "a1b2c3d4e5f6", 12*time.Minute)
			sub.Phases["P3"].Status, sub.Phases["P3"].On = state.PhaseVerifying, []string{"P3.j2", "P3.j3"}
			sub.Jobs = both("P3", "P3.")
			return r
		}, []string{"        ⧗ P3  Phase agents — verifying 2m (jobs P3.j2 and P3.j3) · @P3 <your words> · agent a1b2c3d4"}},
		{"a phase set aside on both", func() *run {
			r := newRun()
			r.st.Phases["P3"] = &state.PhaseState{Status: state.PhaseParked, On: []string{"j2", "j3"}, Park: &state.Park{Reason: state.ParkJob, Since: now, Job: "j3"}}
			r.st.Jobs = both("P3", "")
			return r
		}, []string{"(jobs j2 and j3)"}},
	} {
		got := strings.Join(c.run().board("").Lines(0), "\n")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: the board lacks %q:\n%s", c.name, w, got)
			}
		}
	}
}
