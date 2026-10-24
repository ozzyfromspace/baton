package state

import (
	"reflect"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// The last whole run of a command is the one that ended last, whatever its end: never a rerun, a narrowed
// rerun, an await, a run under way or one that could not run. A run's losses come in the order the gate ran
// its stages.
func TestTheLastWholeRunAndItsLosses(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	st := State{Jobs: []Job{
		{ID: "j1", Kind: JobVerify, Command: "bin/qa-set", Status: JobFailed, Ended: t0.Add(time.Minute)},
		{ID: "j2", Kind: JobVerify, Command: "bin/qa-set", Status: JobTimeout, Ended: t0.Add(2 * time.Minute)},
		{ID: "j3", Kind: JobVerify, Command: "bin/qa-set", Status: JobFailed, Reruns: "j2", Ended: t0.Add(3 * time.Minute)},
		{ID: "j4", Kind: JobVerify, Command: "bin/qa-set", Status: JobFailed, Narrows: []string{"TestA"}, Ended: t0.Add(4 * time.Minute)},
		{ID: "j5", Kind: JobAwait, Command: "bin/qa-set", Status: JobFailed, Ended: t0.Add(5 * time.Minute)},
		{ID: "j6", Kind: JobVerify, Command: "bin/qa-set", Status: JobLaneError, Ended: t0.Add(6 * time.Minute)},
		{ID: "j7", Kind: JobVerify, Command: "bin/qa-set", Status: JobRunning},
		{ID: "j8", Command: "make e2e", Status: JobPassed, Ended: t0.Add(8 * time.Minute)},
	}}
	for _, c := range []struct{ command, want string }{{"bin/qa-set", "j2"}, {"make e2e", "j8"}, {"make lint", ""}} {
		got := ""
		if j := st.LastWhole(c.command); j != nil {
			got = j.ID
		}
		if got != c.want {
			t.Errorf("the last whole run of %s: %q, want %q", c.command, got, c.want)
		}
	}
	m := map[string]lane.EnvMatch{"build": {Kind: lane.EnvKilled, Line: "x"}, "up": {Kind: "a full disk", Line: "y"}}
	stages := []lane.Stage{{Name: "up"}, {Name: "lint"}, {Name: "build"}}
	if got := Losses(m, stages); !reflect.DeepEqual(got, []EnvLoss{{"up", "a full disk", "y"}, {"build", lane.EnvKilled, "x"}}) {
		t.Errorf("losses %+v", got)
	}
}
