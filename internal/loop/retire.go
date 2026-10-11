package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Retiring a worker ends it for good, with no session running it: its run's own host does it once the
// worker's session has ended (or when it has none), and so may any other baton process, for a worker no
// live session hosts. Every step can be taken again, and a worker is retired by one process at a time:
// the first marks it on the run's roster (Worker.Retiring), and nothing resumes a worker so marked.
//
//  0. the mark, under the run's lock: the worker must still be on the roster as the caller read it, not
//     retiring by anyone else, and (for anyone but the run's own host) not running: its sub-run's owner
//     has no live heartbeat;
//  1. its uncommitted work is saved (gitx.Snapshot). If that fails, its worktree stays, and is named;
//  2. its jobs are marked interrupted, and each runner that is still a `baton lane-run` is told to stop,
//     which stops its own gate's process group;
//  3. its worktree is removed, only from under baton's worktrees for the project;
//  4. its branch is removed when nothing on it is left to land; otherwise it is kept, and renamed under
//     kept/ when the run's plan was replaced or the run dropped, so that no later worker on a phase with
//     the same id carries on from it. A worker retired while its plan stands keeps its branch's name, and
//     the phase's next worker carries on from its commits;
//  5. its phase goes back to the run, guarded by the mark: the run's own host takes it back as it always
//     has (state.Unassign), and anyone else only frees it (state.ReleaseWorker), so the run's next host
//     takes it up;
//  6. its instance lets go of its sub-run.

// Retirement is a worker to retire (RetireWorker), and who retires it.
type Retirement struct {
	Project *state.Project
	Home    string       // baton's home: a worktree is removed only from under its worktrees
	Run     *state.Store // the run the worker works for
	Entry   state.Worker // the worker, as the caller read it from the run's roster
	Why     string
	// By is who retires it: the run's own host (Host) by its instance, or another process by its instance
	// or by name ("baton clean").
	By   string
	Host bool
	Now  time.Time
	// Snapshot saves uncommitted work (gitx.Snapshot when nil). Logf is the host's log (nil: none).
	Snapshot func(root string, at time.Time, phase, why string) (gitx.Saved, error)
	Logf     func(format string, a ...any)

	// isRunner and stop find and stop a job's runner (nil: lane.IsRunner, lane.Terminate); between is
	// called after each step (tests).
	isRunner func(pid int) bool
	stop     func(pid int) error
	between  func(step string)
}

// Retired is what RetireWorker did.
type Retired struct {
	Skipped  string // why it left the worker alone ("" when it retired it)
	Snapshot string // the ref its uncommitted work was saved under ("" for none)
	Kept     string // why its worktree was left in place ("" when it was removed, or was gone)
	Branch   string // its branch, kept: it holds Unlanded commits that never landed ("" when it was removed)
	Unlanded int
	Jobs     []string // the jobs it stopped
	Next     string   // the phase the run's lead took up (the run's own host only)
}

// errKept leaves the run's state as it was: the worker is not retired by this caller.
var errKept = errors.New("left alone")

// RetireWorker retires the worker r names (see above), and logs worker_ended in its run. The run's own
// host tells the human; anyone else says what it did in its own way.
func RetireWorker(r Retirement) Retired {
	x := &retirement{Retirement: r}
	if r.Logf == nil {
		x.Logf = func(string, ...any) {}
	}
	steps := []struct {
		name string
		fn   func()
	}{{"snapshot", x.snapshot}, {"jobs", x.stopJobs}, {"worktree", x.removeWorktree}, {"branch", x.branch}, {"release", x.release}}
	if x.out.Skipped = x.mark(); x.out.Skipped != "" {
		return x.out
	}
	hook(x.between, "mark")
	for _, s := range steps {
		s.fn()
		hook(x.between, s.name)
	}
	r.Project.Unbind(x.entry.Instance)
	x.event()
	return x.out
}

type retirement struct {
	Retirement
	entry    state.Worker // as the mark found it
	gone     bool         // it was not on the roster any more (the run's own host only)
	replaced bool         // the run's plan was replaced since it was assigned
	dropped  bool         // the run was dropped
	into     string       // the run's branch
	fields   map[string]any
	out      Retired
}

// mark claims the worker for this caller, or says why it is left alone.
func (x *retirement) mark() string {
	e := x.Entry
	x.fields = map[string]any{"phase": e.Phase, "why": x.Why, "by": x.By}
	if !x.Host {
		if sub, err := x.Project.ExistingRun(e.Ref, ""); err == nil {
			if sst, err := sub.Load(); err == nil && sst.Owner.Live(x.Now) {
				return "its session runs in another baton session"
			}
		}
	}
	var why string
	_, err := x.Run.Update(func(st *state.State) error {
		x.replaced, x.dropped, x.into = e.Generation < st.Generation, st.Mode == state.ModeIdle, st.RunBranch
		cur := rosterEntry(st, e.Ref, e.Session)
		switch {
		case cur == nil && x.Host:
			x.entry, x.gone = e, true
			return errKept
		case cur == nil:
			why = "it is no longer on its run's roster"
		case cur.RetiringBy(x.By, x.Now):
			why = cur.Retirer + " is retiring it"
		case !x.Host && (cur.Status != e.Status || !cur.Since.Equal(e.Since)):
			why = "it changed since it was looked at"
		}
		if why != "" {
			return errKept
		}
		cur.Retiring, cur.Retirer = x.Now, x.By
		x.entry, x.replaced = *cur, cur.Generation < st.Generation
		return nil
	})
	switch {
	case why != "":
		return why
	case err != nil && !errors.Is(err, errKept):
		return "its run's state could not be read: " + err.Error()
	}
	return ""
}

// sub is the worker's sub-run, or nil when it has none: events about the worker's own work go there.
func (x *retirement) sub() *state.Store {
	sub, err := x.Project.ExistingRun(x.entry.Ref, x.entry.Instance)
	if err != nil {
		return nil
	}
	return sub
}

// snapshot saves the worker's uncommitted work. When it cannot be saved, the worktree stays.
func (x *retirement) snapshot() {
	dir := x.entry.Worktree
	if !isDir(dir) {
		return
	}
	if !gitx.Usable(dir) {
		if entries, err := os.ReadDir(dir); err != nil || len(entries) > 0 {
			x.out.Kept = "it is not a git worktree, so its files could not be saved"
		}
		return
	}
	at := x.sub()
	if at == nil {
		at = x.Run
	}
	saved, err := saveWork(at, dir, x.Snapshot, x.Logf, x.entry.Phase, x.Why)
	switch {
	case err != nil:
		x.out.Kept = "its uncommitted work could not be saved: " + err.Error()
	case saved.Ref != "":
		x.out.Snapshot = saved.Ref
	}
}

// stopJobs marks the worker's jobs interrupted, and tells each runner still running to stop.
func (x *retirement) stopJobs() {
	sub := x.sub()
	if sub == nil {
		return
	}
	isRunner, stop := x.isRunner, x.stop
	if isRunner == nil {
		isRunner, stop = lane.IsRunner, lane.Terminate
	}
	why := "its worker ended: " + x.Why
	var jobs []state.Job
	sub.Update(func(st *state.State) error {
		jobs = state.CancelJobs(st, func(state.Job) bool { return true }, why, x.Now)
		return nil
	})
	for _, j := range jobs {
		if j.PID != 0 && isRunner(j.PID) {
			stop(j.PID) // the runner stops its gate's process group, and brings its lane down
		}
		x.out.Jobs = append(x.out.Jobs, j.ID)
	}
	if len(jobs) > 0 {
		sub.Event("jobs_stopped", map[string]any{"jobs": x.out.Jobs, "why": why})
	}
}

// worktrees is where baton keeps the workers' worktrees for the project, by its real path.
func (x *retirement) worktrees() string {
	home := x.Home
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	return filepath.Join(home, "worktrees", lane.Key(x.Run.Checkout))
}

// removeWorktree removes the worker's worktree, unless it is kept or is not baton's to remove.
func (x *retirement) removeWorktree() {
	dir := x.entry.Worktree
	if x.out.Kept != "" || dir == "" {
		return
	}
	base := x.worktrees()
	if x.Home == "" || !gitx.Within(dir, base) {
		if isDir(dir) {
			x.out.Kept = "it is not under baton's worktrees (" + base + ")"
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if isDir(dir) {
		if err := gitx.WorktreeRemove(ctx, x.Run.Checkout, dir); err != nil {
			x.fields["worktree_error"] = err.Error()
		}
		if isDir(dir) {
			os.RemoveAll(dir)
		}
	}
	gitx.PruneUnder(ctx, x.Run.Checkout, base)
	if parent := filepath.Dir(dir); gitx.Within(parent, base) {
		os.Remove(parent) // the run's directory, once empty
	}
	os.Remove(base) // and the project's
}

// branch removes the worker's branch when nothing on it is left to land, and keeps it otherwise.
func (x *retirement) branch() {
	b := x.entry.Branch
	co := x.Run.Checkout
	if b == "" || gitx.RevParse(co, "refs/heads/"+b) == "" {
		return
	}
	into := x.entry.Base
	if x.into != "" {
		into = "refs/heads/" + x.into
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	n, err := gitx.Ahead(ctx, co, into, "refs/heads/"+b)
	switch {
	case err != nil:
		x.out.Branch, x.fields["branch_error"] = b, err.Error()
	case n == 0 && x.out.Kept != "":
		x.out.Branch = b // checked out in the worktree kept
	case n == 0:
		if err := gitx.RemoveBranch(ctx, co, b); err != nil {
			x.out.Branch, x.fields["branch_error"] = b, err.Error()
		}
	default:
		x.out.Branch, x.out.Unlanded = b, n
		if x.replaced || x.dropped {
			kept := keptBranch(co, x.Run.Ref, x.entry.Phase, x.Now)
			if err := gitx.RenameBranch(ctx, co, b, kept); err != nil {
				x.fields["rename_error"] = err.Error()
			} else {
				x.out.Branch = kept
			}
		}
	}
}

// keptBranch is the name a branch of run's worker on phase keeps once the plan it worked for is gone:
// baton/<run>/kept/<phase>-<YYYYMMDD-HHMM>, numbered when one of that name is there already.
func keptBranch(checkout, run, phase string, now time.Time) string {
	name := "baton/" + run + "/kept/" + phase + "-" + now.UTC().Format("20060102-1504")
	kept := name
	for n := 2; gitx.RevParse(checkout, "refs/heads/"+kept) != ""; n++ {
		kept = fmt.Sprintf("%s-%d", name, n)
	}
	return kept
}

// release gives the worker's phase back to its run, as long as this caller still holds the mark.
func (x *retirement) release() {
	e := x.entry
	var pl plan.Plan
	if x.Host {
		pl, _ = x.Run.LoadPlan()
	}
	x.Run.Update(func(st *state.State) error {
		cur := rosterEntry(st, e.Ref, e.Session)
		if cur == nil || cur.Retirer != x.By {
			return errKept // gone, or another took the mark over
		}
		switch {
		case !cur.Live():
		case !x.Host:
			state.ReleaseWorker(st, e.Ref, e.Session, x.Why, x.Now)
		case len(pl.Phases) > 0 && st.WorkerOn(e.Phase) == cur:
			x.out.Next, _ = state.Unassign(st, pl, e.Phase, x.Why, x.Now)
		default:
			cur.Status, cur.Since, cur.Why, cur.Merge = state.WorkerFailed, x.Now, x.Why, nil
		}
		cur.Unlanded, cur.WorktreeKept = x.out.Unlanded, x.out.Kept
		if x.out.Branch != "" {
			cur.Branch = x.out.Branch
		}
		return nil
	})
}

// event logs worker_ended in the run.
func (x *retirement) event() {
	f := x.fields
	if x.out.Branch != "" {
		f["branch"] = x.out.Branch
	}
	if x.out.Unlanded > 0 {
		f["unlanded"] = x.out.Unlanded
	}
	if x.out.Snapshot != "" {
		f["snapshot"] = x.out.Snapshot
	}
	if x.out.Kept != "" {
		f["worktree"], f["worktree_kept"] = x.entry.Worktree, x.out.Kept
	}
	if len(x.out.Jobs) > 0 {
		f["jobs"] = x.out.Jobs
	}
	if x.out.Next != "" {
		f["next"] = x.out.Next
	}
	x.Run.Event("worker_ended", f)
}
