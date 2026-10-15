package loop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// suspended leaves the run with a worker on P1 whose agent ended with its host, and no host: what a host
// that was stopped leaves behind.
func (x *crew) suspended() state.Worker {
	x.t.Helper()
	x.atWork("P1")
	x.c.Ended("the baton session ended", nil)
	w := x.worker("P1")
	x.proj.Unbind("inst")
	if w.Status != state.WorkerSuspended {
		x.t.Fatalf("worker %+v", w)
	}
	return w
}

// retirement is how a process other than the run's host (baton clean) retires worker w.
func (x *crew) retirement(w state.Worker) Retirement {
	run, err := x.proj.ExistingRun("sess-1", "")
	if err != nil {
		x.t.Fatal(err)
	}
	return Retirement{Project: x.proj, Home: x.home, Run: run, Entry: w, Why: "baton clean", By: "baton clean", Now: x.now}
}

func (x *crew) subEvents(phase string) string {
	b, _ := os.ReadFile(filepath.Join(x.sub(phase).Dir, "events.jsonl"))
	return string(b)
}

func (x *crew) branch(name string) bool { return gitx.RevParse(x.repo, "refs/heads/"+name) != "" }

// Any baton process can retire a worker no session runs: its uncommitted work is saved, its worktree
// removed (only from under baton's worktrees, and not if its work could not be saved), and its branch
// removed, or kept when it holds commits that never landed, under kept/ once the plan it worked for is
// gone. Its phase goes back to the run, whose phase in hand and compaction stay as they were.
func TestRetireWorker(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(x *crew, w *state.Worker, r *Retirement)
		check func(t *testing.T, x *crew, w state.Worker, got Retired)
	}{
		{"uncommitted work is saved", func(x *crew, w *state.Worker, r *Retirement) {
			gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			if got.Snapshot == "" || gitx.RevParse(x.repo, got.Snapshot) == "" || isDir(w.Worktree) || x.branch(w.Branch) {
				t.Errorf("retired %+v; worktree there %v, branch there %v", got, isDir(w.Worktree), x.branch(w.Branch))
			}
			if ev := x.subEvents("P1"); !strings.Contains(ev, `"kind":"snapshot"`) || !strings.Contains(ev, `"files":1`) {
				t.Errorf("the sub-run's events:\n%s", ev)
			}
			if ev := x.events(); !strings.Contains(ev, `"snapshot":"`+got.Snapshot+`"`) {
				t.Errorf("events:\n%s", ev)
			}
		}},
		{"its work cannot be saved", func(x *crew, w *state.Worker, r *Retirement) {
			gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
			r.Snapshot = func(string, time.Time, string, string) (gitx.Saved, error) {
				return gitx.Saved{}, errors.New("disk full")
			}
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			after := x.worker("P1")
			if !isDir(w.Worktree) || got.Kept == "" || after.WorktreeKept != got.Kept || !strings.Contains(got.Kept, "disk full") || got.Branch != w.Branch || !x.branch(w.Branch) {
				t.Errorf("retired %+v; entry %+v; worktree there %v", got, after, isDir(w.Worktree))
			}
			tree, _ := json.Marshal(w.Worktree) // a Windows path's backslashes are escaped
			if ev := x.events(); !strings.Contains(ev, `"worktree_kept":"its uncommitted work could not be saved: disk full"`) || !strings.Contains(ev, `"worktree":`+string(tree)) {
				t.Errorf("events:\n%s", ev)
			}
		}},
		{"commits that never landed keep the branch", func(x *crew, w *state.Worker, r *Retirement) {
			gittest.Write(t, w.Worktree, "one.txt", "committed\n")
			gittest.Commit(t, w.Worktree, "P1's work")
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			after := x.worker("P1")
			if got.Branch != w.Branch || got.Unlanded != 1 || !x.branch(w.Branch) || after.Branch != w.Branch || after.Unlanded != 1 || isDir(w.Worktree) {
				t.Errorf("retired %+v; entry %+v", got, after)
			}
			if ev := x.events(); !strings.Contains(ev, `"unlanded":1`) || !strings.Contains(ev, `"branch":"`+w.Branch+`"`) {
				t.Errorf("events:\n%s", ev)
			}
		}},
		{"commits of a replaced plan's worker are kept under kept/", func(x *crew, w *state.Worker, r *Retirement) {
			gittest.Write(t, w.Worktree, "one.txt", "committed\n")
			gittest.Commit(t, w.Worktree, "P1's work")
			x.lead.Update(func(st *state.State) error {
				*st = state.Reattach(*st, x.pl, x.now, state.Origin{})
				st.RunBranch = "main"
				return nil
			})
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			after := x.worker("P1")
			const kept = "baton/sess-1/kept/P1-20261011-1200"
			if got.Branch != kept || got.Unlanded != 1 || !x.branch(kept) || x.branch(w.Branch) || after.Branch != kept {
				t.Errorf("retired %+v; entry %+v", got, after)
			}
		}},
		{"a clean worker leaves nothing", nil, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			if got.Snapshot != "" || got.Branch != "" || got.Kept != "" || isDir(w.Worktree) || x.branch(w.Branch) || isDir(filepath.Dir(w.Worktree)) {
				t.Errorf("retired %+v; worktree's directory there %v", got, isDir(filepath.Dir(w.Worktree)))
			}
			if strings.Contains(gittest.Git(t, x.repo, "worktree", "list", "--porcelain"), w.Worktree) {
				t.Error("git still lists its worktree")
			}
		}},
		{"its worktree is gone already", func(x *crew, w *state.Worker, r *Retirement) {
			os.RemoveAll(w.Worktree)
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			if got.Kept != "" || x.branch(w.Branch) || strings.Contains(gittest.Git(t, x.repo, "worktree", "list", "--porcelain"), w.Worktree) {
				t.Errorf("retired %+v; branch there %v", got, x.branch(w.Branch))
			}
		}},
		{"a worktree outside baton's home stays", func(x *crew, w *state.Worker, r *Retirement) {
			r.Home = t.TempDir()
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			if !isDir(w.Worktree) || !strings.Contains(got.Kept, "not under baton's worktrees") || x.worker("P1").Status != state.WorkerFailed {
				t.Errorf("retired %+v; worktree there %v", got, isDir(w.Worktree))
			}
		}},
		{"a stale mark is taken over", func(x *crew, w *state.Worker, r *Retirement) {
			mark(x, w, "a host that died", x.now.Add(-state.RetireStale-time.Second))
		}, func(t *testing.T, x *crew, w state.Worker, got Retired) {
			if after := x.worker("P1"); after.Status != state.WorkerFailed || after.Retirer != "baton clean" {
				t.Errorf("entry %+v", after)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			w := x.suspended()
			r := x.retirement(w)
			if c.setup != nil {
				c.setup(x, &w, &r)
				r.Entry = w
			}
			before := x.state()
			if !slices.Contains(x.tracked(), w.AgentID) {
				t.Fatalf("its agent %q is none of baton's: %v", w.AgentID, x.tracked())
			}
			got := RetireWorker(r)
			if got.Skipped != "" {
				t.Fatalf("skipped: %s", got.Skipped)
			}
			if slices.Contains(x.tracked(), w.AgentID) {
				t.Errorf("its agent is still one of baton's: %v", x.tracked())
			}
			st := x.state()
			after := x.worker("P1")
			if after.Status != state.WorkerFailed || after.Why != "baton clean" || after.Retirer != "baton clean" || after.Retiring.IsZero() {
				t.Errorf("entry %+v", after)
			}
			if ps := st.Phases["P1"]; before.Generation == st.Generation && ps.Status != state.PhasePending {
				t.Errorf("P1 %+v", ps)
			}
			if st.Current != before.Current || st.BoundaryOwed != before.BoundaryOwed || !reflect.DeepEqual(st.Run.Compaction, before.Run.Compaction) {
				t.Errorf("the lead's phase %q (was %q), boundary %v, compaction %+v", st.Current, before.Current, st.BoundaryOwed, st.Run.Compaction)
			}
			if ev := x.events(); !strings.Contains(ev, `"kind":"worker_ended"`) || !strings.Contains(ev, `"by":"baton clean"`) || !strings.Contains(ev, `"why":"baton clean"`) {
				t.Errorf("events:\n%s", ev)
			}
			c.check(t, x, w, got)
		})
	}
}

// mark records that by began to retire worker w at at.
func mark(x *crew, w *state.Worker, by string, at time.Time) {
	x.lead.Update(func(st *state.State) error {
		e := st.Entry(*w)
		e.Retiring, e.Retirer = at, by
		*w = *e
		return nil
	})
}

// A worker is left alone when its sub-run is held elsewhere, when someone else is retiring it, or when it
// changed since the caller looked at it.
func TestRetireWorkerLeavesAlone(t *testing.T) {
	for _, c := range []struct {
		name, why string
		setup     func(x *crew, w *state.Worker)
	}{
		{"its sub-run's owner is live", "it works in another baton session", func(x *crew, w *state.Worker) {
			x.sub("P1").Update(func(st *state.State) error { return state.Claim(st, "other-P1", 1, x.now) })
		}},
		{"someone else is retiring it", "a new host is retiring it", func(x *crew, w *state.Worker) {
			mark(x, w, "a new host", x.now.Add(-time.Minute))
		}},
		{"it changed since it was read", "it changed since it was looked at", func(x *crew, w *state.Worker) {
			w.Since = w.Since.Add(-time.Hour)
		}},
		{"it is not on the roster", "it is no longer on its run's roster", func(x *crew, w *state.Worker) {
			w.Assigned = w.Assigned.Add(-time.Hour) // another worker on its phase
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			w := x.suspended()
			c.setup(x, &w)
			before := x.worker("P1")
			if got := RetireWorker(x.retirement(w)); got.Skipped != c.why {
				t.Errorf("skipped %q, want %q", got.Skipped, c.why)
			}
			if after := x.worker("P1"); !reflect.DeepEqual(after, before) || !isDir(w.Worktree) || strings.Contains(x.events(), `"by":"baton clean"`) {
				t.Errorf("entry %+v, was %+v; worktree there %v", after, before, isDir(w.Worktree))
			}
		})
	}
}

// The landing path retires a worker once its work has landed: its worktree and branch go, and it stays
// landed.
func TestRetireALandedWorker(t *testing.T) {
	x := newCrew(t, 1)
	w := x.suspended()
	gittest.Write(t, w.Worktree, "one.txt", "landed\n")
	gittest.Commit(t, w.Worktree, "P1's work")
	gittest.Git(t, x.repo, "merge", "-q", "--ff-only", w.Branch)
	x.lead.Update(func(st *state.State) error {
		_, err := state.Landed(st, x.pl, state.Landing{Phase: "P1", Commits: []string{gitx.Head(x.repo)}, Head: gitx.Head(x.repo)}, x.now)
		return err
	})
	r := x.retirement(x.worker("P1"))
	r.Host, r.By, r.Why = true, "inst", "its work landed"
	got := RetireWorker(r)
	if after := x.worker("P1"); got.Skipped != "" || after.Status != state.WorkerLanded || isDir(w.Worktree) || x.branch(w.Branch) || got.Unlanded != 0 {
		t.Errorf("retired %+v; entry %+v; worktree there %v; branch there %v", got, after, isDir(w.Worktree), x.branch(w.Branch))
	}
}

// A worker is never both resumed by its run's host and retired by another process, however the two
// interleave; whichever comes second leaves it alone.
func TestResumeAndRetireNeverBoth(t *testing.T) {
	for _, c := range []struct {
		name    string
		at      string // the step of retiring (retire:) or resuming (resume:) at which the other runs
		resumed bool   // which of the two wins
		fresh   bool   // a new worker takes the phase up, once it was retired
	}{
		{"a retire's mark, then a resume", "retire:mark", false, false},
		{"a retire's snapshot, then a resume", "retire:snapshot", false, false},
		{"a retire's worktree removed, then a resume", "retire:worktree", false, false},
		{"a retire done, then a resume", "retire:release", false, true},
		{"a resume's look, then a retire", "resume:read", false, true},
		{"a resume's claim, then a retire", "resume:claimed", true, false},
		{"a resume's guard, then a retire", "resume:guarded", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			w := x.suspended()
			gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
			x.c = x.coordinator("next", 1) // its first look at the workers resumes P1's
			sessions := len(x.spawns())
			r := x.retirement(w)
			var retired Retired
			side, step, _ := strings.Cut(c.at, ":")
			if side == "retire" {
				r.between = func(s string) {
					if s == step {
						x.tick(time.Second)
					}
				}
				retired = RetireWorker(r)
			} else {
				x.c.resumeStep = func(s string) {
					if s == step {
						r.Now = x.now
						retired = RetireWorker(r)
					}
				}
				x.tick(time.Second)
			}
			st := x.state()
			old := st.Entry(w)
			resumed := strings.Count(x.events(), `"kind":"worker_resumed"`) == 1
			switch {
			case resumed != c.resumed:
				t.Errorf("resumed %v, want %v", resumed, c.resumed)
			case resumed && (old.Status != state.WorkerWorking || retired.Skipped == "" || len(x.spawns()) != sessions+1): // by a message to its agent
				t.Errorf("resumed, and retired too (%q): entry %+v", retired.Skipped, old)
			case !resumed && (old.Status != state.WorkerFailed || retired.Skipped != "" || len(x.spawns()) != sessions+btoi(c.fresh)):
				t.Errorf("retired (%q), with %d sessions started: entry %+v\n%s", retired.Skipped, len(x.spawns())-sessions, old, x.events())
			case !resumed && !c.fresh && x.held("P1") == state.WorkerInstance("next", "P1"):
				t.Error("the resume that gave way still holds the sub-run")
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A host that looked at a worker before another process marked it as retiring does not resume it.
func TestAResumeGivesWayToARetiringMark(t *testing.T) {
	x := newCrew(t, 1)
	w := x.suspended()
	x.c = x.coordinator("next", 1)
	x.c.resumeStep = func(s string) {
		if s == "read" {
			mark(x, &w, "baton clean", x.now)
		}
	}
	x.tick(time.Second)
	if after := x.worker("P1"); len(x.spawns()) != 1 || after.Status != state.WorkerSuspended || after.Retirer != "baton clean" || x.held("P1") != "" {
		t.Errorf("%d sessions; entry %+v", len(x.spawns()), after)
	}
}

// A host resumes a worker whose retirer died midway, once the mark is stale.
func TestAStaleMarkDoesNotHoldAResume(t *testing.T) {
	x := newCrew(t, 1)
	w := x.suspended()
	mark(x, &w, "baton clean", x.now.Add(-state.RetireStale-time.Second))
	x.c = x.coordinator("next", 1)
	x.tick(time.Second)
	if after := x.worker("P1"); len(x.spawns()) != 2 || after.Status != state.WorkerWorking || !after.Retiring.IsZero() || after.Retirer != "" {
		t.Errorf("%d sessions; entry %+v", len(x.spawns()), after)
	}
}

// A plan attached over a run whose worker is on the roster retires that worker as the replaced plan's,
// in a host that runs its session or in the next one, and a worker of the new plan on a phase with the
// same id starts on a fresh branch from the run's branch, never on the old worker's commits.
func TestAReplacedPlansWorkerIsRetired(t *testing.T) {
	for _, c := range []struct {
		name      string
		suspended bool
	}{{"its agent works in this host", false}, {"it was suspended with its host", true}} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			var w state.Worker
			if c.suspended {
				w = x.suspended()
			} else {
				x.atWork("P1")
				w = x.worker("P1")
			}
			gittest.Write(t, w.Worktree, "old.txt", "the old plan's\n")
			gittest.Commit(t, w.Worktree, "the old plan's P1")
			replaced := plan.Plan{Version: 1, Title: "Next", File: "/plans/next.md", Phases: []plan.Phase{
				{ID: "P0", Title: "zero again", After: []string{}}, {ID: "P1", Title: "a new one", After: []string{}}}}
			x.lead.WritePlan(replaced)
			x.lead.Update(func(st *state.State) error {
				*st = state.Reattach(*st, replaced, x.now, state.Origin{})
				st.RunBranch = "main"
				return nil
			})
			x.pl = replaced
			if c.suspended {
				x.c = x.coordinator("next", 1)
			}
			x.tick(time.Second)
			if !c.suspended {
				if len(x.stopped) != 1 || x.stopped[0] != w.AgentID {
					t.Fatalf("its agent was not stopped: %v", x.stopped)
				}
				x.tick(time.Second)
			}
			st := x.state()
			old := st.Workers[0]
			const kept = "baton/sess-1/kept/P1-20261011-1200"
			if old.Status != state.WorkerFailed || old.Why != "the run's plan was replaced" || old.Branch != kept || !x.branch(kept) || old.Unlanded != 1 {
				t.Fatalf("the old worker %+v", old)
			}
			x.tick(time.Second)
			st = x.state()
			nw := st.WorkerOn("P1")
			if nw == nil || nw.Generation != st.Generation || nw.Branch != "baton/sess-1/P1" || gitx.Head(nw.Worktree) != gitx.Head(x.repo) ||
				nw.Base != gitx.Head(x.repo) || isFile(filepath.Join(nw.Worktree, "old.txt")) {
				t.Fatalf("the new plan's worker on P1: %+v (head %s, main %s)", nw, gitx.Head(nw.Worktree), gitx.Head(x.repo))
			}
		})
	}
}

// A host with workers off (no Fleet) retires the workers its run has on record, and the lead takes their
// phases up; with none on record, it does nothing at all, as before workers.
func TestAHostWithoutWorkersRetiresTheRunsWorkers(t *testing.T) {
	x := newCrew(t, 1)
	w := x.suspended()
	gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
	x.lead.Update(func(st *state.State) error { // the lead finished what it could: only P1's worker's phase is left before P3
		st.Phases["P0"].Status, st.Phases["P2"].Status, st.Current = state.PhaseDone, state.PhaseDone, ""
		return nil
	})
	x.c = x.coordinator("next", 1)
	x.c.Fleet = nil
	x.tick(time.Second)
	x.c.fleetWG.Wait()
	st := x.state()
	after := x.worker("P1")
	if after.Status != state.WorkerFailed || after.Why != "workers are off in this session" || isDir(w.Worktree) || len(x.spawns()) != 1 {
		t.Fatalf("entry %+v; worktree there %v; %d sessions", after, isDir(w.Worktree), len(x.spawns()))
	}
	if st.Current != "P1" || st.Run.Compaction.Status != state.CompactQueued {
		t.Errorf("the lead's phase %q, compaction %+v", st.Current, st.Run.Compaction)
	}
	if ev := x.events(); !strings.Contains(ev, `"next":"P1"`) || !strings.Contains(strings.Join(st.Run.Announce, "\n"), "the phase agent on P1 ended: workers are off in this session") {
		t.Errorf("events:\n%s", ev)
	}

	// Nothing on record: nothing runs.
	y := newCrew(t, 1)
	y.c.Fleet = nil
	y.tick(time.Second)
	y.c.fleetWG.Wait()
	if !y.c.fleetAt.IsZero() || len(y.state().Workers) != 0 {
		t.Errorf("a host without workers looked at them: %v", y.c.fleetAt)
	}
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// The lead's `baton done` recorded P1's work landed, its branch merged into main by hand, with what its
// agent had not committed still in its worktree: an agent at work on it is stopped, and the worker is
// retired as its work landed, by the host that runs it or, with no agent at work on it, by the next one.
// Its uncommitted work is saved first, and its branch goes, with nothing on it left to land. It is retired
// once.
func TestAWorkerTheLeadRecordedLandedIsRetired(t *testing.T) {
	for _, c := range []struct {
		name   string
		atWork bool
	}{{"its agent at work", true}, {"no agent at work on it", false}} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			var e state.Worker
			if c.atWork {
				x.atWork("P1")
				e = x.worker("P1")
			} else {
				e = x.suspended()
			}
			gittest.Write(t, e.Worktree, "parser.go", "package parser\n")
			gittest.Commit(t, e.Worktree, "the parser")
			gittest.Git(t, x.repo, "merge", "-q", "--ff-only", e.Branch)
			gittest.Write(t, e.Worktree, "notes.txt", "not committed\n")
			if _, err := x.lead.Update(func(st *state.State) error {
				head := gitx.Head(x.repo)
				_, err := state.Landed(st, x.pl, state.Landing{Phase: "P1", Commits: []string{head}, Head: head, Told: true, By: state.LandedByLead}, x.now)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !c.atWork {
				x.c = x.coordinator("next", 1)
			}
			for range 3 {
				x.tick(time.Second)
			}
			if c.atWork && !slices.Contains(x.stopped, x.agent("P1").id) {
				t.Errorf("its agent was not stopped: %v", x.stopped)
			}
			if w := x.worker("P1"); w.Status != state.WorkerLanded || w.Retiring.IsZero() || len(x.state().Unretired()) > 0 {
				t.Fatalf("worker %+v", w)
			}
			landed := map[string]any{"phase": "P1", "why": "its work landed"}
			if x.count("worker_ended", landed) != 1 {
				t.Fatalf("events:\n%s", x.events())
			}
			if isDir(e.Worktree) {
				t.Errorf("its worktree %s is still there", e.Worktree)
			}
			if gitx.RevParse(x.repo, "refs/heads/"+e.Branch) != "" {
				t.Errorf("its branch %s is still there", e.Branch)
			}
			saved := false
			for _, sn := range gitx.Snapshots(x.repo) {
				if strings.TrimSpace(gittest.Git(t, x.repo, "show", sn.Ref+":notes.txt")) == "not committed" {
					saved = true
				}
			}
			if !saved {
				t.Errorf("what its agent had not committed was not saved: %+v", gitx.Snapshots(x.repo))
			}
			x.tick(time.Second)
			if n := x.count("worker_ended", landed); n != 1 {
				t.Errorf("retired %d times", n)
			}
		})
	}
}
