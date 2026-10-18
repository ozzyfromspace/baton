package state

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// Look-ins are what baton notices about a gate or a landing while it runs, told to the session it concerns
// with the first hook of its that can: information, never a question, each once. A gate's first failure is
// read from the job itself (Job.Failing, which its runner sets; FailTold once told, UntoldFailures). The
// rest the host finds about once a minute and leaves here (LookIn, AddLookIn): a gate past its history or
// near verify_timeout, landings held behind a gate in the checkout, a file in a second landing conflict;
// each once per job and kind (Job.LookedIn) or per file. Neither goes through the inbox (Mail), which drops
// what is about a phase the run is not on: after `baton done` a gate's phase is no longer in hand, and its
// owner still needs to hear what the gate does.

// Look-in kinds, as Job.LookedIn records them.
const (
	LookFailing  = "first_failure"    // a gate printed a failure, and runs on
	LookSlow     = "past_history"     // a gate runs well past its slowest earlier pass
	LookTimeout  = "near_timeout"     // a gate nears verify_timeout
	LookHeld     = "landings_held"    // landings wait behind a gate in the lead's checkout
	LookConflict = "conflicting_file" // a file conflicted in a second landing of the run
)

// LookIn is a look-in the host left for the session's model: what it is about, the text the model is
// given, and the line that tells the human it was.
type LookIn struct {
	Kind string `json:"kind"`
	// Job is the job it is about, if one: once that has ended, the look-in goes untold, as the job's result
	// says more.
	Job  string    `json:"job,omitempty"`
	Text string    `json:"text"`
	Say  string    `json:"say,omitempty"`
	At   time.Time `json:"at"`
}

// lookable reports whether the session can be given look-ins now: the plan runs, and no compaction is
// owed or underway (it would be lost in it, so it comes after, with the brief).
func (st State) lookable() bool { return st.Mode == ModeRunning && !st.Compacting() }

// UntoldFailures lists the jobs running whose first failure (Job.Failing) their owner has not been told of,
// whatever phase they are for, each with the failure it is told of (Failing). A failure in a stage run with
// --keep-going does not count, as the gate chose to collect every failure: a later stage that failed
// without it does, and with none the job's result tells. A stage says it ran with --keep-going only as it
// ends (lane.Stage), so a failure in a stage still running counts. One held for the advisor is left out
// until the host releases it (Advising).
func (st State) UntoldFailures() []Job {
	if !st.lookable() {
		return nil
	}
	var out []Job
	for _, j := range st.Jobs {
		if j.Status != JobRunning || j.Failing == nil || !j.FailTold.IsZero() || st.Advising(LookFailing, j.ID) {
			continue
		}
		if f, ok := toldFailure(j); ok {
			j.Failing = &f
			out = append(out, j)
		}
	}
	return out
}

// toldFailure is the failure of job j its owner is told of, and whether there is one (UntoldFailures): its
// first, unless that came in a stage that ended having run with --keep-going; then the first stage after it
// that failed without --keep-going, if one has ended.
func toldFailure(j Job) (Failing, bool) {
	f := *j.Failing
	if f.Stage == "" {
		return f, true
	}
	i := slices.IndexFunc(j.Stages, func(s lane.Stage) bool { return s.Name == f.Stage && !s.Ended.Before(f.At) })
	if i < 0 || !j.Stages[i].KeepGoing {
		return f, true
	}
	for _, s := range j.Stages[i+1:] {
		if s.Status == lane.StageFailed && !s.KeepGoing {
			line := "stage " + s.Name + " failed"
			if len(s.Failures) > 0 {
				line = s.Failures[0]
			}
			return Failing{At: s.Ended, Line: line, Stage: s.Name}, true
		}
	}
	return Failing{}, false
}

// LeftLookIns lists the look-ins the host left that the session can be given now: those about a job that has
// ended are left out, and so are those held for the advisor (Advising).
func (st State) LeftLookIns() []LookIn {
	if !st.lookable() {
		return nil
	}
	var out []LookIn
	for _, li := range st.LookIns {
		if li.Job == "" || st.jobRuns(li.Job) && !st.Advising(li.Kind, li.Job) {
			out = append(out, li)
		}
	}
	return out
}

// jobRuns reports whether job id is on record and not finished.
func (st State) jobRuns(id string) bool {
	j := st.Job(id)
	return j != nil && !j.Finished()
}

// LookInsDue reports whether the session has a look-in to be given now (TakeLookIns).
func (st State) LookInsDue() bool { return len(st.UntoldFailures()) > 0 || len(st.LeftLookIns()) > 0 }

// TakeLookIns returns the look-ins the session is to be given now, and records them as given: the first
// failures of its gates that run on (UntoldFailures; FailTold, and LookFailing among the job's LookedIn),
// and the look-ins the host left (State.LookIns). Those about a job that has ended are dropped untold. Those
// held for the advisor stay, unless their hold is past its deadline (LapseAdvice); the advisor's words on
// those given are TakeAdvice's.
func TakeLookIns(st *State, now time.Time) (failing []Job, left []LookIn) {
	if !st.lookable() {
		return nil, nil
	}
	LapseAdvice(st, now)
	failing, left = st.UntoldFailures(), st.LeftLookIns()
	for _, f := range failing {
		if j := st.Job(f.ID); j != nil {
			j.FailTold = now
			if !slices.Contains(j.LookedIn, LookFailing) {
				j.LookedIn = append(j.LookedIn, LookFailing)
			}
		}
	}
	st.LookIns = slices.DeleteFunc(st.LookIns, func(li LookIn) bool { return li.Job == "" || !st.Advising(li.Kind, li.Job) })
	if len(st.LookIns) == 0 {
		st.LookIns = nil
	}
	return failing, left
}

// AddLookIn leaves li for the session's model, once per job and kind (Job.LookedIn), and reports whether
// it did: one about a job that is not running, or that was told of li's kind before, is not left. One
// about no job is always left.
func AddLookIn(st *State, li LookIn) bool {
	if li.Job != "" {
		j := st.Job(li.Job)
		if j == nil || j.Finished() || slices.Contains(j.LookedIn, li.Kind) {
			return false
		}
		j.LookedIn = append(j.LookedIn, li.Kind)
	}
	st.LookIns = append(st.LookIns, li)
	return true
}

// CountConflict records a landing of phase that conflicted with the run's branch in paths (Conflicted),
// and returns the paths for which it is the second conflict in the run: each once, as a third says nothing
// a second did not.
func CountConflict(st *State, phase string, paths []string) (second []string) {
	if len(paths) == 0 {
		return nil
	}
	if st.Conflicted == nil {
		st.Conflicted = map[string][]string{}
	}
	for _, p := range paths {
		st.Conflicted[p] = append(st.Conflicted[p], phase)
		if len(st.Conflicted[p]) == 2 {
			second = append(second, p)
		}
	}
	return second
}

// LandingPhases lists the phases of the run whose work waits in its merge queue (WorkerLanding), in roster
// order: what a gate in the lead's checkout holds as it runs.
func (st State) LandingPhases() []string {
	var out []string
	for _, w := range st.Workers {
		if w.Status == WorkerLanding {
			out = append(out, w.Phase)
		}
	}
	return out
}
