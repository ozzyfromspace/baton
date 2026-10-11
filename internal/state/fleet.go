package state

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A run can work on several phases at once. The lead session works on the phase in hand (Current) in
// the project's checkout, as a run always has. Worker sessions each take another phase that can go
// ahead, in a git worktree and a sub-run of their own (Project.NewWorker). The run sees a worker's phase
// go only from working to done: everything in between belongs to the sub-run, and the phase is done once
// its work has landed on the run's branch (Landed), so the phases that need it wait until then. The
// host's coordinator writes the fields below and keeps them in step with the sessions it runs
// (docs/research/workers.md).

// Worker statuses. A worker goes starting → working → landing → landed. Two statuses are off that path:
// suspended is a worker whose session ended with its host (exit, a hang-up), which the next host for the
// run resumes; failed is one that could not go on, whose phase went back to the scheduler (Unassign).
const (
	WorkerStarting  = "starting"  // its session is launched, and has not sent its first prompt yet
	WorkerWorking   = "working"   // its session works on the phase
	WorkerLanding   = "landing"   // its sub-run is complete, and its work waits in the merge queue
	WorkerLanded    = "landed"    // its work is on the run's branch, and its phase is done
	WorkerSuspended = "suspended" // its session ended with the host
	WorkerFailed    = "failed"    // its phase was taken back
)

// Worker is one worker session on the run's roster: the phase it works on, and where.
type Worker struct {
	Phase    string `json:"phase"`
	Ref      string `json:"ref"`      // its sub-run (WorkerRef)
	Session  string `json:"session"`  // its Claude Code session id, picked by baton before launch
	Instance string `json:"instance"` // its BATON_INSTANCE (WorkerInstance)
	Worktree string `json:"worktree"` // its git worktree, outside the checkout
	Branch   string `json:"branch"`   // its own branch in that worktree
	Base     string `json:"base"`     // the commit of the run's branch it started from
	Status   string `json:"status"`
	Why      string `json:"why,omitempty"` // why it is suspended or failed
	// Assigned is when it was given its phase, Since when it took its status, Started when its session
	// sent its first prompt, and Ended when its session last ended.
	Assigned time.Time `json:"assigned"`
	Since    time.Time `json:"since"`
	Started  time.Time `json:"started,omitzero"`
	Ended    time.Time `json:"ended,omitzero"`
	// Resumes counts the times its session was resumed after it ended unexpectedly.
	Resumes int `json:"resumes,omitempty"`
	// Unlanded counts the commits on its branch that never landed, once it has failed: its branch is
	// kept for them.
	Unlanded int    `json:"unlanded,omitempty"`
	Merge    *Merge `json:"merge,omitempty"`
}

// Live reports a worker the run still counts on: one not landed and not failed.
func (w Worker) Live() bool { return w.Status != WorkerLanded && w.Status != WorkerFailed }

// Merge is where a worker's work stands in the merge queue.
type Merge struct {
	Queued time.Time `json:"queued"`
	Tries  int       `json:"tries,omitempty"`
	// Rebased is the branch's tip after its latest rebase, onto Onto, the tip of the run's branch then.
	Rebased string `json:"rebased,omitempty"`
	Onto    string `json:"onto,omitempty"`
	// Deferred is why the latest attempt to land waits (such as the lead's own changes to the same
	// files), since DeferredAt.
	Deferred   string    `json:"deferred,omitempty"`
	DeferredAt time.Time `json:"deferred_at,omitzero"`
	// Reverify is the sub-run's job verifying the rebased branch again, since the run's branch moved on
	// from the commit its verification passed on.
	Reverify string `json:"reverify,omitempty"`
}

// Landing is a phase's work landed on the run's branch.
type Landing struct {
	Phase   string    `json:"phase"`
	Commits []string  `json:"commits"`         // oldest first
	Files   []string  `json:"files,omitempty"` // the paths they changed
	Head    string    `json:"head"`            // the run's branch once they landed
	At      time.Time `json:"at"`
	Told    bool      `json:"told,omitempty"` // the lead was told
}

// Usage is how much of the account's five-hour allowance is used, as a session's status line last
// reported it (At). Limited is set when a session hit the limit (By: "lead", or a worker's phase), at Hit:
// no worker starts, and nothing is typed into the run's sessions, until then.
type Usage struct {
	UsedPct float64   `json:"used_pct"`
	Resets  time.Time `json:"resets_at,omitzero"`
	At      time.Time `json:"at"`
	Limited time.Time `json:"limited_until,omitzero"`
	Hit     time.Time `json:"hit,omitzero"`
	By      string    `json:"by,omitempty"`
}

// LimitHit is when the session's last turn ended on the account's usage limit: zero if it did not, or
// if a turn has started since.
func (r Runtime) LimitHit() time.Time {
	if e := r.LastError; e != nil && e.Error == "rate_limit" && !r.TurnStarted.After(e.At) {
		return e.At
	}
	return time.Time{}
}

// Limit records that a session of the run (by) hit the account's usage limit at at, which holds the run
// until until. A limit hit no later than the one recorded is that one: Limit reports whether this one was
// new.
func Limit(st *State, by string, at, until time.Time) bool {
	var u Usage
	if st.Usage != nil {
		u = *st.Usage
	}
	if !at.After(u.Hit) {
		return false
	}
	u.Hit, u.By, u.Limited = at, by, until
	st.Usage = &u
	return true
}

// AttachWorker is the state a worker's sub-run starts from, before its session does: phase is in hand,
// in the run's plan narrowed to it (plan.Only), and becomes active when the worker's brief is delivered
// (Start). Its decisions and jobs are numbered under the phase (P3.d1, P3.j2), so that no id is used
// twice across the run. Like Reattach, it keeps prev's owner, and its job numbers.
func AttachWorker(prev State, phase string) State {
	st := New()
	st.Owner, st.JobSeq, st.IDPrefix = prev.Owner, prev.JobSeq, phase+"."
	st.Mode, st.Current = ModeRunning, phase
	st.Phases[phase] = &PhaseState{Status: PhasePending}
	return st
}

// NextWorker is the phase a new worker would take up: the first pending phase, in document order, whose
// dependencies are all done, other than the lead's. The lead's is the phase in hand or, when it holds
// none, the one it takes up next (Next). A parked phase is never a worker's: it was started in the
// lead's checkout. "" when there is none.
func NextWorker(st State, p plan.Plan) string {
	lead := st.Current
	if lead == "" {
		lead = Next(st, p)
	}
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ph.ID != lead && (ps == nil || ps.Status == PhasePending) && ready(st, p, ph.ID) {
			return ph.ID
		}
	}
	return ""
}

// WorkerOn is the roster entry of the live worker on phase, or nil when it has none.
func (st *State) WorkerOn(phase string) *Worker {
	for i := len(st.Workers) - 1; i >= 0; i-- {
		if w := &st.Workers[i]; w.Phase == phase && w.Live() {
			return w
		}
	}
	return nil
}

// LiveWorkers lists the workers the run still counts on (Worker.Live), in the order they were assigned.
func (st State) LiveWorkers() []Worker {
	var out []Worker
	for _, w := range st.Workers {
		if w.Live() {
			out = append(out, w)
		}
	}
	return out
}

// Assign gives w.Phase to worker w, which joins the roster, starting: the phase is working, and stays
// so until its work lands (Landed) or it is taken back (Unassign). Only a pending phase that can start,
// and is not the lead's, can be assigned (NextWorker), and only while the plan runs. Assigning a phase
// to the worker that has it already changes nothing.
func Assign(st *State, p plan.Plan, w Worker, now time.Time) error {
	id := w.Phase
	if cur := st.WorkerOn(id); cur != nil && cur.Session == w.Session && cur.Ref == w.Ref {
		return nil
	}
	ps := st.Phases[id]
	switch {
	case st.Mode != ModeRunning:
		return fmt.Errorf("workers start only while a plan runs (mode: %s)", st.Mode)
	case p.Index(id) < 0:
		return fmt.Errorf("unknown phase %q (phases: %s)", id, phaseList(p))
	case w.Ref == "" || w.Session == "":
		return errors.New("a worker needs its sub-run and its session")
	case st.WorkerOn(id) != nil:
		return fmt.Errorf("phase %s has a worker already", id)
	case id == st.Current:
		return fmt.Errorf("phase %s is the lead's", id)
	case ps != nil && ps.Status != PhasePending:
		return fmt.Errorf("phase %s is %s, not pending", id, ps.Status)
	case !ready(*st, p, id):
		return fmt.Errorf("phase %s waits for phases not done yet", id)
	}
	w.Status, w.Assigned, w.Since, w.Why = WorkerStarting, now, now, ""
	st.Phases[id] = &PhaseState{Status: PhaseWorking, StartedAt: now, Worker: w.Ref}
	st.Workers = append(st.Workers, w)
	return nil
}

// Unassign takes phase back from its worker, which cannot go on (why): the worker has failed, and the
// phase, if the plan still has it, is pending again for the lead or another worker. Whatever the worker
// committed stays on its branch. With the lead holding no phase, the lead takes up the next one after a
// boundary compaction this queues (as Settle does), and Unassign returns it ("" for none).
func Unassign(st *State, p plan.Plan, phase, why string, now time.Time) (string, error) {
	w := st.WorkerOn(phase)
	ps := st.Phases[phase]
	working := ps != nil && ps.Status == PhaseWorking
	if w == nil && !working {
		return "", fmt.Errorf("phase %s has no worker", phase)
	}
	if w != nil {
		w.Status, w.Since, w.Why, w.Merge = WorkerFailed, now, why, nil
	}
	if !working {
		return "", nil
	}
	st.Phases[phase] = &PhaseState{Status: PhasePending}
	return takeUp(st, p, now), nil
}

// Landed records that l.Phase's work landed on the run's branch: the phase is done, its worker has
// landed, and the phases that need it may start. With the lead holding no phase, the lead takes up the
// next one after a boundary compaction this queues (as Settle does), or the run is complete; Landed
// returns the phase taken up ("" for none). Recording the same landing again changes nothing.
func Landed(st *State, p plan.Plan, l Landing, now time.Time) (string, error) {
	ps := st.Phases[l.Phase]
	switch {
	case ps != nil && ps.Status == PhaseDone && st.landed(l.Phase):
		return "", nil
	case ps == nil || ps.Status != PhaseWorking:
		return "", fmt.Errorf("phase %s is not a worker's", l.Phase)
	}
	ps.Status, ps.DoneAt = PhaseDone, now
	if w := st.WorkerOn(l.Phase); w != nil {
		w.Status, w.Since, w.Merge = WorkerLanded, now, nil
	}
	if l.At.IsZero() {
		l.At = now
	}
	st.Landed = append(st.Landed, l)
	st.Run.Progress()
	return takeUp(st, p, now), nil
}

// TakeLandings returns the landings the lead has not been told about, and records them as told. None is
// told while a compaction is owed or underway: the lead hears of them after it.
func TakeLandings(st *State) []Landing {
	if st.compacting() {
		return nil
	}
	var out []Landing
	for i := range st.Landed {
		if l := &st.Landed[i]; !l.Told {
			l.Told = true
			out = append(out, *l)
		}
	}
	return out
}

// LandedCommits lists every commit the run's workers landed.
func (st State) LandedCommits() []string {
	var out []string
	for _, l := range st.Landed {
		out = append(out, l.Commits...)
	}
	return out
}

// Landing reports a worker whose work waits in the merge queue.
func (st State) Landing() bool {
	return slices.ContainsFunc(st.Workers, func(w Worker) bool { return w.Status == WorkerLanding })
}

func (st State) landed(phase string) bool {
	for _, l := range st.Landed {
		if l.Phase == phase {
			return true
		}
	}
	return false
}

// takeUp gives the lead the next phase when the plan runs and the lead holds none. The phase starts after
// a boundary compaction this queues, since the lead may be idle and no stop of its own would queue it.
// With nothing left to do, the run is complete. It returns the phase taken up ("" for none).
func takeUp(st *State, p plan.Plan, now time.Time) string {
	if st.Mode != ModeRunning || st.Current != "" {
		return ""
	}
	next := advance(st, p)
	if next != "" {
		st.Run.Compaction.Queue("boundary", now)
	}
	return next
}

// The merge queue lands a worker's phase from its sub-run, which its `baton done` completed. Two things
// send the phase back there: the run's branch moved on since its verification passed, so it is verified
// again on its rebased branch (Reverify), or its work cannot land as it is and the worker must act
// (Reopen).

// Reverify verifies a worker's finished phase again, in its sub-run st, with j: the verification that
// passed, run on the commit its branch was rebased to. The phase is verifying, as after `baton done`
// with a verification running, so the sub-run completes again once j passes (without a word to the
// worker, which was told its phase is done), or takes the phase up again, red, as after any
// verification that fails. It returns j as queued.
func Reverify(st *State, j Job, now time.Time) (Job, error) {
	ps := st.Phases[j.Phase]
	if st.Mode != ModeComplete || ps == nil || ps.Status != PhaseDone {
		return Job{}, fmt.Errorf("phase %s is not a finished worker's", j.Phase)
	}
	j.Kind = JobVerify
	q, _, _ := QueueJob(st, j, now)
	ps.Status, ps.DoneAt = PhaseVerifying, time.Time{}
	st.Mode, st.Current = ModeRunning, ""
	st.Run.CompleteNotified = true
	return q, nil
}

// Reopen takes a worker's finished phase up again in its sub-run st: its work could not land, for the
// reason m gives the worker's model. The phase is in hand and active, as it was before `baton done`, and
// m waits for the worker's next hook (Tell). Reopening a phase already reopened only leaves m.
func Reopen(st *State, phase string, m Message, now time.Time) error {
	ps := st.Phases[phase]
	switch {
	case ps == nil:
		return fmt.Errorf("unknown phase %q", phase)
	case st.Mode == ModeRunning && st.Current == phase && ps.Status == PhaseActive:
	case st.Mode != ModeComplete || ps.Status != PhaseDone:
		return fmt.Errorf("phase %s is not a finished worker's", phase)
	default:
		ps.Status, ps.DoneAt = PhaseActive, time.Time{}
		st.Mode, st.Current, st.BoundaryOwed = ModeRunning, phase, false
		st.Run.CompleteNotified = false
		st.Run.Progress()
	}
	Tell(st, m, now)
	return nil
}
