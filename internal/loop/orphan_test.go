package loop

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// orphan describes the sub-run of worker w as baton clean finds it with no entry on the roster.
func orphan(w state.Worker) state.Worker {
	return state.Worker{Phase: w.Phase, Ref: w.Ref, Session: w.Session, Instance: w.Instance, Worktree: w.Worktree,
		Branch: w.Branch, Generation: -1}
}

func rosterHas(st state.State, ref string) bool {
	return slices.ContainsFunc(st.Workers, func(w state.Worker) bool { return w.Ref == ref })
}

// What a worker's sub-run left once its run has no worker on it is ended as RetireWorker would: its work
// saved, its worktree removed, and its branch kept under kept/ when it holds commits, so no later worker
// of the run carries on from them.
func TestRetireOrphan(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(x *crew, w *state.Worker, r *Retirement)
		check func(t *testing.T, x *crew, w state.Worker, got Retired)
	}{
		{"a roster wiped by an older baton", func(x *crew, w *state.Worker, r *Retirement) {
			gittest.Write(t, w.Worktree, "one.txt", "committed\n")
			gittest.Commit(t, w.Worktree, "P1's work")
			gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
			x.lead.Update(func(st *state.State) error { st.Workers = nil; return nil })
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			const kept = "baton/sess-1/kept/P1-20261011-1200"
			if got.Snapshot == "" || got.Branch != kept || got.Unlanded != 1 || !x.branch(kept) || x.branch(w.Branch) || isDir(w.Worktree) {
				t.Errorf("retired %+v; worktree there %v", got, isDir(w.Worktree))
			}
			if ev := x.events(); !strings.Contains(ev, `"orphaned":true`) || !strings.Contains(ev, `"branch":"`+kept+`"`) {
				t.Errorf("events:\n%s", ev)
			}
		}},
		{"a worktree kept when it was retired", func(x *crew, w *state.Worker, r *Retirement) {
			gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
			first := x.retirement(*w)
			first.Snapshot = func(string, time.Time, string, string) (gitx.Saved, error) {
				return gitx.Saved{}, errors.New("disk full")
			}
			if got := RetireWorker(first); got.Kept == "" {
				t.Fatalf("retired %+v", got)
			}
			*w = x.worker("P1")
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			if got.Snapshot == "" || got.Kept != "" || isDir(w.Worktree) || x.branch(w.Branch) {
				t.Errorf("retired %+v", got)
			}
			if e := x.worker("P1"); e.WorktreeKept != "" || e.Status != state.WorkerFailed {
				t.Errorf("entry %+v", e)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			w := x.suspended()
			r := x.retirement(w)
			c.setup(x, &w, &r)
			r.Entry = w
			if !rosterHas(x.state(), w.Ref) {
				r.Entry = orphan(w)
			}
			got := RetireOrphan(r)
			if got.Skipped != "" {
				t.Fatalf("skipped: %s", got.Skipped)
			}
			if x.proj.HostRun(w.Instance) != "" {
				t.Error("its instance still drives its sub-run")
			}
			c.check(t, x, w, got)
		})
	}

	t.Run("a worker works on it", func(t *testing.T) {
		x := newCrew(t, 1)
		w := x.suspended()
		r := x.retirement(w)
		r.Entry = orphan(w)
		if got := RetireOrphan(r); got.Skipped != "a worker works on it" || !isDir(w.Worktree) {
			t.Errorf("retired %+v", got)
		}
	})
}

// A directory under baton's worktrees that no run names is removed, its work saved while git still
// knows it; nothing outside baton's worktrees is.
func TestRemoveStray(t *testing.T) {
	x := newCrew(t, 1)
	base := worktreesOf(x.home, x.repo)
	known := filepath.Join(base, "gone-run", "P2")
	os.MkdirAll(filepath.Dir(known), 0o755)
	gittest.Git(t, x.repo, "worktree", "add", "-q", "--detach", known, "main")
	gittest.Write(t, known, "draft.txt", "not committed\n")
	forgotten := filepath.Join(base, "gone-run", "P3")
	gittest.Write(t, forgotten, "x.txt", "x\n")
	outside := t.TempDir()

	snap, kept := RemoveStray(Stray{Home: x.home, Checkout: x.repo, Dir: known, Phase: "P2", Why: "baton clean", Now: x.now})
	if snap == "" || kept != "" || isDir(known) || gitx.RevParse(x.repo, snap) == "" {
		t.Errorf("a worktree git knows: snapshot %q, kept %q, there %v", snap, kept, isDir(known))
	}
	if strings.Contains(gittest.Git(t, x.repo, "worktree", "list", "--porcelain"), known) {
		t.Error("git still lists it")
	}
	if snap, kept := RemoveStray(Stray{Home: x.home, Checkout: x.repo, Dir: forgotten, Phase: "P3", Why: "baton clean", Now: x.now}); snap != "" || kept != "" || isDir(forgotten) || isDir(base) {
		t.Errorf("one git forgot: snapshot %q, kept %q, there %v, its parents there %v", snap, kept, isDir(forgotten), isDir(base))
	}
	if _, kept := RemoveStray(Stray{Home: x.home, Checkout: x.repo, Dir: outside, Now: x.now}); !strings.Contains(kept, "not under baton's worktrees") || !isDir(outside) {
		t.Errorf("outside: kept %q", kept)
	}
	if snap, kept := RemoveStray(Stray{Home: x.home, Checkout: x.repo, Dir: known, Now: x.now}); snap != "" || kept != "" {
		t.Errorf("gone already: %q %q", snap, kept)
	}
}
