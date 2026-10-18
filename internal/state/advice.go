package state

import (
	"slices"
	"time"
)

// The advisor (loop/advisor.go) is a small model the host asks about a look-in before the session is given
// it: whether a failure looks like the change's or the environment's, whether the rest of a gate is worth
// waiting for, the smallest next check. Its words go with the look-in, as a suggestion. While the host asks,
// the look-in is held from the session (Advice): every path that tells look-ins and results passes it over
// (UntoldFailures, LeftLookIns, Due) until the host releases it, with the advisor's words when they came,
// and with none at its deadline (Until) whatever the advisor does. A hook that finds a hold past its
// deadline by AdviceGrace releases it itself (LapseAdvice), so a look-in never waits on a host that stopped
// looking.

// LookEnv is the kind of a red gate's result whose log shows its environment failed it (lane.Environment):
// the advisor is asked about it as about a look-in, and the result is held as one.
const LookEnv = "environment"

// AdviceGrace is how long past its deadline a hold stands before a hook releases the look-in itself: the
// host's own tick releases it at the deadline.
const AdviceGrace = 10 * time.Second

// Advice is the advisor's part in the look-in of Kind (a LookIn's kind, LookFailing or LookEnv) about Job:
// held from the session until Released, which the host does when the advisor answers, or at Until whatever
// it does; then Text, the advisor's words as the session is given them ("" for none), goes with the look-in
// when it is told (TakeAdvice).
type Advice struct {
	Kind     string    `json:"kind"`
	Job      string    `json:"job"`
	Until    time.Time `json:"until"`
	Released bool      `json:"released,omitempty"`
	Text     string    `json:"text,omitempty"`
}

// adviceAt is the index of the advice on the look-in of kind about job, or -1.
func (st State) adviceAt(kind, job string) int {
	return slices.IndexFunc(st.Advice, func(a Advice) bool { return a.Kind == kind && a.Job == job })
}

// Advising reports whether the look-in of kind about job is held for the advisor.
func (st State) Advising(kind, job string) bool {
	i := st.adviceAt(kind, job)
	return i >= 0 && !st.Advice[i].Released
}

// Advised reports whether the advisor was asked about the look-in of kind about job, which has not been
// told yet: it is held, or released with what the advisor said.
func (st State) Advised(kind, job string) bool { return st.adviceAt(kind, job) >= 0 }

// Holds lists the look-ins held for the advisor.
func (st State) Holds() []Advice {
	var out []Advice
	for _, a := range st.Advice {
		if !a.Released {
			out = append(out, a)
		}
	}
	return out
}

// AdviceHeld reports whether a look-in of the session is held for the advisor: the host gives it within
// its deadline, and wakes an idle session for it.
func (st State) AdviceHeld() bool { return len(st.Holds()) > 0 }

// HoldForAdvice holds the look-in of kind about job from the session until the host releases it, until at
// the latest, and reports whether it did: only a look-in still to be told (adviceOpen) is held, and never
// twice for the same kind and job.
func HoldForAdvice(st *State, kind, job string, until time.Time) bool {
	if st.Advised(kind, job) || !st.adviceOpen(kind, job) {
		return false
	}
	st.Advice = append(st.Advice, Advice{Kind: kind, Job: job, Until: until})
	return true
}

// adviceOpen reports whether the look-in of kind about job is still to be told: a first failure of a gate
// that runs on, not told yet; a red gate's result not given yet; a look-in the host left about a gate that
// runs.
func (st State) adviceOpen(kind, job string) bool {
	j := st.Job(job)
	if j == nil {
		return false
	}
	switch kind {
	case LookFailing:
		return j.Status == JobRunning && j.Failing != nil && j.FailTold.IsZero()
	case LookEnv:
		return j.Finished() && j.Delivered.IsZero()
	}
	return !j.Finished() && slices.ContainsFunc(st.LookIns, func(li LookIn) bool { return li.Kind == kind && li.Job == job })
}

// ReleaseAdvice releases the look-in of kind about job held for the advisor, with text, the advisor's words
// ("" for none), and reports whether it was held: one released before (at its deadline, by a hook) or gone
// was not, and text came too late for it.
func ReleaseAdvice(st *State, kind, job, text string) bool {
	i := st.adviceAt(kind, job)
	if i < 0 || st.Advice[i].Released {
		return false
	}
	st.Advice[i].Released, st.Advice[i].Text = true, text
	return true
}

// TakeAdvice returns the advisor's words on the look-in of kind about job, which is being told now, and
// forgets them: "" when it said none, or was not asked.
func TakeAdvice(st *State, kind, job string) string {
	i := st.adviceAt(kind, job)
	if i < 0 || !st.Advice[i].Released {
		return ""
	}
	text := st.Advice[i].Text
	st.Advice = slices.Delete(st.Advice, i, i+1)
	return text
}

// LapseAdvice releases, with no words, every hold past its deadline by AdviceGrace at now, and forgets the
// advice on look-ins no longer to be told: their job ended, or a path that gives no advice told them.
func LapseAdvice(st *State, now time.Time) {
	if len(st.Advice) == 0 {
		return
	}
	st.Advice = slices.DeleteFunc(st.Advice, func(a Advice) bool { return !st.adviceOpen(a.Kind, a.Job) })
	for i := range st.Advice {
		if a := &st.Advice[i]; !a.Released && !a.Until.IsZero() && now.Sub(a.Until) >= AdviceGrace {
			a.Released, a.Text = true, ""
		}
	}
	if len(st.Advice) == 0 {
		st.Advice = nil
	}
}
