package loop

import (
	"context"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What a phase starts, it stops (v0.8.7, package leftover). A real run's phase agent kept its own test
// stack up after its work, beside the main one, and the QA gate competed with it for the machine. A phase
// agent's `baton done` is asked once about what it leaves running (package cli); here, the host:
//
//   - stops what a worker left running in its worktree as it retires it, before the worktree goes
//     (retirement.leftovers): each process started under baton. Containers, and processes it cannot tell
//     are the worker's or could not stop, the run's own host names to the lead in a look-in, with the
//     command to stop each; baton removes no container itself;
//   - tells a gate's owner what finished phases left running, while the machine has no room (the fleet's
//     room reading, Coordinator.noRoom) and the gate runs past its history, as for a past_history look-in
//     (leftoverLookIns): what they left may be what slows it. Information only, once a gate.
//
// The machine is read through Coordinator.Leftovers and Retirement.Leftovers, which read nothing when zero.

// leftovers stops what the worker left running in its worktree (retiring's step 2b): each process started
// under baton, leaving out the jobs it stopped, whose runners stop their own. What it does not stop goes to
// Retired.Left, and the run's own host names it to the lead in a look-in. Logged leftover_stopped and
// leftover_told, in the run.
func (x *retirement) leftovers() {
	dir, phase := x.entry.Worktree, x.entry.Phase
	if dir == "" {
		return
	}
	skip := leftover.Here()
	skip.PIDs, skip.Groups = x.jobProcs.PIDs, x.jobProcs.Groups
	left := x.Leftovers.Find(context.Background(), []string{dir}, skip)
	for _, n := range left.Notes {
		x.Logf("loop: %s: what its worker left running: %s", phase, n)
	}
	if left.Empty() {
		return
	}
	stopped, still := x.Leftovers.StopAll(left.Procs)
	if x.out.Stopped = stopped; len(stopped) > 0 {
		x.Run.Event("leftover_stopped", map[string]any{"phase": phase, "processes": leftover.PIDs(stopped), "names": comms(stopped), "by": x.By})
		x.Logf("loop: %s: stopped %d %s its worker left running", phase, len(stopped), plural(len(stopped), "process", "processes"))
	}
	hidden := left.Hidden
	if len(stopped) > 0 {
		hidden = x.Leftovers.Still(hidden) // each in the process group of one stopped, which it may have ended with
	}
	rest := leftover.Left{Procs: still, Hidden: hidden, Containers: left.Containers}
	if rest.Empty() {
		return
	}
	x.out.Left = rest
	told := false
	if x.Host {
		x.Run.Update(func(st *state.State) error {
			told = state.AddLookIn(st, state.LookIn{Kind: state.LookLeftover, At: x.Now,
				Text: decide.LeftoverRetired(phase, dir, rest), Say: decide.LeftoverRetiredSaid(phase, rest)})
			return nil
		})
	}
	var names []string
	for _, c := range rest.Containers {
		names = append(names, c.Name)
	}
	x.Run.Event("leftover_told", map[string]any{"phase": phase, "containers": names, "processes": leftover.PIDs(append(slices.Clone(still), hidden...)),
		"by": x.By, "look_in": told})
}

// stoppedLeftovers tells the human, in the lead's run s, what retiring phase's worker (r) stopped of what
// it left running in its worktree, if anything.
func (c *Coordinator) stoppedLeftovers(s *state.Store, phase string, r Retired) {
	if len(r.Stopped) > 0 {
		c.announce(s, decide.LeftoverStoppedSaid(phase, r.Stopped))
	}
}

// comms are the processes' names, as an event lists them.
func comms(ps []procs.Proc) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Comm)
	}
	return out
}

// leftoverEvery is how long the coordinator waits before it reads the machine again for what finished
// phases left running, while a gate runs slow on a busy machine.
const leftoverEvery = 5 * time.Minute

// phaseTree is a finished phase's worktree.
type phaseTree struct{ phase, dir string }

// finishedTrees are the worktrees of the run st's workers whose phase is finished (their work lands, or
// landed), but for one a live worker of the run works in again.
func finishedTrees(st state.State) []phaseTree {
	var out []phaseTree
	for _, w := range st.Workers {
		if (w.Status != state.WorkerLanding && w.Status != state.WorkerLanded) || w.Worktree == "" ||
			slices.ContainsFunc(out, func(t phaseTree) bool { return t.dir == w.Worktree }) ||
			slices.ContainsFunc(st.Workers, func(o state.Worker) bool {
				return o.Worktree == w.Worktree && o.Live() && o.Status != state.WorkerLanding
			}) {
			continue
		}
		out = append(out, phaseTree{w.Phase, w.Worktree})
	}
	return out
}

// pastHistory reports a gate that has run ran past its history (earlier, how long its command's passing
// runs took): decide.LookSlowFactor times the slowest of them, and Timing.SlowAfter more (t).
func pastHistory(t Timing, ran time.Duration, earlier []time.Duration) bool {
	if t.SlowAfter <= 0 || len(earlier) == 0 {
		return false
	}
	slowest := slices.Max(earlier)
	return ran >= time.Duration(float64(slowest)*decide.LookSlowFactor) && ran >= slowest+t.SlowAfter
}

// leftoverLookIns leaves each gate of loops that runs past its history (passes: the passing runs of each
// command) while the machine has no room a look-in, once, naming what the finished phases of the run st
// left running: running processes and containers in their worktrees. It reads the machine at most every
// leftoverEvery, and only with a gate to tell and a finished phase to look at. lead is the lead's run.
func (c *Coordinator) leftoverLookIns(now time.Time, lead *state.Store, st state.State, loops []lookLoop, passes map[string][]time.Duration) {
	if c.noRoom == "" || !c.leftAt.IsZero() && now.Sub(c.leftAt) < leftoverEvery {
		return
	}
	type gate struct {
		x   lookLoop
		j   state.Job
		ran time.Duration
	}
	var slow []gate
	for _, x := range loops {
		for _, j := range x.st.Jobs {
			from := gateBegan(j)
			if j.Status != state.JobRunning || j.Kind == state.JobAwait || !j.LaneWait.IsZero() || from.IsZero() || !now.After(from) ||
				slices.Contains(j.LookedIn, state.LookLeftover) || !pastHistory(c.Lead.Timing, now.Sub(from), passes[j.Command]) {
				continue
			}
			slow = append(slow, gate{x, j, now.Sub(from)})
		}
	}
	trees := finishedTrees(st)
	if len(slow) == 0 || len(trees) == 0 {
		return
	}
	c.leftAt = now
	skip := leftover.Here()
	var dirs []string
	for _, x := range loops {
		for _, j := range x.st.Unfinished() {
			skip.Job(j.PID, j.PGID)
		}
	}
	for _, t := range trees {
		dirs = append(dirs, t.dir)
	}
	found := c.Leftovers.Find(context.Background(), dirs, skip).Running()
	var left []decide.PhaseLeft
	var phases, containers []string
	var pids []int
	for _, t := range trees {
		in := found.In(t.dir)
		if in.Empty() {
			continue
		}
		left = append(left, decide.PhaseLeft{Phase: t.phase, Left: in})
		phases = append(phases, t.phase)
		pids = append(pids, leftover.PIDs(append(slices.Clone(in.Procs), in.Hidden...))...)
		for _, k := range in.Containers {
			containers = append(containers, k.Name)
		}
	}
	if len(left) == 0 {
		return
	}
	for _, g := range slow {
		c.leaveLookIn(lead, g.x, state.LookIn{Kind: state.LookLeftover, Job: g.j.ID, At: now,
			Text: decide.LeftoverLookIn(g.j, g.ran, c.noRoom, left), Say: decide.LeftoverLookInSaid(g.j, left)},
			map[string]any{"phase": g.j.Phase, "command": g.j.Command, "seconds": int(g.ran.Seconds()), "busy": c.noRoom,
				"phases": phases, "processes": pids, "containers": containers})
	}
}
