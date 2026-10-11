package loop

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Runners starts and watches the runners of a run's jobs. Each job runs in a `baton lane-run` of its own,
// detached, so it outlives the session's claude and a baton that restarts itself (package lane). Tests
// fake it.
type Runners interface {
	// Spawn starts the runner of job id of run, and returns its pid.
	Spawn(run, id string) (pid int, err error)
	// Alive reports whether process pid is running.
	Alive(pid int) bool
	// Kill kills process group pgid: whatever a runner that died left running.
	Kill(pgid int)
	// Sweep removes the project's lanes that no job holds and no live host brought up.
	Sweep()
	// Down brings down the lanes this host brought up that no job holds, and returns them.
	Down() []int
}

// jobs runs the run's jobs: it starts a runner for each job queued, and ends a job whose runner died as
// interrupted, since nothing else ever would. The runners do the rest, and record how each job ended. The
// first time, it sweeps up the lanes hosts that are gone left behind, and once the plan is complete, it
// brings down the lanes this host brought up.
func (l *Loop) jobs(v host.View, st state.State) {
	if l.Runners == nil {
		return
	}
	if !l.swept {
		l.swept = true
		go l.Runners.Sweep()
	}
	if st.Mode != state.ModeComplete {
		l.downed = false
	} else if !l.downed {
		// Nothing runs in them until the next plan, and what they hold up (services, containers) would
		// otherwise stay up until the session ends. A complete plan has no job left running.
		l.downed = true
		go l.lanesDown(l.Store)
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

// lanesDown brings down the lanes this host brought up, as the plan is complete.
func (l *Loop) lanesDown(s *state.Store) {
	if lanes := l.Runners.Down(); len(lanes) > 0 {
		l.logf("loop: the plan is complete: brought lanes %v down", lanes)
		s.Event("lanes_down", map[string]any{"lanes": lanes, "why": "plan complete"})
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
// start, after a compaction the loop queues itself, since the session is idle and no Stop will. What it
// did is logged, and left for the human to read in the session (Runtime.Announce). It returns the state
// as it left it.
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
	var out state.Settled
	var epoch int
	nst, err := l.Store.Update(func(st *state.State) error {
		if out = state.Settle(st, pl, v.Now); !out.Changed() {
			return nil
		}
		var says []string
		for _, vd := range out.Verdicts {
			says = append(says, verdictLine(vd))
		}
		if out.Taken != "" {
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
	if out.Taken != "" {
		l.Store.Event("compact_queued", map[string]any{"reason": "boundary", "epoch": epoch, "next": out.Taken, "by": "settle"})
	}
	return nst
}

// settleEvery spaces out attempts to settle a run that Settle cannot move: each reads the plan.
const settleEvery = time.Second

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

// deliver wakes an idle session whose phase in hand has job results due (state.Due), or whose plan a
// verification just completed: it types a short line, and the prompt's hook hands the results over. It
// waits for the typing gates like anything baton types, and never while the human is being asked for
// something. It reports whether it typed.
func (l *Loop) deliver(v host.View, st state.State, in host.Injector) bool {
	due := st.Due()
	if len(due) == 0 || st.Run.Escalation != nil || st.Blocked != nil || st.ReviewDue != "" || st.Replan != nil || st.PendingProposal() != nil {
		return false
	}
	if l.why != "" {
		l.held(v, st, "tell the model a job finished")
		return false
	}
	ids := jobIDs(due)
	if ids == l.typedFor && v.Now.Sub(l.since(l.typedAt)) < l.Timing.ResumeNudge {
		return false // typed already: its prompt has not arrived yet
	}
	l.proceed()
	l.typedFor, l.typedAt = ids, v.Now
	l.Store.Event("results_typed", map[string]any{"jobs": ids})
	if err := in.Type(decide.JobsFinished(due), true); err != nil {
		l.logf("loop: typing the results: %v", err)
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
