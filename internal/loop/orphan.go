package loop

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A worker's sub-run can outlive its place on its run's roster: a plan attached over the run before v0.6
// wiped the roster, and a worker retired while its worktree could not be removed keeps it. And a
// directory can be left under baton's worktrees that no run names at all. baton clean ends what such a
// sub-run left (RetireOrphan), and removes such a directory (RemoveStray), the way RetireWorker would.

// RetireOrphan ends what a worker's sub-run left behind when its run has no live worker on it any more:
// its uncommitted work is saved, its jobs stopped, its worktree removed, and its branch removed or kept,
// as RetireWorker does. r.Entry describes the sub-run: its Ref, Phase and Worktree,
// the branch its worker worked on and the base it started from (or ""), and the Generation of its last
// roster entry, or -1 when the run has none: a branch kept for a plan the run no longer runs is renamed
// under kept/, so that no later worker carries on from it. Its last roster entry, if it has one, records
// what was kept. It logs worker_ended (orphaned) in the run.
func RetireOrphan(r Retirement) Retired {
	x := &retirement{Retirement: r, entry: r.Entry}
	if x.Logf == nil {
		x.Logf = func(string, ...any) {}
	}
	e := r.Entry
	x.fields = map[string]any{"phase": e.Phase, "why": r.Why, "by": r.By, "orphaned": true}
	st, err := r.Run.Load()
	switch {
	case err != nil:
		x.out.Skipped = "its run's state could not be read: " + err.Error()
		return x.out
	case liveOn(st, e.Ref):
		x.out.Skipped = "a worker works on it"
		return x.out
	}
	x.replaced, x.dropped, x.into = e.Generation < st.Generation, st.Mode == state.ModeIdle, st.RunBranch
	for _, step := range []func(){x.snapshot, x.stopJobs, x.removeWorktree, x.branch} {
		step()
	}
	r.Run.Update(func(st *state.State) error {
		var last *state.Worker
		for i := range st.Workers {
			if w := &st.Workers[i]; w.Ref == e.Ref {
				if w.Live() {
					return errKept // a worker took the phase up meanwhile
				}
				last = w
			}
		}
		if last == nil {
			return errKept
		}
		last.Unlanded, last.WorktreeKept = x.out.Unlanded, x.out.Kept
		if x.out.Branch != "" {
			last.Branch = x.out.Branch
		}
		return nil
	})
	x.unbind()
	x.event()
	return x.out
}

// liveOn reports a live worker on the run's roster whose sub-run is ref.
func liveOn(st state.State, ref string) bool {
	for _, w := range st.Workers {
		if w.Ref == ref && w.Live() {
			return true
		}
	}
	return false
}

// Stray is a directory under baton's worktrees for a checkout that no run names: what a worker's
// worktree leaves once its sub-run and its roster entry are gone.
type Stray struct {
	Home     string // baton's home: only a directory under its worktrees is removed
	Checkout string
	Dir      string
	Run      *state.Store // the run it was made for, which logs its snapshot (nil: that run is gone)
	Phase    string       // what its snapshot is named for
	Why      string
	Now      time.Time
	// Snapshot saves uncommitted work (gitx.Snapshot when nil). Logf is the caller's log (nil: none).
	Snapshot func(root string, at time.Time, phase, why string) (gitx.Saved, error)
	Logf     func(format string, a ...any)
}

// RemoveStray saves the uncommitted work in s.Dir while git still knows it as a worktree, and removes it,
// only from under baton's worktrees for the checkout. It returns the snapshot made ("" for none), and why
// the directory was left in place ("" when it was removed, or was gone).
func RemoveStray(s Stray) (snapshot, kept string) {
	base := worktreesOf(s.Home, s.Checkout)
	switch {
	case !isDir(s.Dir):
		return "", ""
	case s.Home == "" || !gitx.Within(s.Dir, base):
		return "", "it is not under baton's worktrees (" + base + ")"
	}
	// Only its own .git: inside a home directory kept in git, git would otherwise find that repository.
	if _, err := os.Lstat(filepath.Join(s.Dir, ".git")); err == nil && gitx.Usable(s.Dir) {
		take := s.Snapshot
		if take == nil {
			take = gitx.Snapshot
		}
		var saved gitx.Saved
		var err error
		if s.Run != nil {
			logf := s.Logf
			if logf == nil {
				logf = func(string, ...any) {}
			}
			saved, err = saveWork(s.Run, s.Dir, take, logf, s.Phase, s.Why)
		} else {
			saved, err = take(s.Dir, s.Now, s.Phase, s.Why)
		}
		if err != nil {
			return "", "its uncommitted work could not be saved: " + err.Error()
		}
		snapshot = saved.Ref
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	gitx.WorktreeRemove(ctx, s.Checkout, s.Dir) // git may have forgotten it already
	if isDir(s.Dir) {
		os.RemoveAll(s.Dir)
	}
	gitx.PruneUnder(ctx, s.Checkout, base)
	if parent := filepath.Dir(s.Dir); gitx.Within(parent, base) {
		os.Remove(parent) // its run's directory, once empty
	}
	os.Remove(base) // and the project's
	if isDir(s.Dir) {
		return snapshot, "it could not be removed"
	}
	return snapshot, ""
}

// worktreesOf is where baton keeps the workers' worktrees for checkout, by its real path.
func worktreesOf(home, checkout string) string {
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	return filepath.Join(home, "worktrees", lane.Key(checkout))
}
