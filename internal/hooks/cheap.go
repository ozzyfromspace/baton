package hooks

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What a gate's result reads beside its log's end (JobLog): the tests a red one says failed, by id, so the
// result can offer the rerun of only them, and what the phase's gates have cost so far.

// failedOf reads the tests the log at path of red gate j says failed (lane.FailureIDs): at most
// lane.MaxFailures, and how many more. None for any other job, or a log that cannot be read.
func failedOf(j state.Job, path string) ([]lane.FailedTest, int) {
	if j.Kind == state.JobAwait || !j.Red() {
		return nil, 0
	}
	fs, more, err := lane.FailureIDs(path, 0)
	if err != nil {
		return nil, 0
	}
	return fs, more
}

// costOf is what gate j's phase has spent in gates by now, and in j's command among them (state.GateCost),
// read from the state of s, the run that holds j (a phase agent's sub-run for its jobs), without its lock.
// Zero for an await, or a state that cannot be read.
func costOf(s *state.Store, j state.Job) state.Cost {
	if j.Kind == state.JobAwait {
		return state.Cost{}
	}
	st, err := s.Peek()
	if err != nil {
		return state.Cost{}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return state.GateCost(st, j.Phase, j.Command, now())
}
