package cli

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// resumeCancel is what leaveResume cancelled: the usage limit's resume timer (Limit), the weekly
// wind-down's going on after the reset (Wind), or both, with the run's usage as it then stood.
type resumeCancel struct {
	Usage       state.Usage
	By          string
	Limit, Wind bool
}

// leaveResume cancels the run's usage limit resume timer (state/resume.go) and the weekly wind-down's going
// on (state/winddown.go) as the run leaves baton's hands (by: state.CancelDrop, state.CancelExit), at now,
// inside the run's lock. It returns what it cancelled (nil: nothing was armed), for resumeLeft.
func leaveResume(st *state.State, by string, now time.Time) *resumeCancel {
	limit := state.CancelResume(st, by, now)
	wind := state.WindLeft(st, now)
	if !limit && !wind {
		return nil
	}
	return &resumeCancel{Usage: *st.Usage, By: by, Limit: limit, Wind: wind}
}

// resumeLeft logs what leaveResume cancelled.
func resumeLeft(s *state.Store, c *resumeCancel) {
	if c == nil {
		return
	}
	fields := map[string]any{"by": c.By, "window": c.Usage.Window}
	if !c.Limit {
		fields["window"] = state.WindowWeekly
	}
	if c.Wind {
		fields["wind_down"] = true
	}
	s.Event("usage_wait_cancelled", fields)
}
