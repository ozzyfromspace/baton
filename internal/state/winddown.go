package state

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The weekly wind-down: near the end of the account's weekly allowance (weekly_reserve_pct), the run
// finishes what it started and starts nothing new, then goes on after the weekly reset.
//
//   - Wound down (Usage.WoundDown): the lead's status line read the weekly window (Usage.WeeklyPct, a
//     decimal) at the reserve's line or above (WindLine), its reset still ahead (WindDown). That reading is
//     the only one there is, and it refreshes only with the lead's turns, so the wind-down can come late; a
//     weekly limit hit before it is the resume timer's (resume.go). The lead takes no new phase (Next), and
//     no new phase agent starts (loop/fleet.go, HoldWeekly). What is under way goes on: a phase in hand,
//     parked or verifying, its gates and landings, an agent started again on its unfinished phase.
//   - Going on (WindUp): at the reset stored then plus ResumeGrace, as the resume timer would fire, never by
//     a fresh reading (the window drops from the status line once its reset passes); or at once, on a fresh
//     reading below the line (the human switched accounts, say), or with the reserve off.
//   - Cancelled as the resume timer is, by the human: a pause, or a prompt of theirs at the lead, which
//     pauses the run (hooks). A paused run never goes on by itself; /baton run gives it back, and it goes on
//     at the reset, or at once when that has passed. WindPaused marks the pause told.

// Why a wound-down run goes on (WindUpBy).
const (
	WindReset   = "reset"   // the weekly reset it waited for passed
	WindReading = "reading" // a reading of the weekly window below the line
	WindOff     = "off"     // weekly_reserve_pct is 0
)

// WindLine is the weekly reading, in percent, at which a run that leaves reserve percent winds down.
func WindLine(reserve float64) float64 { return 100 - reserve }

// sameWindow reports two readings' weekly resets that name one window: an hour apart at most.
func sameWindow(a, b time.Time) bool { return a.Sub(b).Abs() < time.Hour }

// WoundDown reports a run wound down: nothing new starts until it goes on (WindUp).
func (st State) WoundDown() bool { return st.Usage != nil && !st.Usage.WoundDown.IsZero() }

// WindArmed reports a wound-down run that goes on by itself: one that runs (a pause stops it).
func (st State) WindArmed() bool { return st.WoundDown() && st.Mode == ModeRunning }

// GoesOnAt is when a wound-down run goes on by itself: the weekly reset it waits for plus ResumeGrace
// (zero: it is not wound down).
func (u *Usage) GoesOnAt() time.Time {
	if u == nil || u.WoundDown.IsZero() {
		return time.Time{}
	}
	return u.WindFor.Add(ResumeGrace)
}

// WindDown winds the run st down at at when its weekly reading is at reserve's line or above, the window's
// reset still ahead, and it is not wound down for that window already: it records when (Usage.WoundDown)
// and the reset it waits for (WindFor), and reports whether it did. A reserve of 0 winds nothing down.
func WindDown(st *State, reserve float64, at time.Time) bool {
	u := st.Usage
	if reserve <= 0 || u == nil || u.WeeklyPct < WindLine(reserve) || !u.WeeklyResets.After(at) ||
		!u.WoundDown.IsZero() && sameWindow(u.WindFor, u.WeeklyResets) {
		return false
	}
	resets := u.WeeklyResets
	n := st.usage()
	n.WoundDown, n.WindFor, n.WindPaused = at, resets, time.Time{}
	return true
}

// WindUpBy says why the wound-down run st goes on at at, with reserve: its weekly reset passed, unless it
// is paused (WindReset); a reading of the weekly window below the line, its reset ahead (WindReading); the
// reserve off (WindOff). "" while it stays wound down.
func WindUpBy(st State, reserve float64, at time.Time) string {
	u := st.Usage
	switch {
	case !st.WoundDown():
		return ""
	case reserve <= 0:
		return WindOff
	case u.WeeklyResets.After(at) && u.WeeklyPct < WindLine(reserve):
		return WindReading
	case st.Mode != ModePaused && !at.Before(u.GoesOnAt()):
		return WindReset
	}
	return ""
}

// WindUp ends the run's wind-down at at and, running with no phase in hand, takes up the next phase of p,
// after a compaction, as at a phase boundary: it returns the phase taken up ("" for none).
func WindUp(st *State, p plan.Plan, at time.Time) string {
	if !st.WoundDown() {
		return ""
	}
	u := st.usage()
	u.WoundDown, u.WindFor, u.WindPaused, u.WoundUp = time.Time{}, time.Time{}, time.Time{}, at
	if st.Mode != ModeRunning || st.Current != "" {
		return ""
	}
	return advance(st, p, at)
}

// WindPause marks, at at, the pause of a wound-down run told: it goes on by itself no more. It reports
// whether the pause was not told yet.
func WindPause(st *State, at time.Time) bool {
	if !st.WoundDown() || st.Mode != ModePaused || !st.Usage.WindPaused.IsZero() {
		return false
	}
	st.usage().WindPaused = at
	return true
}

// WindLeft marks, at at, the going on of a wound-down run that runs cancelled as the run leaves baton's
// hands (a drop, or leaving baton, which pauses it), told by that command. It reports whether it did.
func WindLeft(st *State, at time.Time) bool {
	if !st.WindArmed() {
		return false
	}
	st.usage().WindPaused = at
	return true
}

// WindRearm arms a wound-down run's going on again once it runs after a pause told (WindPause): /baton run
// gave it back. It reports whether it did.
func WindRearm(st *State) bool {
	if !st.WindArmed() || st.Usage.WindPaused.IsZero() {
		return false
	}
	st.usage().WindPaused = time.Time{}
	return true
}

// UnderWay lists the phases of p the run st started and has not done, in order: those a wind-down lets
// finish.
func UnderWay(st State, p plan.Plan) []string {
	var ids []string
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status != PhasePending && ps.Status != PhaseDone {
			ids = append(ids, ph.ID)
		}
	}
	return ids
}
