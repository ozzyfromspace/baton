package state

import (
	"slices"
	"time"
)

// baton's reviewer of the model's own background shells (v0.8.21, loop/tasks.go). A signal that a shell may
// do nothing more for the run (Suspects) never stops it: the host asks a model (review_model, Sonnet at high
// effort by default) to judge each suspect from the evidence, and acts only on its verdict. A STOP verdict
// stops the shell (TaskStop through baton's mod, or its process group where that cannot); KEEP names when to
// look again; UNSURE, or no verdict at all, asks the task's owner to look, tells the human, and stops nothing.
// So uncertainty cannot loop: one review in flight per task, a review only when a kind of signal no review
// has seen appears or a KEEP's time comes, and MaxReviews in all. What came of each review is kept here, by
// task id, in the run of the loop whose task it is (Runtime.Reviews), as the session's: it outlives a plan.

// The reviewer's verdicts (TaskReview.Verdict).
const (
	ReviewStop   = "stop"   // the evidence shows the task can do nothing more for the run: it is stopped
	ReviewKeep   = "keep"   // it may be honest work: looked at again at TaskReview.Again
	ReviewUnsure = "unsure" // the reviewer could not tell: its owner is asked to look, nothing is stopped
	ReviewNone   = "none"   // no verdict (the reviewer off, no mod, past its deadline, an API error): as unsure
)

// MaxReviews is how many reviews a task gets in all. One still running after them is named once
// (TaskReview.Long) and left alone.
const MaxReviews = 3

// How a stopped task ended (TaskStop.Via).
const (
	StopViaTask    = "task_stop" // Claude Code's TaskStop, which baton's mod called
	StopViaProcess = "process"   // baton ended the shell's process group itself
)

// TaskReview is what baton made of one background shell of the loop's own that was a suspect, by its task
// id (Runtime.Reviews): how many reviews it was given, the kinds of signal they were given on (Seen), the
// review in flight (Asking), the last verdict and its reason, when a KEEP looks again, the stop under way or
// done, and what was said once: its owner asked to look (Asked), the human told it is a suspect (Told), and
// that it is long-running work left alone (Long). A task that was never a suspect has an entry only once it
// is named as long-running.
type TaskReview struct {
	Reviews int       `json:"reviews,omitempty"`
	Seen    []string  `json:"seen,omitempty"`
	Asking  time.Time `json:"asking,omitzero"`
	Verdict string    `json:"verdict,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Model   string    `json:"model,omitempty"`
	At      time.Time `json:"at,omitzero"`
	Again   time.Time `json:"again,omitzero"`
	Stop    *TaskStop `json:"stop,omitempty"`
	Asked   bool      `json:"asked,omitempty"`
	Told    bool      `json:"told,omitempty"`
	Long    bool      `json:"long,omitempty"`
}

// TaskStop is the stop of a task on the reviewer's STOP verdict: when it was decided, why (the reviewer's
// reason), what names the task, the mod's stop command (0: none issued), when baton ended its process group itself where that did
// not (Killed), and how it ended (Via, at Ended), or why it could not be stopped (Failed).
type TaskStop struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
	// What names the task once its start record is gone (a Stop no longer lists it): its description, the
	// phase it started in, the command it ran (Shell), its output file, and when it started.
	Description string    `json:"description,omitempty"`
	Phase       string    `json:"phase,omitempty"`
	Shell       string    `json:"shell,omitempty"`
	Output      string    `json:"output,omitempty"`
	Started     time.Time `json:"started,omitzero"`
	Command     int       `json:"command,omitempty"`
	Killed      time.Time `json:"killed,omitzero"`
	Via         string    `json:"via,omitempty"`
	Ended       time.Time `json:"ended,omitzero"`
	Failed      string    `json:"failed,omitempty"`
}

// Settled reports a stop that is over: the task ended, or could not be stopped.
func (s *TaskStop) Settled() bool { return s != nil && (!s.Ended.IsZero() || s.Failed != "") }

// Triggers are the kinds of signal that can call for a review by themselves: all but SignalIdle, since a
// process tree idle for a minute is common in honest work (a server waiting for requests, a sleep in a test)
// and only adds to the evidence.
func Triggers(s Suspect) []string {
	var out []string
	for _, g := range s.Signals {
		if g.Why != SignalIdle && !slices.Contains(out, g.Why) {
			out = append(out, g.Why)
		}
	}
	return out
}

// Due reports whether suspect s, whose record so far is r (nil: none), is due a review at now, and the kinds
// of signal it is given on: none while a review is in flight, once a stop was decided, or after MaxReviews;
// otherwise a kind of signal no review has seen (Triggers), or the time a KEEP named.
func (r *TaskReview) Due(s Suspect, now time.Time) (bool, []string) {
	kinds := Triggers(s)
	if r == nil {
		return len(kinds) > 0, kinds
	}
	if !r.Asking.IsZero() || r.Stop != nil || r.Reviews >= MaxReviews {
		return false, nil
	}
	fresh := slices.DeleteFunc(slices.Clone(kinds), func(k string) bool { return slices.Contains(r.Seen, k) })
	switch {
	case len(fresh) > 0:
		return true, kinds
	case r.Verdict == ReviewKeep && !r.Again.IsZero() && !now.Before(r.Again) && len(kinds) > 0:
		return true, kinds
	}
	return false, nil
}

// ReviewOf returns the record of task id in the loop's run, made if there is none.
func (r *Runtime) ReviewOf(id string) *TaskReview {
	if r.Reviews == nil {
		r.Reviews = map[string]*TaskReview{}
	}
	if r.Reviews[id] == nil {
		r.Reviews[id] = &TaskReview{}
	}
	return r.Reviews[id]
}

// AskReview records that a review of task id is asked at now on kinds of signal: in flight, and counted.
// It reports false when one is in flight already, a stop was decided, or MaxReviews were given.
func (r *Runtime) AskReview(id string, kinds []string, now time.Time) bool {
	rv := r.ReviewOf(id)
	if !rv.Asking.IsZero() || rv.Stop != nil || rv.Reviews >= MaxReviews {
		return false
	}
	rv.Asking, rv.Reviews = now, rv.Reviews+1
	for _, k := range kinds {
		if !slices.Contains(rv.Seen, k) {
			rv.Seen = append(rv.Seen, k)
		}
	}
	return true
}

// Reviewed records the verdict on the review of task id asked at asked: its reason, the model, and for a
// KEEP when to look again (again). A STOP starts the task's stop (TaskStop). It reports false when no such
// review is in flight: an answer for a review given up on.
func (r *Runtime) Reviewed(id string, asked time.Time, verdict, reason, model string, again time.Time, now time.Time) bool {
	rv := r.Reviews[id]
	if rv == nil || rv.Asking.IsZero() || !rv.Asking.Equal(asked) {
		return false
	}
	rv.Asking, rv.Verdict, rv.Reason, rv.Model, rv.At, rv.Again = time.Time{}, verdict, reason, model, now, time.Time{}
	switch verdict {
	case ReviewKeep:
		rv.Again = again
	case ReviewStop:
		rv.Stop = &TaskStop{At: now, Reason: reason}
	}
	return true
}

// ForgetReviews drops the records of the tasks the loop no longer has (keep: the task ids it has), but a stop
// not settled: a task that ended needs none.
func (r *Runtime) ForgetReviews(keep func(id string) bool) {
	for id, rv := range r.Reviews {
		if !keep(id) && (rv.Stop == nil || rv.Stop.Settled()) {
			delete(r.Reviews, id)
		}
	}
	if len(r.Reviews) == 0 {
		r.Reviews = nil
	}
}

// Why a look at the loop's background work is owed at once (TaskAudit.Why), beside the host's look every
// minute: a plan attached, a run resumed or carried on, a phase boundary of the lead's, a phase agent let go
// of, and a restart that background work holds.
const (
	AuditAttach   = "attach"
	AuditResume   = "resume"
	AuditCarry    = "carry"
	AuditBoundary = "boundary"
	AuditRetire   = "retire"
	AuditRestart  = "restart"
)

// TaskAudit is a look at the loop's background work that is owed at once (Runtime.Audit): why, each once,
// and since when. The host makes it and logs background_audited; nothing on the paths that owe it waits.
type TaskAudit struct {
	Why []string  `json:"why"`
	At  time.Time `json:"at"`
}

// OweAudit owes a look at the loop's background work at once, for why.
func (r *Runtime) OweAudit(why string, now time.Time) {
	if r.Audit == nil {
		r.Audit = &TaskAudit{At: now}
	}
	if !slices.Contains(r.Audit.Why, why) {
		r.Audit.Why = append(r.Audit.Why, why)
	}
}

// TakeAudit settles the look owed for the reasons in why, made by the host: a reason owed since stays owed.
func (r *Runtime) TakeAudit(why []string) {
	if r.Audit == nil {
		return
	}
	r.Audit.Why = slices.DeleteFunc(r.Audit.Why, func(w string) bool { return slices.Contains(why, w) })
	if len(r.Audit.Why) == 0 {
		r.Audit = nil
	}
}
