package decide

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Clock is when t is, as the human reads it from now: the time of day on the same day ("17:00"), the
// weekday and time within the week ahead ("Mon 10:00"), the date and time beyond it ("Oct 26 10:00").
// A weekly limit resets days away.
func Clock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	switch y, m, d := t.Date(); {
	case y == now.Year() && m == now.Month() && d == now.Day():
		return t.Format("15:04")
	case t.After(now) && t.Sub(now) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("Jan 2 15:04")
}

// windowName is a usage window as the human knows it.
func windowName(window string) string {
	switch window {
	case state.WindowFiveHour:
		return "five-hour usage limit"
	case state.WindowWeekly:
		return "weekly usage limit"
	case state.WindowUnknown, "":
		return "usage limit"
	}
	return "usage limit (" + window + ")"
}

// UsageLimited is the announcement of a usage limit the session who ("this session", "the worker on P3")
// hit: of window, which resets at resets (zero: unknown), and holds the run until until. Nothing resumes a
// stopped session by itself (docs/research/usage.md): baton's own retry does, once the hold ends.
func UsageLimited(who, window string, resets, until, now time.Time) string {
	s := "baton: " + who + " hit the account's " + windowName(window)
	if resets.IsZero() {
		return s + ". No worker starts, and baton types nothing into the run's sessions until " + Clock(until, now) +
			": when it resets is unknown, so baton tries again then."
	}
	return s + ", which resets at " + Clock(resets, now) + ". No worker starts, and baton types nothing into the run's sessions until " +
		Clock(until, now) + ", then resumes them."
}

// UsageLifted is the announcement that the hold of a usage limit ended before its reset: the session who
// got a reply from the model since the limit was hit (the human switched accounts, say), so the account's
// limit no longer holds.
func UsageLifted(who string) string {
	return "baton: " + who + " got a reply from the model since the usage limit was hit, so the limit no longer holds the run."
}
