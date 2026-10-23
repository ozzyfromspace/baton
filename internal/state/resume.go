package state

import "time"

// The resume timer: after a usage limit whose reset is known, a run that was working on its own goes on as
// soon as the window resets. Nothing resumes a stopped session by itself (docs/research/usage.md), so the
// timer is when each session's held retry types, once (loop/usage.go, Loop.watchError). It is kept in the
// run's Usage, so a host restart keeps it and a laptop asleep through the reset resumes on waking.
//
//   - Armed (Usage.ResumeAt): a limit hit by a run working on its own (Autonomous), whose window and reset
//     are known (LimitWindow), at its reset plus ResumeGrace; or, hit again within RefireWithin of the
//     timer firing, ResumeBackoff later.
//   - Cancelled (Usage.Cancelled): by the human's pause, a prompt of theirs at the lead (which pauses the
//     run), or leaving baton. The run then waits for /baton run, which arms it again (Rearm).
//   - Fired (Usage.Fired): its time came, and the run's sessions stopped on the limit are woken.
//
// A hit whose reset is unknown arms nothing: each loop retries it after Timing.RateLimitRetry, as before.

// ResumeGrace is how long after a window's reset the run's timer fires, and the run's hold ends: the
// account's own clock and baton's may differ by a little.
const ResumeGrace = 2 * time.Minute

// RefireWithin is how soon after the timer fired a new limit hit counts as the same limit, not yet gone:
// the next try backs off (ResumeBackoff).
const RefireWithin = 10 * time.Minute

// Who cancelled the resume timer (Usage.Cancelled).
const (
	CancelPause  = "pause"
	CancelPrompt = "prompt"
	CancelDrop   = "drop"
	CancelExit   = "exit"
)

// backoff is the wait before each try of a limit hit again soon after the timer fired: the last repeats.
var backoff = []time.Duration{2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute}

// ResumeBackoff is the wait before try n (1 on) of a limit hit again within RefireWithin of the timer
// firing: 2, 5, 10, then every 15 minutes. It never gives up.
func ResumeBackoff(n int) time.Duration { return backoff[min(max(n, 1), len(backoff))-1] }

// Autonomous reports a run working on its own when the session by ("lead", or a worker's phase) hit the
// usage limit: running, not paused, and the lead's stopped turn not one the human started.
func Autonomous(st State, by string) bool {
	return st.Mode == ModeRunning && (by != "lead" || st.Run.TurnBy != "human")
}

// ResumeFor is when the run's timer would fire for a limit hit at at whose window resets at resets (zero:
// unknown), as u stood before it, and the tries it would count: the reset plus ResumeGrace, a fresh start;
// with the reset unknown, ResumeBackoff after the hit when the timer fired within RefireWithin before
// it, a try more; otherwise none (zero), and the hit is retried after Timing.RateLimitRetry.
func ResumeFor(u *Usage, at, resets time.Time) (time.Time, int) {
	switch {
	case !resets.IsZero():
		return resets.Add(ResumeGrace), 0
	case u != nil && !u.Fired.IsZero() && !at.Before(u.Fired) && at.Sub(u.Fired) <= RefireWithin:
		return at.Add(ResumeBackoff(u.Tries + 1)), u.Tries + 1
	}
	return time.Time{}, 0
}

// ArmResume arms the run's timer at resume, its tries counted (ResumeFor), for the limit hit recorded in
// st. It reports whether the time it fires changed.
func ArmResume(st *State, resume time.Time, tries int) bool {
	u := st.usage()
	changed := !u.ResumeAt.Equal(resume)
	u.ResumeAt, u.Tries, u.Cancelled, u.CancelSaid = resume, tries, "", time.Time{}
	return changed
}

// CancelResume cancels the run's armed timer, by who (CancelPause, …), told and logged at at (zero: not
// yet; the coordinator does it). It reports whether a timer was armed.
func CancelResume(st *State, by string, at time.Time) bool {
	if st.Usage == nil || st.Usage.ResumeAt.IsZero() {
		return false
	}
	u := st.usage()
	u.ResumeAt, u.Cancelled, u.CancelSaid = time.Time{}, by, at
	return true
}

// Rearm arms the timer of a cancelled one again once the run is running (the human's /baton run), at the
// end of its hold: at once when that is past. It reports whether it did.
func Rearm(st *State) bool {
	if st.Mode != ModeRunning || st.Usage == nil || st.Usage.Cancelled == "" {
		return false
	}
	u := st.usage()
	u.ResumeAt, u.Cancelled, u.CancelSaid = u.Limited, "", time.Time{}
	return true
}

// FireResume fires the run's armed timer at at, once its time came while the run is running. It reports
// whether it did.
func FireResume(st *State, at time.Time) bool {
	if st.Mode != ModeRunning || st.Usage == nil || st.Usage.ResumeAt.IsZero() || at.Before(st.Usage.ResumeAt) {
		return false
	}
	u := st.usage()
	u.ResumeAt, u.Fired = time.Time{}, at
	if at.Before(u.Limited) {
		u.Limited = at
	}
	return true
}

// Waits reports a timer armed or cancelled: the run's sessions stopped on the limit hold their retry,
// whatever the hold, until it fires or /baton run arms it again.
func (u *Usage) Waits() bool { return u != nil && (!u.ResumeAt.IsZero() || u.Cancelled != "") }

// FiredFor reports a timer that fired after a session's turn ended on the limit at at.
func (u *Usage) FiredFor(at time.Time) bool { return u != nil && u.Fired.After(at) }

// usage is st's Usage, a copy of its own to change.
func (st *State) usage() *Usage {
	var u Usage
	if st.Usage != nil {
		u = *st.Usage
	}
	st.Usage = &u
	return &u
}
