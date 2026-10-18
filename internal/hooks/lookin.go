package hooks

import (
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// lookIns are the look-ins a hook took for the model under the state's lock (takeLookIns): the first
// failures of its gates that run on, and what the host left for it (state.LookIn). What a failing gate in
// the lead's checkout holds is read after the lock.
type lookIns struct {
	failing []state.Job // each with the failure it is told of (state.UntoldFailures)
	left    []state.LookIn
	advice  map[string]string // the advisor's notes on them (state.TakeAdvice), by adviceKey
}

func (l lookIns) any() bool { return len(l.failing) > 0 || len(l.left) > 0 }

// text is the look-ins for the model. A failing gate run in the lead's checkout (--here) names the landings
// it holds: the run's phases whose work waits to land, read from its state, and how many of other baton
// sessions' do, read through the project. No landing touches a phase agent's worktree.
func (l lookIns) text(s *state.Store) string {
	var parts []string
	for _, j := range l.failing {
		var landing []string
		others := 0
		if j.Here && s.Worker() == "" {
			landing, others = heldHere(s)
		}
		parts = append(parts, withNote(decide.FirstFailure(j, *j.Failing, failedIn(j), landing, others), l.advice[adviceKey(state.LookFailing, j.ID)]))
	}
	for _, li := range l.left {
		parts = append(parts, withNote(li.Text, l.advice[adviceKey(li.Kind, li.Job)]))
	}
	return strings.Join(parts, "\n\n")
}

// says tells the human which look-ins the model was given.
func (l lookIns) says() string {
	var lines []string
	for _, j := range l.failing {
		lines = append(lines, l.noted(decide.FirstFailureSaid(j, *j.Failing, failedIn(j)), state.LookFailing, j.ID))
	}
	for _, li := range l.left {
		if li.Say != "" {
			lines = append(lines, l.noted(li.Say+" → told Claude", li.Kind, li.Job))
		}
	}
	return strings.Join(lines, "\n")
}

// noted is line, which tells the human of the look-in of kind about job, saying whether the advisor's note
// went with it.
func (l lookIns) noted(line, kind, job string) string {
	if l.advice[adviceKey(kind, job)] == "" {
		return line
	}
	return decide.WithAdvisorNote(line)
}

// event logs the look-ins given, and how.
func (l lookIns) event(s *state.Store, via string) {
	for _, j := range l.failing {
		fields := map[string]any{"type": state.LookFailing, "job": j.ID, "phase": j.Phase, "line": j.Failing.Line,
			"stage": j.Failing.Stage, "seconds": int(failedIn(j).Seconds()), "via": via}
		l.advised(fields, state.LookFailing, j.ID)
		s.Event("look_in_told", fields)
	}
	for _, li := range l.left {
		fields := map[string]any{"type": li.Kind, "via": via}
		if li.Job != "" {
			fields["job"] = li.Job
		}
		l.advised(fields, li.Kind, li.Job)
		s.Event("look_in_told", fields)
	}
}

// advised marks fields, a told look-in's event, when the advisor's note went with it.
func (l lookIns) advised(fields map[string]any, kind, job string) {
	if l.advice[adviceKey(kind, job)] != "" {
		fields["advised"] = true
	}
}

// failedIn is how long job j had run when it failed: from when its command began (Job.Ran; when it started,
// for a job recorded without it) to its failure.
func failedIn(j state.Job) time.Duration {
	from := j.Ran
	if from.IsZero() {
		from = j.Started
	}
	if from.IsZero() || j.Failing == nil || j.Failing.At.Before(from) {
		return 0
	}
	return j.Failing.At.Sub(from)
}

// heldHere is what a gate in the lead's checkout holds as it runs, as the merge queue reads it (loop's
// Coordinator.gate): the phases of s's run whose work waits to land, and how many phases of the other live
// runs in the checkout do. Read with no lock.
func heldHere(s *state.Store) (landing []string, others int) {
	if st, err := s.Load(); err == nil {
		landing = st.LandingPhases()
	}
	for _, r := range s.ProjectOf().Others(s.Ref) {
		others += len(r.State.LandingPhases())
	}
	return landing, others
}

// takeBriefLookIns takes the look-ins due with a brief (takeLookIns), and leaves the line that tells the
// human of them in the run's announcements (Runtime.Announce), which the session's next hook shows: the
// hook that gives a brief says nothing of its own.
func takeBriefLookIns(st *state.State, now time.Time) lookIns {
	l := takeLookIns(st, now)
	if says := l.says(); says != "" {
		st.Run.Announce = append(st.Run.Announce, says)
	}
	return l
}

// briefLookIns adds to text, a brief, the look-ins taken with it (l), and logs them as given in it. It
// returns the brief as given.
func briefLookIns(s *state.Store, text string, l lookIns) string {
	if !l.any() {
		return text
	}
	l.event(s, "brief")
	return strings.TrimSpace(text + "\n\n" + l.text(s))
}
