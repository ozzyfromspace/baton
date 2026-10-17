package decide

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// laneWaitSaid is the shortest wait for a lane a job's outcome mentions.
const laneWaitSaid = time.Minute

// tookFor is how long finished job j ran, as its outcome says it: " after 12m", counted from when it got
// its lane (Job.Ran), and with a wait for one of a minute or more, " after 12m, 31m waiting for a lane". A
// job with no record of when it got its lane counts from its start.
func tookFor(j state.Job) string {
	if j.Ran.IsZero() || j.Ended.IsZero() || j.Ran.Before(j.Started) {
		return " after " + Span(j.Took(j.Ended))
	}
	took := " after " + Span(max(j.Ended.Sub(j.Ran), 0))
	if waited := j.Ran.Sub(j.Started); waited >= laneWaitSaid {
		took += ", " + Span(waited) + " waiting for a lane"
	}
	return took
}
