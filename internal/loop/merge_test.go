package loop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// landingCrew is a crew whose worker on P1 is at work. A rebase makes commits, so git has an identity.
func landingCrew(t *testing.T) *crew {
	t.Helper()
	x := newCrew(t, 1)
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	if w := x.worker("P1"); w.Status != state.WorkerWorking {
		t.Fatalf("P1's worker: %+v", w)
	}
	return x
}

// finish makes the worker on phase finish it: it commits files in its worktree, and reports the phase
// done (verified on that commit, when verify says so), which completes its sub-run, and its turn ends.
// It returns the commit.
func (x *crew) finish(phase string, files map[string]string, verify bool) string {
	t := x.t
	t.Helper()
	w := x.worker(phase)
	for name, content := range files {
		gittest.Write(t, w.Worktree, name, content)
	}
	gittest.Commit(t, w.Worktree, phase+": the worker's")
	tip := gitx.Head(w.Worktree)
	sub := x.sub(phase)
	pl, _ := sub.LoadPlan()
	if _, err := sub.Update(func(st *state.State) error {
		if ps := st.Phases[phase]; ps.Status == state.PhasePending {
			state.Start(st, x.now, state.Origin{})
		}
		if verify {
			q, _, _ := state.QueueJob(st, state.Job{Phase: phase, Kind: state.JobVerify, Command: "make test", Commit: tip, Notes: "all of it"}, x.now)
			j := st.Job(q.ID)
			j.Status, j.Started, j.Ended, j.Delivered = state.JobPassed, x.now, x.now, x.now
		}
		if _, err := state.Done(st, pl, phase, x.now, state.DoneOpts{}); err != nil {
			return err
		}
		st.Run.TurnOpen, st.Run.CompleteNotified = false, true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return tip
}

// leadCommits commits files on the run's branch in the checkout, as the lead does.
func (x *crew) leadCommits(files map[string]string) string {
	for name, content := range files {
		gittest.Write(x.t, x.repo, name, content)
	}
	paths := slices.Sorted(func(yield func(string) bool) {
		for name := range files {
			if !yield(name) {
				return
			}
		}
	})
	gittest.Commit(x.t, x.repo, "the lead's", paths...)
	return gitx.Head(x.repo)
}

func (x *crew) announced() string { return strings.Join(x.state().Run.Announce, "\n") }

func (x *crew) has(kind string, parts ...string) bool {
	for _, line := range strings.Split(x.events(), "\n") {
		if !strings.Contains(line, `"kind":"`+kind+`"`) {
			continue
		}
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			return true
		}
	}
	return false
}

// A finished worker's phase lands on the run's branch in the checkout, beside the lead's own uncommitted
// work: the phase is done, the lead is to be told, and the worker ends, its worktree and branch removed.
func TestAFinishedWorkersPhaseLands(t *testing.T) {
	x := landingCrew(t)
	gittest.Write(t, x.repo, "a.txt", "the lead's, uncommitted\n")
	gittest.Write(t, x.repo, "notes.txt", "untracked\n")
	before := x.worker("P1")
	tip := x.finish("P1", map[string]string{"one.txt": "one\n", "dir/two.txt": "two\n"}, false)
	x.tick(time.Second)

	if got := gitx.Head(x.repo); got != tip {
		t.Fatalf("the checkout's HEAD = %s, want the worker's %s\n%s", got, tip, x.events())
	}
	if gitx.Branch(x.repo) != "main" || read(t, x.repo, "a.txt") != "the lead's, uncommitted\n" || read(t, x.repo, "notes.txt") != "untracked\n" ||
		read(t, x.repo, "one.txt") != "one\n" {
		t.Error("the landing touched the lead's work, or did not bring the worker's")
	}
	st := x.state()
	if st.Phases["P1"].Status != state.PhaseDone || len(st.Landed) != 1 {
		t.Fatalf("P1 %+v, landed %+v", st.Phases["P1"], st.Landed)
	}
	l := st.Landed[0]
	if l.Phase != "P1" || !slices.Equal(l.Commits, []string{tip}) || !slices.Equal(l.Files, []string{"dir/two.txt", "one.txt"}) || l.Head != tip || l.Told {
		t.Errorf("landing %+v", l)
	}
	if !x.has("land_queued", `"phase":"P1"`) || !x.has("landed", `"phase":"P1"`, tip) || x.has("rebased") || x.has("land_deferred") {
		t.Errorf("events:\n%s", x.events())
	}
	for _, want := range []string{"P1 is finished → its work waits to land", "P1 landed on the run's branch: 1 commit (dir/two.txt, one.txt)"} {
		if !strings.Contains(x.announced(), want) {
			t.Errorf("announced %q, want %q", x.announced(), want)
		}
	}
	f := x.sessions[0]
	if f.ended != 1 {
		t.Fatalf("the landed worker's session was ended %d times", f.ended)
	}
	f.exit(0)
	x.tick(time.Second)
	if isDir(before.Worktree) || gitx.RevParse(x.repo, "refs/heads/"+before.Branch) != "" {
		t.Error("the landed worker's worktree or branch is still there")
	}
	if w := x.worker("P1"); w.Status != state.WorkerLanded || !x.has("worker_ended", `"why":"its work landed"`) {
		t.Errorf("worker %+v\n%s", w, x.events())
	}
	if w := x.worker("P2"); w.Status != state.WorkerStarting {
		t.Errorf("the freed slot went unused: %+v", w)
	}
}

// The run's branch moved on since the worker started: its branch is rebased onto it first, so the
// history stays a line.
func TestALandingRebasesOntoTheMovedBranch(t *testing.T) {
	x := landingCrew(t)
	lead := x.leadCommits(map[string]string{"lead.txt": "lead\n"})
	old := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	rebased := gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1")
	if rebased == old || !x.has("rebased", `"phase":"P1"`, `"onto":"`+lead+`"`) || gitx.Head(x.repo) != lead {
		t.Fatalf("after a step: branch %s (was %s), HEAD %s\n%s", rebased, old, gitx.Head(x.repo), x.events())
	}
	if !strings.Contains(x.announced(), "rebased P1's branch onto main") {
		t.Errorf("announced %q", x.announced())
	}
	x.tick(time.Second)
	if got := gitx.Head(x.repo); got != rebased {
		t.Fatalf("HEAD = %s, want the rebased %s\n%s", got, rebased, x.events())
	}
	if parent := gittest.Git(t, x.repo, "rev-parse", "HEAD~1"); parent != lead {
		t.Errorf("the landed commit's parent is %s, not the lead's %s", parent, lead)
	}
	if w := x.worker("P1"); w.Status != state.WorkerLanded {
		t.Errorf("worker %+v", w)
	}
}

// A phase verified before the run's branch moved on is verified again on its rebased branch: green
// lands it, and red takes it up again in the worker.
func TestALandingIsVerifiedAgain(t *testing.T) {
	for _, c := range []struct {
		name   string
		status string
	}{{"green", state.JobPassed}, {"red", state.JobFailed}} {
		t.Run(c.name, func(t *testing.T) {
			x := landingCrew(t)
			x.leadCommits(map[string]string{"lead.txt": "lead\n"})
			x.finish("P1", map[string]string{"one.txt": "one\n"}, true)
			x.tick(time.Second) // rebased
			x.tick(time.Second) // verified again
			rebased := gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1")
			sub := x.sub("P1")
			sst, _ := sub.Load()
			j := sst.Latest("P1")
			if j == nil || j.Status != state.JobQueued || j.Commit != rebased || j.Command != "make test" || !strings.Contains(j.Notes, "all of it") ||
				sst.Phases["P1"].Status != state.PhaseVerifying || x.worker("P1").Merge.Reverify != j.ID {
				t.Fatalf("job %+v; P1 %+v; worker %+v", j, sst.Phases["P1"], x.worker("P1"))
			}
			if !x.has("reverify_queued", `"job":"`+j.ID+`"`) || !strings.Contains(x.announced(), "verifying it again on its rebased branch") {
				t.Errorf("events:\n%s", x.events())
			}
			x.tick(time.Second)
			if gitx.Head(x.repo) == rebased {
				t.Fatal("landed before its verification passed")
			}

			sub.Update(func(st *state.State) error {
				x := st.Job(j.ID)
				x.Status, x.Ended = c.status, x.Started
				return nil
			})
			x.tick(time.Second) // the worker's own loop settles its job
			x.tick(time.Second)
			if c.status == state.JobPassed {
				if gitx.Head(x.repo) != rebased || x.worker("P1").Status != state.WorkerLanded {
					t.Fatalf("not landed after its verification passed\n%s", x.events())
				}
				if typed := x.typedTo["P1"].text; len(typed) > 0 {
					t.Errorf("the worker was told %q, for a verification that passed", typed)
				}
				return
			}
			sst, _ = sub.Load()
			if gitx.Head(x.repo) == rebased || sst.Mode != state.ModeRunning || sst.Current != "P1" || x.worker("P1").Status != state.WorkerWorking || x.worker("P1").Merge != nil {
				t.Fatalf("after red: sub-run %s on %q; worker %+v", sst.Mode, sst.Current, x.worker("P1"))
			}
			if !x.has("land_returned", `"phase":"P1"`) || x.state().Conflicts != 0 {
				t.Errorf("a red verification counted as a conflict (%d)\nevents:\n%s", x.state().Conflicts, x.events())
			}
		})
	}
}

// A rebase that conflicts is aborted, and the phase goes back to its worker, told what conflicted; once it
// resolves the conflict and reports the phase done again, it lands.
func TestAConflictGoesBackToTheWorker(t *testing.T) {
	x := landingCrew(t)
	x.leadCommits(map[string]string{"a.txt": "the lead's\n"})
	old := x.finish("P1", map[string]string{"a.txt": "the worker's\n"}, false)
	x.tick(time.Second)

	w := x.worker("P1")
	if gitx.Head(w.Worktree) != old || gittest.Git(t, w.Worktree, "status", "--porcelain") != "" {
		t.Fatal("the conflicted rebase was not aborted")
	}
	sst, _ := x.sub("P1").Load()
	if w.Status != state.WorkerWorking || w.Merge != nil || sst.Mode != state.ModeRunning || sst.Current != "P1" || sst.Phases["P1"].Status != state.PhaseActive {
		t.Fatalf("worker %+v; sub-run %s on %q", w, sst.Mode, sst.Current)
	}
	if len(sst.Inbox) != 1 || sst.Inbox[0].Text != decide.LandConflict("P1", "main", []string{"a.txt"}) {
		t.Errorf("the worker's inbox: %+v", sst.Inbox)
	}
	if !x.has("land_returned", `"paths":["a.txt"]`) || !strings.Contains(x.announced(), "P1's work did not land: its branch conflicts with the run's in a.txt") {
		t.Errorf("events:\n%s\nannounced %q", x.events(), x.announced())
	}
	if n := x.state().Conflicts; n != 1 {
		t.Errorf("%d landings in a row conflicted, want 1", n)
	}
	x.tick(time.Second)
	if typed := x.typedTo["P1"].text; !slices.Contains(typed, decide.MailTyped([]string{state.MessageLand})) {
		t.Errorf("typed into the idle worker: %q", typed)
	}

	// The worker resolves it, and reports the phase done again.
	exec.Command("git", "-C", w.Worktree, "rebase", "main").Run()
	gittest.Write(t, w.Worktree, "a.txt", "both\n")
	gittest.Git(t, w.Worktree, "add", "a.txt")
	gittest.Git(t, w.Worktree, "-c", "core.editor=true", "rebase", "--continue")
	sub := x.sub("P1")
	pl, _ := sub.LoadPlan()
	sub.Update(func(st *state.State) error {
		state.TakeMail(st)
		_, err := state.Done(st, pl, "P1", x.now, state.DoneOpts{})
		st.Run.TurnOpen = false
		return err
	})
	x.tick(time.Second)
	if read(t, x.repo, "a.txt") != "both\n" || x.worker("P1").Status != state.WorkerLanded || x.state().Conflicts != 0 {
		t.Fatalf("not landed after the worker resolved it (%d conflicts on record)\n%s", x.state().Conflicts, x.events())
	}
}

// The lead's local changes to a file the worker's branch changes, ignored files included, hold the
// landing until they are gone; it is tried again now and then, not every step.
func TestALandingWaitsForTheLeadsChanges(t *testing.T) {
	x := landingCrew(t)
	w := x.worker("P1")
	gittest.Write(t, w.Worktree, "build.log", "the worker's\n")
	gittest.Git(t, w.Worktree, "add", "-f", "build.log") // the crew's checkout ignores nothing, but the lead's file may be ignored
	gittest.Write(t, x.repo, "a.txt", "the lead's, uncommitted\n")
	tip := x.finish("P1", map[string]string{"a.txt": "the worker's\n"}, false)
	x.tick(time.Second)
	if gitx.Head(x.repo) == tip || x.worker("P1").Merge.Deferred == "" {
		t.Fatalf("landed over the lead's change\n%s", x.events())
	}
	if !x.has("land_deferred", `"paths":["a.txt"]`) || !strings.Contains(x.announced(), "P1 waits to land: the lead has local changes to a.txt") {
		t.Errorf("events:\n%s", x.events())
	}
	gittest.Git(t, x.repo, "checkout", "--", "a.txt")
	gittest.Write(t, x.repo, "build.log", "the lead's ignored log\n")
	os.WriteFile(filepath.Join(x.repo, ".git", "info", "exclude"), []byte(".baton/\nbuild.log\n"), 0o644)
	x.tick(time.Second)
	if gitx.Head(x.repo) == tip {
		t.Fatal("tried again on the next step")
	}
	x.tick(overlapRetry)
	if gitx.Head(x.repo) == tip || !x.has("land_deferred", `"paths":["build.log"]`) {
		t.Fatalf("landed over the lead's ignored file\n%s", x.events())
	}
	os.Remove(filepath.Join(x.repo, "build.log"))
	x.tick(overlapRetry)
	if gitx.Head(x.repo) != tip || read(t, x.repo, "a.txt") != "the worker's\n" {
		t.Fatalf("not landed once the lead's changes were gone\n%s", x.events())
	}
	if n := strings.Count(x.events(), `"kind":"land_deferred"`); n != 2 {
		t.Errorf("%d land_deferred events, want one for each reason", n)
	}
}

// Nothing lands while a tool call of a lead in the checkout runs: this run's lead, or another's; a
// stale call holds nothing.
func TestALandingWaitsForTheLeadsToolCalls(t *testing.T) {
	x := landingCrew(t)
	x.lead.Update(func(st *state.State) error {
		st.Run.ToolStarted(state.ToolCall{ID: "t1", Tool: "Edit", Since: x.now})
		st.Run.ToolStarted(state.ToolCall{ID: "t0", Tool: "Bash", Since: x.now.Add(-state.ToolStale)})
		return nil
	})
	other, _ := x.proj.Bind(state.Binding{Session: "sess-2", Instance: "inst-2"})
	other.WritePlan(x.pl)
	other.Update(func(st *state.State) error {
		*st = state.Reattach(*st, x.pl, x.now, state.Origin{})
		st.Run.ToolStarted(state.ToolCall{ID: "o1", Tool: "Write", Since: x.now})
		return nil
	})
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	x.tick(time.Second)
	if gitx.Head(x.repo) == tip {
		t.Fatal("landed while the lead's Edit ran")
	}
	x.lead.Update(func(st *state.State) error { st.Run.ToolEnded("t1", "", "Edit"); return nil })
	x.tick(time.Second)
	if gitx.Head(x.repo) == tip {
		t.Fatal("landed while another run's lead's Write ran in the checkout")
	}
	other.Update(func(st *state.State) error { st.Run.ToolsEnded(""); return nil })
	x.tick(time.Second)
	if gitx.Head(x.repo) != tip {
		t.Fatalf("not landed once no call ran (a stale one does not count)\n%s", x.events())
	}
	if x.has("land_deferred") {
		t.Error("a running tool call was logged as a deferral")
	}
}

// flaky is git whose fast-forwards fail as the test says, before they go through.
type flaky struct {
	landGit
	mu   sync.Mutex
	errs []error
	ffs  int
}

func (f *flaky) FastForward(ctx context.Context, root, commit string) error {
	f.mu.Lock()
	f.ffs++
	var err error
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.landGit.FastForward(ctx, root, commit)
}

// A fast-forward that does not go through is tried again: soon after a locked index, at the next step
// after the run's branch moved on, and later after anything else.
func TestAFailedFastForwardIsTriedAgain(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		retry time.Duration
		event string
	}{
		{"a locked index", gitx.ErrLocked, lockedRetry, ""},
		{"not a fast-forward", gitx.ErrNotFastForward, fleetEvery, ""},
		{"would overwrite", gitx.ErrOverwrite, overlapRetry, "land_deferred"},
		{"anything else", errors.New("disk full"), failedRetry, "land_deferred"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := landingCrew(t)
			g := &flaky{landGit: realGit{}, errs: []error{c.err}}
			x.c.Fleet.git = g
			tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
			x.tick(time.Second)
			if gitx.Head(x.repo) == tip || g.ffs != 1 {
				t.Fatalf("after the failure: %d fast-forwards", g.ffs)
			}
			if c.retry > fleetEvery {
				x.tick(c.retry - time.Millisecond)
				if g.ffs != 1 {
					t.Fatalf("tried again within %s", c.retry)
				}
			}
			x.tick(time.Second)
			if gitx.Head(x.repo) != tip || g.ffs != 2 {
				t.Fatalf("not landed after %s: %d fast-forwards\n%s", c.retry, g.ffs, x.events())
			}
			if c.event != "" && !x.has(c.event) || c.event == "" && x.has("land_deferred") {
				t.Errorf("events:\n%s", x.events())
			}
		})
	}
}

// A paused run lands nothing: its workers' work waits, queued, until it runs again.
func TestAPausedRunLandsNothing(t *testing.T) {
	x := landingCrew(t)
	x.lead.Update(func(st *state.State) error { st.Mode = state.ModePaused; return nil })
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	x.tick(time.Second)
	if gitx.Head(x.repo) == tip || x.worker("P1").Status != state.WorkerLanding {
		t.Fatalf("paused: HEAD %s, worker %+v", gitx.Head(x.repo), x.worker("P1"))
	}
	x.lead.Update(func(st *state.State) error { st.Mode = state.ModeRunning; return nil })
	x.tick(time.Second)
	if gitx.Head(x.repo) != tip {
		t.Fatalf("not landed once the run went on\n%s", x.events())
	}
}

// A worker still in its turn is not touched: its worktree is its own until it has stopped.
func TestALandingWaitsForTheWorkerToStop(t *testing.T) {
	x := landingCrew(t)
	x.leadCommits(map[string]string{"lead.txt": "lead\n"})
	old := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = true; return nil })
	x.tick(time.Second)
	x.tick(time.Second)
	if gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") != old || x.worker("P1").Status != state.WorkerLanding {
		t.Fatalf("rebased under a worker at work\n%s", x.events())
	}
	// A stop baton refused leaves the turn going, though the state says it ended: the session must
	// have been quiet a while too.
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastActivity = false, x.now; return nil })
	x.tick(time.Second)
	if gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") != old {
		t.Fatalf("rebased under a worker a moment after its last hook\n%s", x.events())
	}
	x.tick(workerQuiet)
	x.tick(time.Second)
	if x.worker("P1").Status != state.WorkerLanded {
		t.Fatalf("not landed once it stopped\n%s", x.events())
	}
}

// The lead idle with no phase in hand takes up the phase that waited for the landed one, after a
// compaction; and the landing that finishes the plan completes it.
func TestALandingMovesTheRunOn(t *testing.T) {
	x := landingCrew(t)
	x.lead.Update(func(st *state.State) error {
		for _, id := range []string{"P0", "P2"} {
			st.Phases[id].Status = state.PhaseDone
		}
		st.Current = ""
		return nil
	})
	x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	st := x.state()
	if st.Current != "P3" || !st.BoundaryOwed || st.Run.Compaction.Status != state.CompactQueued || !x.has("compact_queued", `"by":"landed"`, `"next":"P3"`) {
		t.Fatalf("the lead holds %q (owed %v, compaction %s)\n%s", st.Current, st.BoundaryOwed, st.Run.Compaction.Status, x.events())
	}

	x = landingCrew(t)
	x.lead.Update(func(st *state.State) error {
		for _, id := range []string{"P0", "P2", "P3"} {
			st.Phases[id].Status = state.PhaseDone
		}
		st.Current = ""
		return nil
	})
	x.sub("P1").Update(func(st *state.State) error {
		_, err := state.AddNote(st, "used a fake clock", "revert it", x.now)
		return err
	})
	x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	st = x.state()
	if st.Mode != state.ModeComplete || !st.Run.CompleteNotified || !x.has("plan_complete", `"by":"landed"`) ||
		!strings.Contains(x.announced(), "plan complete — all 4 phases done · 1 decision made without you — /baton status") {
		t.Fatalf("mode %s, notified %v\n%s", st.Mode, st.Run.CompleteNotified, x.events())
	}
	if n := st.Run.Notices; len(n) == 0 || n[len(n)-1].Kind != "plan_complete" || n[len(n)-1].Text != "Demo: all 4 phases done · 1 decision made without you — /baton status" {
		t.Errorf("notices %+v", st.Run.Notices)
	}
}

// A worker waiting to land when the host ends is suspended with the rest, and the next host resumes it;
// its phase lands once it has stopped again.
func TestALandingWorkerIsSuspendedAndResumed(t *testing.T) {
	x := landingCrew(t)
	x.lead.Update(func(st *state.State) error { st.Mode = state.ModePaused; return nil })
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	_, f := x.last()
	f.exit(0)
	x.c.Ended("the baton session ended")
	if w := x.worker("P1"); w.Status != state.WorkerSuspended || w.Merge != nil {
		t.Fatalf("worker %+v", w)
	}
	x.proj.Unbind("inst-P1")
	x.proj.Unbind("inst")
	x.lead.Update(func(st *state.State) error { st.Mode = state.ModeRunning; return nil })

	x.c = x.coordinator("next", 1)
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	if gitx.Head(x.repo) != tip || x.worker("P1").Status != state.WorkerLanded {
		t.Fatalf("not landed after the next host resumed it: %+v\n%s", x.worker("P1"), x.events())
	}
}

// gate is git whose fast-forward waits for the test, to hold a landing under way.
type gate struct {
	landGit
	in, out chan struct{}
}

func (g *gate) FastForward(ctx context.Context, root, commit string) error {
	g.in <- struct{}{}
	<-g.out
	return g.landGit.FastForward(ctx, root, commit)
}

// A tool call of the lead's and a landing never overlap: a call recorded before the landing took the
// lock holds it back, and one recorded after waits until the landing is over.
func TestToolCallsAndLandingsNeverOverlap(t *testing.T) {
	x := landingCrew(t)
	g := &gate{landGit: realGit{}, in: make(chan struct{}), out: make(chan struct{})}
	x.c.Fleet.git = g
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	done := make(chan struct{})
	go func() {
		x.tick(time.Second)
		close(done)
	}()
	<-g.in // the landing holds the lock, and found no call running

	// A call now: recorded first, as PreToolUse does, then it waits for the landing.
	x.lead.Update(func(st *state.State) error {
		st.Run.ToolStarted(state.ToolCall{ID: "t1", Tool: "Edit", Since: x.clock()})
		return nil
	})
	var landedFirst bool
	waited := make(chan struct{})
	go func() {
		state.WaitLanding(x.proj.Dir, 10*time.Second)
		landedFirst = gitx.Head(x.repo) == tip
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("the call ran while the landing was under way")
	case <-time.After(200 * time.Millisecond):
	}
	g.out <- struct{}{}
	<-done
	<-waited
	if !landedFirst {
		t.Error("the call ran before the landing it waited for")
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// sneak is git that records a tool call of the lead's just before a landing takes the lock, as a
// PreToolUse that ran between baton's first look and the lock would.
type sneak struct {
	landGit
	x *crew
}

func (g *sneak) Commits(ctx context.Context, root, from, to string) ([]string, error) {
	g.x.lead.Update(func(st *state.State) error {
		st.Run.ToolStarted(state.ToolCall{ID: "late", Tool: "Edit", Since: g.x.clock()})
		return nil
	})
	return g.landGit.Commits(ctx, root, from, to)
}

// A call recorded after baton first looked, but before it took the lock, holds the landing back: baton
// looks again under the lock.
func TestACallRecordedBeforeTheLockHoldsTheLanding(t *testing.T) {
	x := landingCrew(t)
	x.c.Fleet.git = &sneak{landGit: realGit{}, x: x}
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	if gitx.Head(x.repo) == tip {
		t.Fatal("landed while a call recorded before the lock ran")
	}
}

// The first worker queued waiting does not hold up the next: each lands as soon as it can.
func TestAWaitingLandingHoldsUpNoOther(t *testing.T) {
	x := newCrew(t, 2)
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	x.tick(time.Second)
	x.tick(time.Second)
	x.started("P1")
	x.started("P2")
	x.tick(time.Second)
	first := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = true; return nil }) // its session still at work
	x.tick(time.Second)
	tip := x.finish("P2", map[string]string{"two.txt": "two\n"}, false)
	x.tick(time.Second)
	if gitx.Head(x.repo) != tip || x.worker("P2").Status != state.WorkerLanded || x.worker("P1").Status != state.WorkerLanding {
		t.Fatalf("P2 waited behind P1\n%s", x.events())
	}
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = false; return nil })
	x.tick(time.Second) // rebased onto P2's
	x.tick(time.Second)
	if x.worker("P1").Status != state.WorkerLanded || gitx.Head(x.repo) == first || read(t, x.repo, "one.txt") != "one\n" {
		t.Fatalf("P1 did not land after P2\n%s", x.events())
	}
}

// The lead's checkout on another branch than the run's lands nothing, until it is back.
func TestALandingWaitsForTheRunsBranch(t *testing.T) {
	x := landingCrew(t)
	gittest.Git(t, x.repo, "checkout", "-q", "-b", "elsewhere")
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	if gitx.RevParse(x.repo, "refs/heads/elsewhere") == tip || gitx.RevParse(x.repo, "refs/heads/main") == tip {
		t.Fatal("landed with the checkout on another branch")
	}
	if !x.has("land_deferred", `the lead's checkout is on \"elsewhere\"`) {
		t.Errorf("events:\n%s", x.events())
	}
	gittest.Git(t, x.repo, "checkout", "-q", "main")
	x.tick(overlapRetry)
	if gitx.Head(x.repo) != tip {
		t.Fatalf("not landed once the checkout was back on main\n%s", x.events())
	}
}

// scribble is git whose fast-forward goes with a write to one of the lead's files, as a process of the
// lead's might make at that moment.
type scribble struct {
	landGit
	path string
}

func (g *scribble) FastForward(ctx context.Context, root, commit string) error {
	err := g.landGit.FastForward(ctx, root, commit)
	os.WriteFile(g.path, []byte("changed meanwhile\n"), 0o644)
	return err
}

// After a landing, HEAD is the branch's tip and the lead's local changes are as they were; anything else
// is reported.
func TestALandingIsChecked(t *testing.T) {
	x := landingCrew(t)
	gittest.Write(t, x.repo, "a.txt", "the lead's, uncommitted\n")
	x.c.Fleet.git = &scribble{landGit: realGit{}, path: filepath.Join(x.repo, "a.txt")}
	x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	if x.worker("P1").Status != state.WorkerLanded {
		t.Fatalf("not landed\n%s", x.events())
	}
	if !x.has("land_check", `"problem":"the lead's local changes to a.txt changed as it landed"`) ||
		!strings.Contains(x.announced(), "after P1 landed, the lead's local changes to a.txt changed as it landed") {
		t.Errorf("not reported\n%s", x.events())
	}

	x = landingCrew(t)
	gittest.Write(t, x.repo, "a.txt", "the lead's, uncommitted\n")
	x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	if x.has("land_check") {
		t.Errorf("a clean landing was reported\n%s", x.events())
	}
}
