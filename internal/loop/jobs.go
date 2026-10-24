package loop

import (
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Runners starts and watches the runners of a session's jobs. Each job runs in a `baton lane-run` of its
// own, detached, so it outlives the session's claude and a baton that restarts itself (package lane).
// Tests fake it.
type Runners interface {
	// Spawn starts the runner of job id of the run ref names (a worker's sub-run: <run>/<phase>), and
	// returns its pid.
	Spawn(ref, id string) (pid int, err error)
	// Alive reports whether process pid is running.
	Alive(pid int) bool
	// Kill kills process group pgid: whatever a runner that died left running.
	Kill(pgid int)
	// Usage reads the CPU time used so far by the process tree of process group pgid (procs.TreeUsage).
	Usage(pgid int) (procs.Usage, error)
}

// jobs runs the session's jobs: it starts a runner for each job queued, and ends a job whose runner died
// as interrupted, since nothing else ever would. The runners do the rest, and record how each job ended.
// The lanes they run in are the whole run's, and the Coordinator's to sweep and bring down.
func (l *Loop) jobs(v host.View, st state.State) {
	if l.Runners == nil {
		return
	}
	for _, j := range st.Jobs {
		switch {
		case j.Status == state.JobQueued && j.Spawned.IsZero():
			l.spawn(v, j)
		case j.Status == state.JobQueued:
			if v.Now.Sub(l.since(j.Spawned)) > l.Timing.SpawnGrace && !l.Runners.Alive(j.PID) {
				l.lost(v, j, fmt.Sprintf("its runner (pid %d) ended before it started the job", j.PID))
			}
		case j.Status != state.JobRunning:
		case !l.Runners.Alive(j.PID):
			l.lost(v, j, fmt.Sprintf("its runner (pid %d) is gone", j.PID))
		default:
			if quiet := v.Now.Sub(l.since(latest(j.Heartbeat, j.Started))); quiet > l.Timing.JobStale {
				l.lost(v, j, fmt.Sprintf("its runner (pid %d) has not been heard from for %s", j.PID, quiet.Round(time.Second)))
			}
		}
	}
	l.hung(v, st)
}

// A gate can hang without a word: a test waiting on a server that never answers, on input nobody gives, on
// a lock. Left alone, it would hold its phase until verify_timeout (3 hours by default). So the host looks
// at each running gate about once a minute (hungEvery), and one whose log has not grown, and whose process
// tree has used no CPU, for Timing.HungAfter is stopped as hung: it ends interrupted, with why
// (decide.HungReason), so its phase counts as unverified, and the model is given the result. Its runner
// stops its command and brings its lane down at its next beat, as for a job cancelled. Never on the log's
// silence alone: a gate whose processes cannot be read (no reading on this system, or a process of
// another user's) is never taken for hung, nor is one waiting for a lane, nor an await, whose wait on
// something outside the project may well be silent and idle.

// hungEvery is how often the host looks at its running gates for signs of life.
const hungEvery = time.Minute

// life is what the host saw of a running gate when it last looked (at): the process group it runs, its
// log, the CPU its process tree had used (nil: no reading), and since when none of them has moved (zero:
// not known to be still).
type life struct {
	pgid  int
	log   logMark
	cpu   procs.Usage
	still time.Time
	at    time.Time
}

// logMark is a log's size and when it was last written; a size of -1 for a log that cannot be read.
type logMark struct {
	size int64
	mod  time.Time
}

func markOf(path string) logMark {
	fi, err := os.Stat(path)
	if path == "" || err != nil {
		return logMark{size: -1}
	}
	return logMark{size: fi.Size(), mod: fi.ModTime()}
}

// hung looks at the running gates of the loop's run st, about once a minute, and stops one that hangs
// (see above). A look that comes long after the last (the machine slept) starts each count afresh.
func (l *Loop) hung(v host.View, st state.State) {
	after := l.Timing.HungAfter
	if after <= 0 || !l.hungAt.IsZero() && v.Now.Sub(l.hungAt) < hungEvery {
		return
	}
	l.hungAt = v.Now
	seen := map[string]life{}
	for _, j := range st.Jobs {
		if j.Status != state.JobRunning || j.Kind == state.JobAwait || j.PGID == 0 || !j.LaneWait.IsZero() {
			continue
		}
		now := life{pgid: j.PGID, log: markOf(j.Log), at: v.Now}
		if u, err := l.Runners.Usage(j.PGID); err == nil && len(u) > 0 {
			now.cpu = u
		}
		was, ok := l.lives[j.ID]
		switch {
		case now.cpu == nil || now.log.size < 0:
			// No reading: never still.
		case ok && !was.still.IsZero() && was.pgid == now.pgid && was.log == now.log && maps.Equal(was.cpu, now.cpu) &&
			v.Now.Sub(was.at) <= hungEvery+l.Timing.ClockJump:
			now.still = was.still
		default:
			now.still = v.Now
		}
		seen[j.ID] = now
		if quiet := v.Now.Sub(now.still); !now.still.IsZero() && quiet >= after {
			l.stopHung(v, j, quiet)
			delete(seen, j.ID)
		}
	}
	l.lives = seen // a job that ended is forgotten
}

// stopHung stops gate j, whose log and processes have been still for quiet: it ends interrupted, with
// why, unless it ended meanwhile, and its runner stops its command at its next beat.
func (l *Loop) stopHung(v host.View, j state.Job, quiet time.Duration) {
	stopped := false
	l.update(func(st *state.State) {
		x := st.Job(j.ID)
		if x == nil || x.Status != state.JobRunning || x.PID != j.PID || !x.Started.Equal(j.Started) {
			return
		}
		x.Status, x.Exit, x.Reason, x.Ended = state.JobInterrupted, -1, decide.HungReason(quiet), v.Now
		st.Run.Announce = append(st.Run.Announce, decide.JobHung(*x, quiet))
		stopped = true
	})
	if !stopped {
		return
	}
	l.logf("loop: job %s hung: its log and its processes (group %d) still for %s; stopped", j.ID, j.PGID, quiet.Round(time.Second))
	l.Store.Event("job_hung", map[string]any{"job": j.ID, "phase": j.Phase, "command": j.Command, "pgid": j.PGID, "log": j.Log,
		"quiet": quiet.Round(time.Second).String(), "by": "host"})
}

// spawn starts a queued job's runner, which takes the job from there.
func (l *Loop) spawn(v host.View, j state.Job) {
	pid, err := l.Runners.Spawn(l.Store.Ref, j.ID)
	if err != nil {
		l.lost(v, j, fmt.Sprintf("baton could not start its runner: %v", err))
		return
	}
	l.logf("loop: started the runner of job %s (pid %d)", j.ID, pid)
	l.update(func(st *state.State) {
		if x := st.Job(j.ID); x != nil {
			x.Spawned = v.Now
			if x.PID == 0 {
				x.PID = pid
			}
		}
	})
}

// lost ends a job its runner will never finish (unless it ended meanwhile), as interrupted, and kills
// whatever its command left running.
func (l *Loop) lost(v host.View, j state.Job, reason string) {
	var ended *state.Job
	l.update(func(st *state.State) {
		x := st.Job(j.ID)
		if x == nil || x.Status != j.Status || x.PID != j.PID {
			return
		}
		x.Status, x.Exit, x.Reason, x.Ended = state.JobInterrupted, -1, reason, v.Now
		e := *x
		ended = &e
		if line := decide.FinishedAside(*st, e); line != "" {
			st.Run.Announce = append(st.Run.Announce, line)
		}
	})
	if ended == nil {
		return
	}
	if ended.PGID != 0 {
		l.Runners.Kill(ended.PGID)
	}
	l.logf("loop: job %s interrupted: %s", j.ID, reason)
	var seconds int
	if !j.Started.IsZero() {
		seconds = int(v.Now.Sub(j.Started).Seconds())
	}
	l.Store.Event("job_finished", map[string]any{"job": j.ID, "phase": j.Phase, "status": state.JobInterrupted, "exit": -1,
		"lane": ended.Lane, "reason": reason, "seconds": seconds, "by": "host"})
}

// settle applies what finished jobs decided (state.Settle): a phase verifying on a job that passed is
// done, and one whose job failed reopens. With no phase in hand, the run takes up the next phase that can
// start, after a compaction the loop queues itself, since the session is idle and no Stop will. A phase's
// sub-run owes no compaction (nothing compacts a phase agent): its phase, reopened, is its agent's again at
// once (state.Retake), and the result is due to it. What it did is logged, and left for the human to read
// in the session (Runtime.Announce; a phase agent's, in the lead's). It returns the state as it left it.
func (l *Loop) settle(v host.View, st state.State) state.State {
	l.envRepeat(v, st) // what the environment took of each whole run that ended red (envrepeat.go)
	if !state.Unsettled(st) || v.Now.Sub(l.settled) < settleEvery {
		return st
	}
	l.settled = v.Now
	pl, err := l.Store.LoadPlan()
	if err != nil {
		l.logf("loop: settling the run's jobs: %v", err)
		return st
	}
	agent := l.Store.Worker() != ""
	var origin state.Origin
	if agent {
		origin = state.OriginOf(l.Store.Root) // read before taking the lock: git can be slow
	}
	var out state.Settled
	var epoch int
	var retook *state.Verdict
	nst, err := l.Store.Update(func(st *state.State) error {
		if out = state.Settle(st, pl, v.Now); !out.Changed() {
			return nil
		}
		if agent && out.Taken != "" && state.Retake(st, v.Now, origin) {
			vd := st.ParkedOn()
			retook = &vd
		}
		var says []string
		for _, vd := range out.Verdicts {
			if retook == nil || vd.Phase != retook.Phase {
				says = append(says, verdictLine(vd))
			}
		}
		says = append(says, confirmLines(*st, out)...)
		if out.Taken != "" && !agent {
			st.Run.Compaction.Queue("boundary", v.Now)
			epoch = st.Run.Compaction.Epoch
			verb := "starts"
			if ps := st.Phases[out.Taken]; ps != nil && ps.Status == state.PhaseParked {
				verb = "resumes"
			}
			says = append(says, fmt.Sprintf("baton: %s can start → compacting, then %s %s", out.Taken, out.Taken, verb))
		}
		st.Run.Announce = append(st.Run.Announce, says...)
		return nil
	})
	if err != nil {
		l.logf("loop: settling the run's jobs: %v", err)
		return st
	}
	for _, vd := range out.Verdicts {
		if vd.Park == "" {
			l.Store.Event("phase_done", map[string]any{"phase": vd.Phase, "by": "verification", "job": vd.Job.ID, "next": out.Taken})
			continue
		}
		l.Store.Event("phase_parked", map[string]any{"phase": vd.Phase, "reason": vd.Park, "job": vd.Job.ID, "status": vd.Job.Status})
	}
	l.confirmed(nst, out)
	switch {
	case retook != nil:
		l.retook(*retook, "settle")
	case out.Taken != "" && !agent:
		l.Store.Event("compact_queued", map[string]any{"reason": "boundary", "epoch": epoch, "next": out.Taken, "by": "settle"})
	}
	return nst
}

// settleEvery spaces out attempts to settle a run that Settle cannot move: each reads the plan.
const settleEvery = time.Second

// retakeEvery spaces out looks for a phase a phase's sub-run owes a boundary before (Agent.retake): one
// reads the worktree.
const retakeEvery = time.Second

// retake takes up again, in a phase's sub-run, a phase that is parked with a boundary owed before it
// (state.Retake): a sub-run that a baton before v0.8.2 left so, whose agent nothing would ever compact,
// wake or replace, and whose every tool call was held. settle retakes its own; this is for what it never
// reaches, as it moves only a run with no phase in hand. It returns the state as it left it.
func (l *Loop) retake(v host.View, st state.State) state.State {
	if l.Store.Worker() == "" || !state.Retakable(st) || v.Now.Sub(l.retaken) < retakeEvery {
		return st
	}
	l.retaken = v.Now
	origin := state.OriginOf(l.Store.Root) // read before taking the lock: git can be slow
	var retook *state.Verdict
	nst, err := l.Store.Update(func(st *state.State) error {
		if state.Retake(st, v.Now, origin) {
			vd := st.ParkedOn()
			retook = &vd
		}
		return nil
	})
	if err != nil {
		l.logf("loop: taking %s up again: %v", st.Current, err)
		return st
	}
	if retook != nil {
		l.retook(*retook, "tick")
	}
	return nst
}

// retook logs that a phase's sub-run took its phase up again at once (state.Retake), as verdict vd
// reopened it, by what found it, and tells the human in the lead's session (Store.Retook).
func (l *Loop) retook(vd state.Verdict, by string) {
	l.logf("loop: %s reopened (%s): its agent takes it up again", vd.Phase, vd.Park)
	l.Store.Retook(vd, by, decide.AgentRetakes(vd.Phase, vd.Job))
}

// verdictLine tells the human what a verdict did.
func verdictLine(vd state.Verdict) string {
	j := vd.Job
	switch {
	case vd.Park == "" && len(vd.Jobs) > 1:
		var ids []string
		for _, x := range vd.Jobs {
			ids = append(ids, x.ID)
		}
		return fmt.Sprintf("baton: %s verified (jobs %s passed) → %s done", vd.Phase, strings.Join(ids, " and "), vd.Phase)
	case vd.Park == "":
		return fmt.Sprintf("baton: %s verified (job %s passed) → %s done", vd.Phase, j.ID, vd.Phase)
	case j.ID == "":
		return fmt.Sprintf("baton: %s's verification is gone from the record → %s reopens", vd.Phase, vd.Phase)
	case vd.Park == state.ParkRed:
		return fmt.Sprintf("baton: %s's verification %s (job %s) → %s reopens", vd.Phase, decide.Outcome(j), j.ID, vd.Phase)
	}
	return fmt.Sprintf("baton: %s's verification %s (job %s), with no verdict → %s reopens", vd.Phase, decide.Outcome(j), j.ID, vd.Phase)
}

// deliver wakes an idle session whose phase in hand has job results due (state.Due), whose plan a
// verification just completed, that baton has messages for (state.Mail), or look-ins at its gates and
// landings, whatever phase they are for (state.LookInsDue): it gives it a short line (say), and the
// prompt's hook hands them over. It waits for the typing gates like anything baton types, and never
// while the session is to ask the human something (a worker's proposal is the lead session's to ask). It
// reports whether it typed.
func (l *Loop) deliver(v host.View, st state.State, in host.Injector) bool {
	due, mail := st.Due(), st.Mail()
	looks, lookIDs := lookInsDue(st)
	if len(due) == 0 && len(mail) == 0 && len(looks) == 0 || holdsDeliveries(st.Run.Escalation) || st.Blocked != nil || st.ReviewDue != "" || st.PlanQuestion != nil || st.OwnProposal() != nil {
		return false
	}
	if l.why != "" {
		l.held(v, st, "tell the model a job finished")
		return false
	}
	ids := jobIDs(due) + lookIDs
	var kinds []string
	for _, m := range mail {
		kinds = append(kinds, m.Kind)
		ids += " mail@" + m.At.Format(time.RFC3339Nano)
	}
	if ids == l.typedFor && v.Now.Sub(l.since(l.typedAt)) < l.Timing.ResumeNudge {
		return false // typed already: its prompt has not arrived yet
	}
	l.proceed()
	// The lead's prompt hook hands over the results and baton's messages; a phase agent, started again by
	// the line, is given them with its next tool call or its stop.
	agent := l.Store.Worker() != ""
	var line string
	switch {
	case len(due) > 0 && agent:
		line = decide.AgentJobsFinished(due)
	case len(due) > 0:
		line = decide.JobsFinished(due)
	case len(looks) > 0 && agent:
		line = decide.AgentLookIn(looks)
	case len(looks) > 0:
		line = decide.LookInTyped(looks)
	case agent:
		line = decide.AgentMail(kinds)
	default:
		line = decide.MailTyped(kinds)
	}
	if err := l.say(v, in, line); err != nil {
		l.unsaid(err, "typing the results")
		return false // said at a later tick
	}
	l.typedFor, l.typedAt = ids, v.Now
	switch {
	case len(due) > 0:
		l.Store.Event("results_typed", map[string]any{"jobs": jobIDs(due), "via": l.via()})
	case len(looks) > 0:
		l.Store.Event("look_in_typed", map[string]any{"kinds": looks, "via": l.via()})
	default:
		l.Store.Event("mail_typed", map[string]any{"messages": kinds, "via": l.via()})
	}
	return true
}

func jobIDs(jobs []state.Job) string {
	var ids []string
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	return strings.Join(ids, " ")
}

// holdsDeliveries reports an escalation that keeps baton from giving the session its results and mail:
// any but a watchdog's stall, whose remedy is exactly something arriving to work on. A refused turn or an
// API error is never retried by a delivery of baton's.
func holdsDeliveries(e *state.Escalation) bool { return e != nil && !quietStall(e) }

// quietStall reports the watchdog's own stall escalation: the session went quiet with a phase in hand.
func quietStall(e *state.Escalation) bool { return e != nil && e.Watchdog && e.Kind == "stalled" }
