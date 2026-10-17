package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A job's outcome counts its running time from when it got its lane, and says a wait for one worth saying.
func TestOutcomeCountsFromTheLane(t *testing.T) {
	start := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	for _, c := range []struct {
		name string
		j    state.Job
		want string
	}{
		{"no record of the lane", state.Job{Status: state.JobPassed, Started: start, Ended: at(43 * time.Minute)}, "passed after 43m"},
		{"no wait", state.Job{Status: state.JobPassed, Started: start, Ran: at(time.Second), Ended: at(12*time.Minute + time.Second)}, "passed after 12m"},
		{"a wait under a minute", state.Job{Status: state.JobPassed, Started: start, Ran: at(50 * time.Second), Ended: at(12*time.Minute + 50*time.Second)}, "passed after 12m"},
		{"a long wait", state.Job{Status: state.JobPassed, Started: start, Ran: at(31 * time.Minute), Ended: at(43 * time.Minute)}, "passed after 12m, 31m waiting for a lane"},
		{"failed", state.Job{Status: state.JobFailed, Exit: 1, Started: start, Ran: at(2 * time.Hour), Ended: at(2*time.Hour + 30*time.Second)},
			"failed (exit 1) after 30s, 2h00m waiting for a lane"},
		{"ran out of time", state.Job{Status: state.JobTimeout, Started: start, Ran: at(5 * time.Minute), Ended: at(3*time.Hour + 5*time.Minute)},
			"ran out of time after 3h00m, 5m waiting for a lane"},
		{"interrupted", state.Job{Status: state.JobInterrupted, Reason: "the job was cancelled", Started: start, Ran: at(time.Minute), Ended: at(3 * time.Minute)},
			"was interrupted after 2m, 1m waiting for a lane: the job was cancelled"},
		{"a lane before its start, as a clock that stepped back records it", state.Job{Status: state.JobPassed, Started: start, Ran: at(-time.Minute), Ended: at(4 * time.Minute)}, "passed after 4m"},
	} {
		if got := Outcome(c.j); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
