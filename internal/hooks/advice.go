package hooks

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The advisor's notes (loop/advisor.go, state.Advice): a look-in the host asked the advisor about is held
// from every path that tells look-ins and results until the host releases it; then it goes with the
// advisor's note after it, taken under the state's lock with the look-in (takeAdvice), and the line that
// tells the human says the note went with it. A stop while one is held lets the session idle: the host
// gives it the look-in within its deadline, and wakes it for that.

// adviceKey names the look-in of kind about job among a delivery's advisor's notes.
func adviceKey(kind, job string) string { return kind + "/" + job }

// takeAdvice takes the advisor's note on the look-in of kind about job, being told now (state.TakeAdvice),
// into notes, which it makes if it must, and returns them.
func takeAdvice(st *state.State, notes map[string]string, kind, job string) map[string]string {
	if job == "" {
		return notes
	}
	if note := state.TakeAdvice(st, kind, job); note != "" {
		if notes == nil {
			notes = map[string]string{}
		}
		notes[adviceKey(kind, job)] = note
	}
	return notes
}

// withNote is text, a look-in or a result, with the advisor's note on it after, if there is one.
func withNote(text, note string) string {
	if note == "" {
		return text
	}
	return text + "\n\n" + note
}

// adviceDue is how long, at now, until the last look-in held for the advisor in st goes at its deadline.
func adviceDue(st state.State, now time.Time) time.Duration {
	var until time.Time
	for _, h := range st.Holds() {
		if h.Until.After(until) {
			until = h.Until
		}
	}
	if d := until.Sub(now); d > 0 {
		return d
	}
	return 0
}
