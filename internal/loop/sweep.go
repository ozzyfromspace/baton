package loop

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// When a plan is attached to the host's run (its generation grows), the host retires the workers other
// runs of the project left behind, which no session will come back to (state.Abandoned). A run whose host
// ended keeps its workers suspended until a host of that same run starts again, which may never happen,
// and until then their worktrees, their branches and their phases wait for it. Each is retired as
// `baton drop` would (RetireWorker): its uncommitted work is saved, its worktree removed, and its branch
// kept, by its name, when it holds commits that never landed; its phase goes back to its run, for that
// run's next host. A run with a live host is left alone, and so is one whose host's process still runs
// (the machine slept), or that showed a sign of life in the last state.AbandonAfter (a host restarting on
// a newer baton). The generation the host starts with counts as seen, so a host that restarts sweeps
// nothing. The sweep runs in the coordinator's step, off the lead's tick, one worker at a time.

// whyNewRun is why a worker another run left behind is retired.
const whyNewRun = "a new run started in this project"

// planAttached notes, on the lead's tick, a plan attached to the host's run s since the host last looked
// (st.Generation grew), which owes a sweep. The generation of a run the host has just found is seen.
func (c *Coordinator) planAttached(s *state.Store, st state.State) {
	switch {
	case s.Ref != c.genRun:
		c.genRun, c.gen = s.Ref, st.Generation
	case st.Generation > c.gen:
		c.gen, c.sweepOwed = st.Generation, true
	}
}

// sweep retires the workers the project's other runs left behind, and tells the human, in the session
// of the host's run s, about each run's.
func (c *Coordinator) sweep(now time.Time, s *state.Store) {
	isBaton := c.isBaton
	if isBaton == nil {
		isBaton = batonRunning
	}
	for _, r := range c.Project.Runs() {
		live := r.State.LiveWorkers()
		if r.ID == s.Ref || len(live) == 0 {
			continue
		}
		run, err := c.Project.ExistingRun(r.ID, "")
		if err != nil {
			continue
		}
		var left []decide.Left
		for _, e := range live {
			if !state.Abandoned(r, &e, isBaton, now) {
				c.Lead.logf("loop: run %s: its worker on %s is left alone: the run may yet go on", r.ID, e.Phase)
				continue
			}
			got := RetireWorker(Retirement{Project: c.Project, Home: c.Home, Run: run, Entry: e, Why: whyNewRun, By: s.Instance, Now: now,
				Snapshot: c.Lead.Snapshot, Logf: c.Lead.Logf, between: c.retireStep})
			if got.Skipped != "" {
				c.Lead.logf("loop: run %s: its worker on %s is left alone: %s", r.ID, e.Phase, got.Skipped)
				continue
			}
			left = append(left, decide.Left{Phase: e.Phase, Branch: got.Branch, Unlanded: got.Unlanded, Worktree: e.Worktree, Kept: got.Kept})
		}
		if len(left) == 0 {
			continue
		}
		phases, kept, trees := []string{}, map[string]int{}, map[string]string{}
		for _, l := range left {
			phases = append(phases, l.Phase)
			if l.Unlanded > 0 {
				kept[l.Branch] = l.Unlanded
			}
			if l.Kept != "" {
				trees[l.Worktree] = l.Kept
			}
		}
		fields := map[string]any{"run": r.ID, "phases": phases, "kept": kept}
		if len(trees) > 0 {
			fields["worktrees_kept"] = trees
		}
		s.Event("workers_retired", fields)
		c.announce(s, decide.WorkersRetired(left))
	}
}

// batonRunning reports whether process pid is a baton that is still running, as the process table has it
// now: the pid of a run's host that has gone may since belong to another program. When the table cannot
// be read (no ps), a process that is alive counts as one, so that no live run's workers are taken for
// abandoned.
func batonRunning(pid int) bool {
	if !lane.Alive(pid) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return lane.Alive(pid) // it ended meanwhile, or there is no ps
	}
	return batonName(string(out))
}

// batonName reports a process name ps prints (comm: a path on macOS, a name on Linux) as baton's.
func batonName(comm string) bool {
	return strings.TrimSuffix(filepath.Base(strings.TrimSpace(comm)), ".exe") == "baton"
}
