package state

import (
	"strings"
	"time"
)

// The account's usage windows a limit can be (Usage.Window, StopError.Window).
const (
	WindowFiveHour = "five_hour"
	WindowWeekly   = "weekly"
	WindowUnknown  = "unknown"
)

// LimitLine is the status line reading, in percent, at which a window counts as the limit a stop hit when
// the stop does not say which: a limit's stop has read 99 as well as 100 (docs/research/usage.md).
const LimitLine = 99

// MaxReset is how far after a limit hit a reset is believed: a weekly window resets within seven days.
const MaxReset = 8 * 24 * time.Hour

// WindowOf is the window of a usage limit Claude Code named by kind (quotaLimits.rateLimitType): every
// seven_day kind (seven_day, seven_day_opus, …) is the weekly window; any other is as named, and "" none.
func WindowOf(kind string) string {
	if strings.HasPrefix(kind, "seven_day") {
		return WindowWeekly
	}
	return kind
}

// LimitWindow is which usage window the limit hit at at is, and when it resets.
//
//   - What the stop itself said (e, from the stopped loop's transcript) comes first; a stop that named its
//     window but not its reset takes the reset of that window's reading.
//   - Then a window the status line's reading u has at LimitLine or more, the weekly window first.
//   - Otherwise it is WindowUnknown, with a zero reset: a five-hour reset ahead says nothing of a weekly
//     limit, which real runs hit at 27% and 77% of the five-hour window.
//
// A reset not after at, or more than MaxReset after it, is none.
func LimitWindow(e *StopError, u *Usage, at time.Time) (window string, resets time.Time) {
	valid := func(t time.Time) bool { return t.After(at) && t.Sub(at) <= MaxReset }
	var r Usage
	if u != nil {
		r = *u
	}
	if e != nil && e.Window != "" {
		switch {
		case valid(e.Resets):
			return e.Window, e.Resets
		case e.Window == WindowWeekly && valid(r.WeeklyResets):
			return WindowWeekly, r.WeeklyResets
		case e.Window == WindowFiveHour && valid(r.Resets):
			return WindowFiveHour, r.Resets
		}
	}
	switch {
	case r.WeeklyPct >= LimitLine && valid(r.WeeklyResets):
		return WindowWeekly, r.WeeklyResets
	case r.UsedPct >= LimitLine && valid(r.Resets):
		return WindowFiveHour, r.Resets
	}
	return WindowUnknown, time.Time{}
}

// RecordLimit records a limit hit (Limit) with the window it was and when that resets (LimitWindow). It
// reports whether the hit was new.
func RecordLimit(st *State, by string, at, until time.Time, window string, resets time.Time) bool {
	if !Limit(st, by, at, until) {
		return false
	}
	st.Usage.Window, st.Usage.WindowResets, st.Usage.Lifted = window, resets, time.Time{}
	return true
}

// RepliedSince reports a turn that started after t and ended with the model's reply rather than an API
// error: a request sent after t went through.
func (r Runtime) RepliedSince(t time.Time) bool {
	return r.TurnStarted.After(t) && r.LastStop.After(r.TurnStarted) && (r.LastError == nil || !r.LastError.At.After(r.TurnStarted))
}

// LiftLimit ends, at at, the hold of the limit recorded in st: a session of the run got a reply since it
// was hit (RepliedSince), so the account's limit no longer holds, whenever its reset was to be (the human
// switched accounts, say), and an armed resume timer fires now. It reports whether a hold was lifted.
func LiftLimit(st *State, at time.Time) bool {
	u := st.Usage
	if u == nil || !at.Before(u.Limited) {
		return false
	}
	next := *u
	next.Limited, next.Lifted = at, at
	if !next.ResumeAt.IsZero() {
		next.ResumeAt = at
	}
	st.Usage = &next
	return true
}

// UsageReading is what a status line said of the account's usage windows. Has… is false for a window it
// said nothing of: none before the first response, and none once its reset has passed.
type UsageReading struct {
	HasFiveHour    bool
	FiveHourPct    float64
	FiveHourResets time.Time
	HasWeekly      bool
	WeeklyPct      float64
	WeeklyResets   time.Time
}

// Empty reports a reading of neither window.
func (r UsageReading) Empty() bool { return !r.HasFiveHour && !r.HasWeekly }

// Same reports a reading that says nothing u does not: each window it has, u has the same.
func (r UsageReading) Same(u *Usage) bool {
	if u == nil {
		return r.Empty()
	}
	return (!r.HasFiveHour || u.UsedPct == r.FiveHourPct && u.Resets.Equal(r.FiveHourResets)) &&
		(!r.HasWeekly || u.WeeklyPct == r.WeeklyPct && u.WeeklyResets.Equal(r.WeeklyResets))
}

// SawUsage records reading r, taken at at, in st: the windows it has, keeping what st recorded of the
// others and of a limit.
func SawUsage(st *State, r UsageReading, at time.Time) {
	var u Usage
	if st.Usage != nil {
		u = *st.Usage
	}
	if r.HasFiveHour {
		u.UsedPct, u.Resets = r.FiveHourPct, r.FiveHourResets
	}
	if r.HasWeekly {
		u.WeeklyPct, u.WeeklyResets = r.WeeklyPct, r.WeeklyResets
	}
	u.At = at
	st.Usage = &u
}
