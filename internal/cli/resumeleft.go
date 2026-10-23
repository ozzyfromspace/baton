package cli

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// leaveResume cancels the run's usage limit resume timer (state/resume.go) as the run leaves baton's hands
// (by: state.CancelDrop, state.CancelExit), at now, inside the run's lock. It returns the timer it
// cancelled (nil: none was armed), for resumeLeft.
func leaveResume(st *state.State, by string, now time.Time) *state.Usage {
	if !state.CancelResume(st, by, now) {
		return nil
	}
	u := *st.Usage
	return &u
}

// resumeLeft logs the resume timer leaveResume cancelled.
func resumeLeft(s *state.Store, u *state.Usage) {
	if u != nil {
		s.Event("usage_wait_cancelled", map[string]any{"by": u.Cancelled, "window": u.Window})
	}
}
