package state

import (
	"fmt"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The scheduler picks the phase the run takes up whenever nothing is in hand: after a phase is done, or
// set aside. The plan's dependency graph says what can start (plan.Deps); a phase that was parked comes
// back as soon as what it waited for is over.

// Job is a command baton runs for a phase while the session works on, such as a verification gate. Its
// runner (`baton lane-run`, package lane) takes it from queued to running and records how it ended.
type Job struct {
	ID      string `json:"id"`
	Phase   string `json:"phase"`
	Kind    string `json:"kind,omitempty"` // JobVerify or JobAwait
	Command string `json:"command,omitempty"`
	Commit  string `json:"commit,omitempty"` // the commit a verify job checks, in a lane
	Notes   string `json:"notes,omitempty"`  // where the phase stood when the job was queued, in the model's words
	Status  string `json:"status"`
	Exit    int    `json:"exit,omitempty"`   // the command's exit code once it ended; -1 if it did not exit by itself
	Reason  string `json:"reason,omitempty"` // why, when the job ended without the command's own verdict
	Lane    int    `json:"lane,omitempty"`   // the lane it ran in
	Log     string `json:"log,omitempty"`
	PID     int    `json:"pid,omitempty"`  // the runner's
	PGID    int    `json:"pgid,omitempty"` // the process group of what the runner runs now (or ran last)

	Queued    time.Time `json:"queued,omitzero"`
	Spawned   time.Time `json:"spawned,omitzero"` // when the host started its runner
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	Heartbeat time.Time `json:"heartbeat,omitzero"` // the runner's, while the job runs
	// Delivered is when the model was given the job's result, or found to need none: the job was
	// cancelled, its phase was finished with it in hand, or its verification passed for a phase the run
	// had moved on from. Each result is given once.
	Delivered time.Time `json:"delivered,omitzero"`
}

// Job kinds: a verify job runs a gate on a commit, in a lane; an await job waits on something outside the
// project (a CI run, say) from the working tree, with no lane.
const (
	JobVerify = "verify"
	JobAwait  = "await"
)

// Job statuses: queued and running are unfinished; every other status is a result.
const (
	JobQueued      = "queued"
	JobRunning     = "running"
	JobPassed      = "passed"
	JobFailed      = "failed"
	JobLaneError   = "lane_error"
	JobTimeout     = "timeout"
	JobInterrupted = "interrupted"
)

// Finished reports whether the job has a result.
func (j Job) Finished() bool { return j.Status != JobQueued && j.Status != JobRunning }

// Red reports a verdict against what the job checked: its command failed, or ran out of time. A job that
// could not run its command (lane_error) or did not finish (interrupted) has no verdict.
func (j Job) Red() bool { return j.Status == JobFailed || j.Status == JobTimeout }

// Took is how long the job has run, or ran.
func (j Job) Took(now time.Time) time.Duration {
	switch {
	case j.Started.IsZero():
		return 0
	case j.Ended.IsZero():
		return now.Sub(j.Started)
	}
	return j.Ended.Sub(j.Started)
}

// MaxJobs is how many jobs a run keeps on record. Past it, the oldest finished jobs are dropped.
const MaxJobs = 30

// QueueJob queues j as its phase's job, numbered after every job the run has had. A phase has one job at
// a time: one it already has queued or running is cancelled and replaced by j. It returns the job as
// queued, the job it replaced (nil for none), and the finished jobs dropped from the record to keep it
// within MaxJobs.
func QueueJob(st *State, j Job, now time.Time) (queued Job, replaced *Job, dropped []Job) {
	n := st.JobSeq
	for _, o := range st.Jobs {
		var k int
		if _, err := fmt.Sscanf(o.ID, "j%d", &k); err == nil && k > n {
			n = k
		}
	}
	st.JobSeq = n + 1
	j.ID, j.Status, j.Queued = fmt.Sprintf("j%d", st.JobSeq), JobQueued, now
	if old := CancelJobs(st, func(o Job) bool { return o.Phase == j.Phase }, "replaced by "+j.ID, now); len(old) > 0 {
		replaced = &old[0]
	}
	st.Jobs = append(st.Jobs, j)
	for len(st.Jobs) > MaxJobs {
		i := slices.IndexFunc(st.Jobs, Job.Finished)
		if i < 0 {
			break
		}
		dropped = append(dropped, st.Jobs[i])
		st.Jobs = slices.Delete(st.Jobs, i, i+1)
	}
	return j, replaced, dropped
}

// CancelJobs cancels the jobs not finished yet that which picks, for reason: each is marked interrupted,
// and its runner, which beats into the state, stops its command and brings its lane down. It returns them
// as they were, with their runners' pids, for whoever must stop them sooner.
func CancelJobs(st *State, which func(Job) bool, reason string, now time.Time) []Job {
	var out []Job
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if j.Finished() || !which(*j) {
			continue
		}
		out = append(out, *j)
		j.Status, j.Exit, j.Reason, j.Ended, j.Delivered = JobInterrupted, -1, reason, now, now
	}
	return out
}

// Unfinished lists the jobs queued or running.
func (st State) Unfinished() []Job {
	var out []Job
	for _, j := range st.Jobs {
		if !j.Finished() {
			out = append(out, j)
		}
	}
	return out
}

// Latest is phase's latest job, or nil when it has none on record. A phase has one job at a time, so its
// latest is the one that says where its verification stands.
func (st State) Latest(phase string) *Job {
	for i := len(st.Jobs) - 1; i >= 0; i-- {
		if st.Jobs[i].Phase == phase {
			return &st.Jobs[i]
		}
	}
	return nil
}

// Red lists the phases whose latest job is red, by that job, oldest first. They share the branch with
// the phase in hand, so failures in their area are theirs.
func (st State) Red() []Job {
	var out []Job
	for _, j := range st.Jobs {
		if j.Red() && st.Latest(j.Phase).ID == j.ID {
			out = append(out, j)
		}
	}
	return out
}

// Due lists the results the model has not been given and should be given now: those of the phase in
// hand, or, once the plan is complete and before the human is told, of the verification that completed
// it. None is due while a compaction is owed or underway: the result comes after it.
func (st State) Due() []Job {
	switch {
	case st.BoundaryOwed || st.CheckpointOwed || st.CheckpointDue || st.Run.Compaction.InFlight():
		return nil
	case st.Mode == ModeComplete:
		if st.Run.CompleteNotified {
			return nil
		}
	case st.Mode != ModeRunning:
		return nil
	case st.Phases[st.Current] == nil || st.Phases[st.Current].Status != PhaseActive:
		return nil
	}
	var out []Job
	for _, j := range st.Jobs {
		if j.Finished() && j.Delivered.IsZero() && (st.Mode == ModeComplete || j.Phase == st.Current) {
			out = append(out, j)
		}
	}
	return out
}

// TakeDue returns the results due (Due) and records them as given.
func TakeDue(st *State, now time.Time) []Job {
	due := st.Due()
	for _, d := range due {
		if j := st.Job(d.ID); j != nil {
			j.Delivered = now
		}
	}
	return due
}

// Job returns the job with this id, or nil when there is none on record.
func (st State) Job(id string) *Job {
	for i := range st.Jobs {
		if st.Jobs[i].ID == id {
			return &st.Jobs[i]
		}
	}
	return nil
}

// Next is the phase the run takes up when nothing is in hand: a parked phase whose job has finished (the
// one parked longest first), else the first pending phase, in document order, whose dependencies are all
// done, else "": every phase left is verifying, parked while its job runs, or waits for one that is. In a
// run that never parked a phase this is the first phase not done, as phases have always run.
func Next(st State, p plan.Plan) string {
	next, since := "", time.Time{}
	for _, ph := range p.Phases {
		ps := st.Phases[ph.ID]
		if ps == nil || ps.Status != PhaseParked || !st.resolved(ps.Park) {
			continue
		}
		var at time.Time
		if ps.Park != nil {
			at = ps.Park.Since
		}
		if next == "" || at.Before(since) {
			next, since = ph.ID, at
		}
	}
	if next != "" {
		return next
	}
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; (ps == nil || ps.Status == PhasePending) && ready(st, p, ph.ID) {
			return ph.ID
		}
	}
	return ""
}

// ready reports whether every phase that id waits for is done. A verifying phase is not done yet: what
// builds on it waits for its verification to pass.
func ready(st State, p plan.Plan, id string) bool {
	for _, d := range p.Deps(id) {
		if ps := st.Phases[d]; ps == nil || ps.Status != PhaseDone {
			return false
		}
	}
	return true
}

// resolved reports whether a parked phase can be taken up again: the job it waits for has finished, or is
// no longer on record.
func (st State) resolved(pk *Park) bool {
	if pk == nil || pk.Job == "" {
		return true
	}
	j := st.Job(pk.Job)
	return j == nil || j.Finished()
}

// Complete reports a run with nothing left to do: every phase of p done, and no job queued or running.
func Complete(st State, p plan.Plan) bool {
	if len(Remaining(st, p)) > 0 {
		return false
	}
	for _, j := range st.Jobs {
		if !j.Finished() {
			return false
		}
	}
	return true
}

// advance takes up the next phase once the one in hand is done or set aside; it starts after a
// compaction, as at any phase boundary. With nothing that can start, the run is complete, or holds no
// phase until a job finishes. It returns the phase taken up, or "".
func advance(st *State, p plan.Plan) string {
	next := Next(*st, p)
	switch {
	case next != "":
		st.Current, st.BoundaryOwed = next, true
	case Complete(*st, p):
		st.Mode, st.Current, st.BoundaryOwed = ModeComplete, "", false
	default:
		st.Current, st.BoundaryOwed = "", false
	}
	return next
}

// Verdict is what a finished job decided about the phase that was verifying on it: the phase is done
// (Park ""), or reopened, parked for the reason Park gives.
type Verdict struct {
	Phase string
	Job   Job // the job as it ended; zero when none is on record
	Park  string
}

// Settled is what Settle changed.
type Settled struct {
	Verdicts []Verdict
	Taken    string // the phase the run took up, now that one can start ("" for none)
	Complete bool   // the run is complete now
}

// Changed reports whether Settle changed anything.
func (s Settled) Changed() bool { return len(s.Verdicts) > 0 || s.Taken != "" || s.Complete }

// Unsettled reports a run that Settle would move: a phase verifying on a job that has finished (or is
// gone from the record), or, with no phase in hand, a parked phase that can be taken up again or no job
// left to wait for.
func Unsettled(st State) bool {
	for id, ps := range st.Phases {
		if ps.Status == PhaseVerifying {
			if j := st.Latest(id); j == nil || j.Finished() {
				return true
			}
		}
	}
	if st.Mode != ModeRunning || st.Current != "" {
		return false
	}
	for _, ps := range st.Phases {
		if ps.Status == PhaseParked && st.resolved(ps.Park) {
			return true
		}
	}
	return len(st.Unfinished()) == 0
}

// Settle applies the verdicts of finished jobs to the phases verifying on them: a phase whose job passed
// is done; one whose job failed or ran out of time reopens, parked as red, and one whose job ended
// without a verdict (it could not run, or was interrupted) reopens as unverified. Either comes back
// before any phase not started yet (Next). With no phase in hand, the run then takes up the next one that
// can start, after a compaction, or is complete.
//
// The result of a verification that passed needs no telling, unless it completed the run: then it is how
// the model hears that the plan is complete (Due).
func Settle(st *State, p plan.Plan, now time.Time) Settled {
	var out Settled
	for _, ph := range p.Phases {
		ps := st.Phases[ph.ID]
		if ps == nil || ps.Status != PhaseVerifying {
			continue
		}
		j := st.Latest(ph.ID)
		if j != nil && !j.Finished() {
			continue
		}
		v := Verdict{Phase: ph.ID}
		if j != nil {
			v.Job = *j
		}
		switch {
		case j != nil && j.Status == JobPassed:
			ps.Status, ps.DoneAt = PhaseDone, now
		default:
			v.Park = ParkUnverified
			pk := Park{Reason: ParkUnverified, Since: now}
			if j != nil {
				if j.Red() {
					v.Park, pk.Reason = ParkRed, ParkRed
				}
				pk.Job, pk.Notes = j.ID, j.Notes
			}
			ps.Status, ps.Park = PhaseParked, &pk
		}
		out.Verdicts = append(out.Verdicts, v)
	}
	if len(out.Verdicts) > 0 {
		st.Run.Progress()
	}
	if st.Mode == ModeRunning && st.Current == "" {
		out.Taken = advance(st, p)
		out.Complete = st.Mode == ModeComplete
	}
	if !out.Complete {
		for _, v := range out.Verdicts {
			if j := st.Job(v.Job.ID); v.Park == "" && j != nil && j.Delivered.IsZero() {
				j.Delivered = now
			}
		}
	}
	return out
}

// ParkPhase sets phase id aside unfinished, with why and where it stood (pk). Only a phase that has started
// can be parked. If it was the phase in hand, the run takes up the next phase after a compaction, as at a
// phase boundary. It returns the phase in hand afterwards ("" for none).
func ParkPhase(st *State, p plan.Plan, id string, pk Park) (string, error) {
	if st.Mode == ModeIdle {
		return "", ErrNoPlan
	}
	if p.Index(id) < 0 {
		return "", fmt.Errorf("unknown phase %q (phases: %s)", id, phaseList(p))
	}
	ps := st.Phases[id]
	switch {
	case ps == nil || ps.Status == PhasePending:
		return "", fmt.Errorf("phase %s has not started, so there is nothing to set aside", id)
	case ps.Status == PhaseDone:
		return "", fmt.Errorf("phase %s is already done", id)
	}
	ps.Status, ps.Park = PhaseParked, &pk
	st.Run.Progress()
	if id != st.Current {
		return st.Current, nil
	}
	st.CheckpointOwed, st.CheckpointDue = false, false
	return advance(st, p), nil
}

// Resumed reports whether the phase in hand was set aside before and has been taken up again.
func (st State) Resumed() bool {
	ps := st.Phases[st.Current]
	return ps != nil && ps.Park != nil && !ps.Park.Resumed.IsZero()
}
