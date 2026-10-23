package state

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
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
	Commit  string `json:"commit,omitempty"`  // the commit a verify job checks, in a lane, or checked out where it runs (Here)
	Here    bool   `json:"here,omitempty"`    // a verify job run in the session's own working tree, with no lane (HereGate)
	Requeue bool   `json:"requeue,omitempty"` // a verify job the merge queue runs again on a worker's merged branch (Reverify)
	Notes   string `json:"notes,omitempty"`   // where the phase stood when the job was queued, in the model's words
	QA      string `json:"qa,omitempty"`      // why a full check runs ahead of the plan's QA phase (--qa), in the model's words
	Why     string `json:"why,omitempty"`     // what this run can show that the last could not (--why), in the model's words
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
	// LaneWait is since when the job's runner has waited for a lane, every lane being in use; zero once it
	// holds one, or while it waits for none.
	LaneWait time.Time `json:"lane_wait,omitzero"`
	// Delivered is when the model was given the job's result, or found to need none: the job was
	// cancelled, its phase was finished with it in hand, or its verification passed for a phase the run
	// had moved on from. Each result is given once.
	Delivered time.Time `json:"delivered,omitzero"`

	// Replaces is the red gate of another command this gate was queued to stand in for (--replace, RedGate):
	// its phase's gate from then on, as a decision the human is told of.
	Replaces string `json:"replaces,omitempty"`
	// Reruns is the red gate this job reruns (verify --failed): its stages that passed are carried, its
	// failed ones run narrowed to what failed, and a pass takes over from it.
	Reruns string `json:"reruns,omitempty"`

	// Ran is when the job got its lane (or needed none) and its command began: what it waited for a lane
	// before is not its running time.
	Ran time.Time `json:"ran,omitzero"`
	// Stages are the stages of the gate that have ended so far (the gate protocol, lane.Stage), oldest first.
	Stages []lane.Stage `json:"stages,omitempty"`
	// Failing is the first failure seen in the job's output while it ran; FailTold is when its owner was
	// told of it, once.
	Failing  *Failing  `json:"failing,omitempty"`
	FailTold time.Time `json:"fail_told,omitzero"`
	// LookedIn are the kinds of look-in already sent about the job, each once.
	LookedIn []string `json:"looked_in,omitempty"`
	// Unexplained is the line the human was told of a long gate queued again over baton's question with no
	// --why (decide.RepeatUnexplained); its result opens with it.
	Unexplained string `json:"unexplained,omitempty"`
	// Narrows are the failures of the gate it reruns (Reruns) that a narrowed rerun names: of a gate with no
	// stages, rerun by a command of the model's that names each of them (verify --failed -- <command>).
	Narrows []string `json:"narrows,omitempty"`

	// QAPhase: the job is a QA phase's (plan.TypeQA), whose gate runs every stage (lane.EnvQAPhase), to
	// collect every red at once.
	QAPhase bool `json:"qa_phase,omitempty"`
}

// Failing is the first failure a job's output showed while it ran: a failure line (lane.Failure), a fail
// marker, or a stage that failed.
type Failing struct {
	At    time.Time `json:"at"`
	Line  string    `json:"line"`
	Stage string    `json:"stage,omitempty"` // the stage it failed in, if the gate names its stages
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

// MaxJobs is how many jobs a run keeps on record. Past it, the oldest finished jobs are dropped, apart from
// those a red verification stands on (droppable).
const MaxJobs = 30

// kind is the job's kind: JobVerify or JobAwait (a job recorded without one is a verify).
func (j Job) kind() string {
	if j.Kind == JobAwait {
		return JobAwait
	}
	return JobVerify
}

// Same reports whether j runs the same check as o: a job of the same kind, with the same command (and,
// for a verify, the same --here). Queued again, it is the gate again, on a newer commit or as a wait
// again. A rerun of a red gate (verify --failed, Job.Reruns) has the gate's command, and its --here unless
// asked to run there, so it is the same check as the gate, and as any other run of its command.
func (j Job) Same(o Job) bool {
	return j.kind() == o.kind() && j.Command == o.Command && j.Here == o.Here
}

// QueueJob queues j as its phase's job of its kind, numbered after every job the run has had (under its
// IDPrefix). A phase has one job of each kind at a time, a verify and an await, which run side by side:
// its job of j's kind already queued or running is cancelled and replaced by j, and one of the other kind
// is left alone. Whether a different check may replace the one that runs is the caller's to ask first
// (Clash). It returns the job as queued, the job it replaced (nil for none), and the finished jobs dropped
// from the record to keep it within MaxJobs: never one whose phase would stand on another red job without
// it (droppable). A red job dropped keeps its log (KeptLogs).
func QueueJob(st *State, j Job, now time.Time) (queued Job, replaced *Job, dropped []Job) {
	n := st.JobSeq
	for _, o := range st.Jobs {
		var k int
		if _, err := fmt.Sscanf(strings.TrimPrefix(o.ID, st.IDPrefix), "j%d", &k); err == nil && k > n {
			n = k
		}
	}
	st.JobSeq = n + 1
	j.ID, j.Status, j.Queued = fmt.Sprintf("%sj%d", st.IDPrefix, st.JobSeq), JobQueued, now
	if old := CancelJobs(st, func(o Job) bool { return o.Phase == j.Phase && o.kind() == j.kind() }, "replaced by "+j.ID, now); len(old) > 0 {
		replaced = &old[0]
	}
	st.Jobs = append(st.Jobs, j)
	for len(st.Jobs) > MaxJobs {
		i := slices.IndexFunc(st.Jobs, func(o Job) bool { return o.Finished() && st.droppable(o) })
		if i < 0 {
			break
		}
		if o := st.Jobs[i]; o.Red() {
			st.KeptLogs = append(st.KeptLogs, o.ID)
		}
		dropped = append(dropped, st.Jobs[i])
		st.Jobs = slices.Delete(st.Jobs, i, i+1)
	}
	return j, replaced, dropped
}

// droppable reports whether job o can leave the record without changing the red jobs its phase stands on
// (Standing): it is none of them, and none stands in its place once it is gone (a red gate an older check
// of the same command had cleared, say). A passing job its phase stands on may go, as it always could.
func (st State) droppable(o Job) bool {
	reds := func(st State) []string {
		var out []string
		for _, j := range st.Standing(o.Phase) {
			if j.Red() {
				out = append(out, j.ID)
			}
		}
		return out
	}
	rest := st
	rest.Jobs = slices.DeleteFunc(slices.Clone(st.Jobs), func(x Job) bool { return x.ID == o.ID })
	return slices.Equal(reds(st), reds(rest))
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

// Clash is the job of phase j.Phase, of j's kind, that queuing j would stop although it runs another check
// (Same): queued or running, with another command, or another --here. nil when there is none, or it is the
// same check again. A different check is the model's to choose to stop (--replace): it may want both.
func (st State) Clash(j Job) *Job {
	for i := range st.Jobs {
		if o := &st.Jobs[i]; o.Phase == j.Phase && o.kind() == j.kind() && !o.Finished() && !o.Same(j) {
			return o
		}
	}
	return nil
}

// Beside is the job of phase j.Phase of the other kind than j's, queued or running: the one j runs beside.
// nil when there is none.
func (st State) Beside(j Job) *Job {
	for i := range st.Jobs {
		if o := &st.Jobs[i]; o.Phase == j.Phase && o.kind() != j.kind() && o.ID != j.ID && !o.Finished() {
			return o
		}
	}
	return nil
}

// Latest is phase's latest job of either kind, or nil when it has none on record: what ran last. Where
// the phase's verification stands is its jobs of each kind (Standing).
func (st State) Latest(phase string) *Job {
	for i := len(st.Jobs) - 1; i >= 0; i-- {
		if st.Jobs[i].Phase == phase {
			return &st.Jobs[i]
		}
	}
	return nil
}

// LatestOf is phase's latest job of kind, or nil when it has none on record.
func (st State) LatestOf(phase, kind string) *Job {
	for i := len(st.Jobs) - 1; i >= 0; i-- {
		if j := &st.Jobs[i]; j.Phase == phase && j.kind() == kind {
			return j
		}
	}
	return nil
}

// Standing is what phase's verification stands on: its gate (gate) and its latest wait on something
// outside, in that order; none, one or two. A local gate and a wait on something outside check different
// things, so the phase is done once both passed, and red while either is.
func (st State) Standing(phase string) []Job {
	var out []Job
	if j := st.gate(phase, false); j != nil {
		out = append(out, *j)
	}
	if j := st.LatestOf(phase, JobAwait); j != nil {
		out = append(out, *j)
	}
	return out
}

// gate is phase's gate, the verify job its verification stands on: its latest, unless a gate before it is
// red and it runs another check. A red gate stands until a gate that takes over from it (takesOver) is
// queued: one of the same command, or one the model made the phase's gate in its place (--replace). A
// narrower check that passes does not clear it. With ended, only the gates that have ended count: the gate
// the phase stood on before those queued or running now.
func (st State) gate(phase string, ended bool) *Job {
	var g *Job
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if j.Phase != phase || j.kind() != JobVerify || ended && !j.Finished() {
			continue
		}
		if g == nil || !g.Red() || takesOver(*j, *g) {
			g = j
		}
	}
	return g
}

// takesOver reports whether gate j takes over from red gate g: it runs the same command (in a lane or in
// the working tree), reruns what failed in g (Reruns: verify --failed, the same command on its failures
// alone), or was queued to stand in for g (Replaces), and it is queued, running, or ended with a verdict:
// a pass clears g, and a failure of its own is the red gate from then on. One that ended without a verdict
// (it could not run, or was interrupted, by `baton stop` say) checked nothing, and g stands again.
func takesOver(j, g Job) bool {
	return (j.Command == g.Command || j.Reruns == g.ID || j.Replaces == g.ID) && (!j.Finished() || j.Red() || j.Status == JobPassed)
}

// RedGate is the red gate phase stood on before the gates queued or running now (gate), or nil when it
// stood on none that is red: what a gate of another command queued now does not clear, unless it is made
// the phase's gate in its place (--replace).
func (st State) RedGate(phase string) *Job {
	if g := st.gate(phase, true); g != nil && g.Red() {
		return g
	}
	return nil
}

// After is phase j.Phase's jobs of j's kind queued after j, oldest first: for a red gate it stands on, the
// checks that did not clear it.
func (st State) After(j Job) []Job {
	var out []Job
	seen := false
	for _, o := range st.Jobs {
		switch {
		case o.ID == j.ID:
			seen = true
		case seen && o.Phase == j.Phase && o.kind() == j.kind():
			out = append(out, o)
		}
	}
	return out
}

// Running is phase's jobs queued or running, oldest first: a verify and an await at most.
func (st State) Running(phase string) []Job {
	var out []Job
	for _, j := range st.Jobs {
		if j.Phase == phase && !j.Finished() {
			out = append(out, j)
		}
	}
	return out
}

// stands reports whether j is one its phase's verification stands on (Standing).
func (st State) stands(j Job) bool {
	return slices.ContainsFunc(st.Standing(j.Phase), func(o Job) bool { return o.ID == j.ID })
}

// Red lists the jobs red that their phases stand on (Standing), oldest first. Their phases share the
// branch with the phase in hand, so failures in their area are theirs.
func (st State) Red() []Job {
	var out []Job
	for _, j := range st.Jobs {
		if j.Red() && st.stands(j) {
			out = append(out, j)
		}
	}
	return out
}

// WaitsOn is the jobs phase id waits on while it is verifying or parked (PhaseState.On), as on record now;
// a job gone from the record is left out. A phase recorded before baton kept them waits on the one job its
// park names, or, verifying, on its latest.
func (st State) WaitsOn(id string) []Job {
	var out []Job
	for _, jid := range st.on(id) {
		if j := st.Job(jid); j != nil {
			out = append(out, *j)
		}
	}
	return out
}

// on is the ids of the jobs phase id waits on (WaitsOn), gone from the record or not.
func (st State) on(id string) []string {
	ps := st.Phases[id]
	switch {
	case ps == nil:
		return nil
	case len(ps.On) > 0:
		return ps.On
	case ps.Status == PhaseParked:
		if ps.Park != nil && ps.Park.Job != "" {
			return []string{ps.Park.Job}
		}
		return nil
	}
	if j := st.Latest(id); j != nil {
		return []string{j.ID}
	}
	return nil
}

// HeldOn reports whether phase id, verifying or parked, still waits on its jobs (WaitsOn): one of them is
// queued or running, and none has ended without passing. On the first that does (red, or with no
// verdict), or one gone from the record, there is something to act on, and the phase waits no more.
func (st State) HeldOn(id string) bool {
	runs := false
	for _, jid := range st.on(id) {
		j := st.Job(jid)
		switch {
		case j == nil:
			return false
		case !j.Finished():
			runs = true
		case j.Status != JobPassed:
			return false
		}
	}
	return runs
}

// ids is the jobs' ids.
func ids(jobs []Job) []string {
	var out []string
	for _, j := range jobs {
		out = append(out, j.ID)
	}
	return out
}

// Due lists the results the model has not been given and should be given now: those of the phase in
// hand, or, once the plan is complete and before the human is told, of the verification that completed
// it. None is due while a compaction is owed or underway: the result comes after it. A red gate's result
// held for the advisor is not due until the host releases it (Advising, LookEnv).
func (st State) Due() []Job {
	switch {
	case st.Compacting():
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
		if j.Finished() && j.Delivered.IsZero() && (st.Mode == ModeComplete || j.Phase == st.Current) && !st.Advising(LookEnv, j.ID) {
			out = append(out, j)
		}
	}
	return out
}

// TakeDue returns the results due (Due) and records them as given. A hold for the advisor past its deadline
// is released first (LapseAdvice); the advisor's words on a result given are TakeAdvice's.
func TakeDue(st *State, now time.Time) []Job {
	LapseAdvice(st, now)
	due := st.Due()
	for _, d := range due {
		if j := st.Job(d.ID); j != nil {
			j.Delivered = now
		}
	}
	return due
}

// HereGate is the job not finished yet that runs in the session's own working tree (Job.Here), or nil
// when there is none. It reads that tree as it runs, so nothing else may change it until it ends: the
// session takes up no other phase (Next) and is not switched, edits there are refused (hooks), and no
// worker's work lands in a checkout a lead's gate runs in (loop).
func (st State) HereGate() *Job {
	for i := range st.Jobs {
		if j := &st.Jobs[i]; j.Here && !j.Finished() {
			return j
		}
	}
	return nil
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

// Next is the phase the run takes up when nothing is in hand: a parked phase whose jobs have given it
// something to act on (resolved; the one parked longest first), else the first pending phase, in document
// order, whose dependencies are all done, else "": every phase left is verifying, parked while its jobs
// run, a worker's, or waits for one that is. In a run that never parked a phase this is the first phase
// not done, as phases have always run. While a gate runs in the session's working tree (HereGate), it is
// "" too: the next phase would change the files the gate reads. In a run with phase agents, a QA phase is
// an agent's, and passed over (forAgent).
func Next(st State, p plan.Plan) string {
	if st.HereGate() != nil {
		return ""
	}
	if next := oldestParked(st, p, st.resolved); next != "" {
		return next
	}
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; (ps == nil || ps.Status == PhasePending) && ready(st, p, ph.ID) && !st.forAgent(ph) {
			return ph.ID
		}
	}
	return ""
}

// ready reports whether every phase that id waits for is done. A verifying phase is not done yet: what
// builds on it waits for its verification to pass. Nor is a working phase: what builds on it waits until
// its work has landed.
func ready(st State, p plan.Plan, id string) bool {
	for _, d := range p.Deps(id) {
		if ps := st.Phases[d]; ps == nil || ps.Status != PhaseDone {
			return false
		}
	}
	return true
}

// resolved reports whether phase id, parked as pk, can be taken up again: it was parked on no job, or it
// waits on its jobs no more (HeldOn): all of them have passed, or one ended without passing, or is no
// longer on record.
func (st State) resolved(id string, pk *Park) bool {
	return pk == nil || pk.Job == "" || !st.HeldOn(id)
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
func advance(st *State, p plan.Plan, now time.Time) string {
	next := Next(*st, p)
	switch {
	case next != "":
		st.Current = next
		st.OweBoundary(now)
	case Complete(*st, p):
		st.Mode, st.Current, st.BoundaryOwed = ModeComplete, "", false
	default:
		st.Current, st.BoundaryOwed = "", false
	}
	return next
}

// Verdict is what the jobs a phase was verifying on decided about it: the phase is done (Park ""), or
// reopened, parked for the reason Park gives.
type Verdict struct {
	Phase string
	// Job is the job that decided, as it ended: the first red, else the first with no verdict, else the
	// last that passed; zero when none is on record. Jobs are all it was verifying on, as on record now: a
	// verify and an await at most.
	Job  Job
	Jobs []Job
	Park string
}

// Settled is what Settle changed.
type Settled struct {
	Verdicts []Verdict
	Taken    string // the phase the run took up, now that one can start ("" for none)
	Complete bool   // the run is complete now
	// Confirms are the confirmation runs queued for QA phases whose reruns passed (confirmDue): each phase
	// verifies on its own, with no verdict yet.
	Confirms []Job
}

// Changed reports whether Settle changed anything.
func (s Settled) Changed() bool {
	return len(s.Verdicts) > 0 || s.Taken != "" || s.Complete || len(s.Confirms) > 0
}

// Unsettled reports a run that Settle would move: a phase verifying on jobs it waits on no more (HeldOn),
// or, with no phase in hand, a parked phase that can be taken up again or no job left to wait for.
func Unsettled(st State) bool {
	for id, ps := range st.Phases {
		if ps.Status == PhaseVerifying && !st.HeldOn(id) {
			return true
		}
	}
	if st.Mode != ModeRunning || st.Current != "" {
		return false
	}
	for id, ps := range st.Phases {
		if ps.Status == PhaseParked && st.resolved(id, ps.Park) {
			return true
		}
	}
	return len(st.Unfinished()) == 0
}

// Settle applies the verdicts of finished jobs to the phases verifying on them (WaitsOn): a phase whose
// jobs all passed is done; on the first that failed or ran out of time it reopens, parked as red, and on
// one that ended without a verdict (it could not run, or was interrupted) it reopens as unverified, even
// while its other job runs on. Either comes back before any phase not started yet (Next). With no phase in
// hand, the run then takes up the next one that can start, after a compaction, or is complete. A QA phase
// whose done owed a full run once the rerun it verified on passed verifies on that run instead (confirmDue,
// Confirms).
//
// The result of a verification that passed needs no telling, unless it completed the run: then it is how
// the model hears that the plan is complete (Due).
func Settle(st *State, p plan.Plan, now time.Time) Settled {
	var out Settled
	for _, ph := range p.Phases {
		ps := st.Phases[ph.ID]
		if ps == nil || ps.Status != PhaseVerifying || st.HeldOn(ph.ID) {
			continue
		}
		v := Verdict{Phase: ph.ID, Jobs: st.WaitsOn(ph.ID)}
		j := decider(v.Jobs, len(v.Jobs) < len(st.on(ph.ID)))
		if j != nil {
			v.Job = *j
		}
		switch {
		case j != nil && j.Status == JobPassed:
			if q, ok := confirmDue(st, ph.ID, v.Jobs, now); ok {
				out.Confirms = append(out.Confirms, q)
				continue
			}
			ps.Status, ps.DoneAt, ps.On = PhaseDone, now, nil
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
			st.Run.CompleteNotified = false // a worker's phase verified again after it was done: it is not done
		}
		out.Verdicts = append(out.Verdicts, v)
	}
	if len(out.Verdicts) > 0 {
		st.Run.Progress()
	}
	if st.Mode == ModeRunning && st.Current == "" {
		out.Taken = advance(st, p, now)
		out.Complete = st.Mode == ModeComplete
	}
	if !out.Complete {
		for _, v := range out.Verdicts {
			for _, x := range v.Jobs {
				if j := st.Job(x.ID); v.Park == "" && j != nil && j.Delivered.IsZero() {
					j.Delivered = now
				}
			}
		}
	}
	return out
}

// decider is the job that decides a verification on jobs, none of them still to wait for (HeldOn): the
// first red, else the first that ended with no verdict, else the last that passed. nil when one is gone
// from the record (gone) and none is red, or there are none: the verification has no verdict.
func decider(jobs []Job, gone bool) *Job {
	var noVerdict, passed *Job
	for i := range jobs {
		switch j := &jobs[i]; {
		case j.Red():
			return j
		case j.Status != JobPassed:
			if noVerdict == nil {
				noVerdict = j
			}
		default:
			passed = j
		}
	}
	if noVerdict != nil {
		return noVerdict
	}
	if gone {
		return nil
	}
	return passed
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
	case ps.Status == PhaseWorking:
		return "", fmt.Errorf("phase %s is a worker's, so it is not this session's to set aside", id)
	}
	ps.Status, ps.Park = PhaseParked, &pk
	st.Run.Progress()
	if id != st.Current {
		return st.Current, nil
	}
	st.CheckpointOwed, st.CheckpointDue = false, false
	return advance(st, p, pk.Since), nil
}

// Resumed reports whether the phase in hand was set aside before and has been taken up again.
func (st State) Resumed() bool {
	ps := st.Phases[st.Current]
	return ps != nil && ps.Park != nil && !ps.Park.Resumed.IsZero()
}

// ParkedOn is the verdict the phase in hand was parked on, as its park says (Park "" when it has none),
// with the job the park names as it ended (zero when it is gone from the record).
func (st State) ParkedOn() Verdict {
	vd := Verdict{Phase: st.Current}
	if ps := st.Phases[st.Current]; ps != nil && ps.Park != nil {
		vd.Park = ps.Park.Reason
		if j := st.Job(ps.Park.Job); j != nil && ps.Park.Job != "" {
			vd.Job = *j
		}
	}
	return vd
}

// Retakable reports a run that Retake would move: the plan runs, and the phase in hand is parked with a
// boundary owed before it starts again.
func Retakable(st State) bool {
	ps := st.Phases[st.Current]
	return st.Mode == ModeRunning && st.BoundaryOwed && ps != nil && ps.Status == PhaseParked
}

// Retake takes the phase in hand up again at once, with no compaction first: the phase's agent, in a
// phase's sub-run, whose phase reopened because its verification failed (Settle, or the merge queue's
// Reverify). advance owes a boundary before a phase resumes, as the lead's conversation carries another
// phase's; a sub-run holds its own phase alone, and nothing compacts a phase agent. The phase is active
// again (Start), what it is held to starts from o, and a boundary compaction queued for it is cancelled. A
// checkpoint queued or owed is left alone: it is the agent's, and a fresh agent ends it (state.Fresh). The
// result the phase reopened on stays undelivered, so Due hands it over. It reports whether it took the
// phase up; callers test Retakable on the state they loaded first, and read o before taking the lock.
func Retake(st *State, now time.Time, o Origin) bool {
	if !Retakable(*st) {
		return false
	}
	Start(st, now, o)
	if c := &st.Run.Compaction; c.InFlight() && c.Reason == "boundary" {
		c.Status = CompactCancelled
	}
	return true
}
