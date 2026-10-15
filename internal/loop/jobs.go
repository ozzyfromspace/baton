package loop

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
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
// verification just completed, or that baton has messages for (state.Mail): it gives it a short line
// (say), and the prompt's hook hands them over. It waits for the typing gates like anything baton types, and never
// while the session is to ask the human something (a worker's proposal is the lead session's to ask). It
// reports whether it typed.
func (l *Loop) deliver(v host.View, st state.State, in host.Injector) bool {
	due, mail := st.Due(), st.Mail()
	if len(due) == 0 && len(mail) == 0 || holdsDeliveries(st.Run.Escalation) || st.Blocked != nil || st.ReviewDue != "" || st.Replan != nil || st.OwnProposal() != nil {
		return false
	}
	if l.why != "" {
		l.held(v, st, "tell the model a job finished")
		return false
	}
	ids := jobIDs(due)
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
	if len(due) > 0 {
		l.Store.Event("results_typed", map[string]any{"jobs": jobIDs(due), "via": l.via()})
	} else {
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
