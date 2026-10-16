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

// landingCrew is a crew whose worker on P1 is at work. A merge makes commits, so git has an identity.
func landingCrew(t *testing.T) *crew {
	t.Helper()
	x := newCrew(t, 1)
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	x.atWork("P1")
	return x
}

// finish makes the worker on phase finish it: it commits files in its worktree, and reports the phase
// done (verified on that commit, when verify says so), which completes its sub-run, and its turn ends:
// its hooks have been quiet long enough for baton to touch its worktree (workerQuiet). It returns the
// commit.
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
		st.Run.TurnOpen, st.Run.CompleteNotified, st.Run.LastActivity = false, true, x.now.Add(-workerQuiet)
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
	if !x.has("land_queued", `"phase":"P1"`) || !x.has("landed", `"phase":"P1"`, tip) || x.has("merged_in") || x.has("land_deferred") {
		t.Errorf("events:\n%s", x.events())
	}
	for _, want := range []string{"P1 is finished → its work waits to land", "P1 landed on the run's branch: 1 commit (dir/two.txt, one.txt)"} {
		if !strings.Contains(x.announced(), want) {
			t.Errorf("announced %q, want %q", x.announced(), want)
		}
	}
	x.tick(time.Second)
	if !slices.Equal(x.stopped, []string{before.AgentID}) {
		t.Fatalf("stopped %v, want the landed worker's agent", x.stopped)
	}
	x.tick(time.Second)
	if isDir(before.Worktree) || gitx.RevParse(x.repo, "refs/heads/"+before.Branch) != "" {
		t.Error("the landed worker's worktree or branch is still there")
	}
	if w := x.worker("P1"); w.Status != state.WorkerLanded || !x.has("worker_ended", `"why":"its work landed"`) || slices.Contains(x.tracked(), before.AgentID) {
		t.Errorf("worker %+v; baton's agents %v\n%s", w, x.tracked(), x.events())
	}
	if x.status("P2") == "" {
		t.Errorf("the freed slot went unused: %+v", x.state().Workers)
	}
}

// A worker's branch the run's branch moved on from has the run's branch merged in, never rebased: the
// worker's commit stays as it was, and the checkout fast-forwards to the merge.
func TestALandingMergesTheMovedBranchIn(t *testing.T) {
	x := landingCrew(t)
	lead := x.leadCommits(map[string]string{"lead.txt": "lead\n"})
	old := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	merged := gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1")
	if merged == old || !x.has("merged_in", `"phase":"P1"`, `"onto":"`+lead+`"`) || gitx.Head(x.repo) != lead {
		t.Fatalf("after a step: branch %s (was %s), HEAD %s\n%s", merged, old, gitx.Head(x.repo), x.events())
	}
	if !strings.Contains(x.announced(), "merged main into P1's branch") {
		t.Errorf("announced %q", x.announced())
	}
	x.tick(time.Second)
	if got := gitx.Head(x.repo); got != merged {
		t.Fatalf("HEAD = %s, want the merge %s\n%s", got, merged, x.events())
	}
	if parents := gittest.Git(t, x.repo, "rev-parse", "HEAD^1", "HEAD^2"); parents != old+"\n"+lead {
		t.Errorf("the landed merge's parents are %q, want the worker's %s and the lead's %s", parents, old, lead)
	}
	if msg := gittest.Git(t, x.repo, "log", "-1", "--format=%s"); msg != "Merge branch 'main' into baton/sess-1/P1" {
		t.Errorf("the merge's message is %q", msg)
	}
	if w := x.worker("P1"); w.Status != state.WorkerLanded {
		t.Errorf("worker %+v", w)
	}
}

// A phase verified before the run's branch moved on is verified again with the run's branch merged in: green
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
			x.tick(time.Second) // merged
			x.tick(time.Second) // verified again
			merged := gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1")
			sub := x.sub("P1")
			sst, _ := sub.Load()
			j := sst.Latest("P1")
			if j == nil || j.Status != state.JobQueued || j.Commit != merged || j.Command != "make test" || !strings.Contains(j.Notes, "all of it") ||
				sst.Phases["P1"].Status != state.PhaseVerifying || x.worker("P1").Merge.Reverify != j.ID {
				t.Fatalf("job %+v; P1 %+v; worker %+v", j, sst.Phases["P1"], x.worker("P1"))
			}
			if !x.has("reverify_queued", `"job":"`+j.ID+`"`, `"why":"lead"`) || !j.Requeue ||
				!strings.Contains(x.announced(), "commits were made on main in the checkout since P1 was verified → verifying it again with the run's branch merged in") {
				t.Errorf("events:\n%s", x.events())
			}
			x.tick(time.Second)
			if gitx.Head(x.repo) == merged {
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
				if gitx.Head(x.repo) != merged || x.worker("P1").Status != state.WorkerLanded {
					t.Fatalf("not landed after its verification passed\n%s", x.events())
				}
				if sst, _ := sub.Load(); len(sst.Inbox) > 0 {
					t.Errorf("the worker was told %+v, for a verification that passed", sst.Inbox)
				}
				return
			}
			sst, _ = sub.Load()
			if gitx.Head(x.repo) == merged || sst.Mode != state.ModeRunning || sst.Current != "P1" || x.worker("P1").Status != state.WorkerWorking || x.worker("P1").Merge != nil {
				t.Fatalf("after red: sub-run %s on %q; worker %+v", sst.Mode, sst.Current, x.worker("P1"))
			}
			// The phase is its agent's again at once: nothing compacts a phase agent, so no boundary is owed,
			// and the result is due to it.
			if ps := sst.Phases["P1"]; sst.BoundaryOwed || sst.Run.Compaction.InFlight() || ps.Status != state.PhaseActive {
				t.Fatalf("after red: owed %v, compaction %+v, P1 %+v", sst.BoundaryOwed, sst.Run.Compaction, ps)
			}
			if due := sst.Due(); len(due) != 1 || due[0].ID != j.ID || x.subLogged("P1", "compact_queued", nil) {
				t.Errorf("after red: due %+v\n%s", due, x.subEvents("P1"))
			}
			if !x.has("land_returned", `"phase":"P1"`) || x.state().Conflicts != 0 {
				t.Errorf("a red verification counted as a conflict (%d)\nevents:\n%s", x.state().Conflicts, x.events())
			}
			x.tick(5 * time.Second)
			if len(x.sent) != 1 || !strings.Contains(x.sent[0].text, "[baton] Job "+j.ID+" (P1) finished") {
				t.Errorf("sent %+v", x.sent)
			}
			if got := x.endTurn("P1"); !strings.Contains(got, j.ID) || !strings.Contains(got, "failed (exit 0)") {
				t.Errorf("its stop gave it %q", got)
			}
		})
	}
}

// A merge that conflicts is aborted, and the phase goes back to its worker, told what conflicted; once it
// resolves the conflict and reports the phase done again, it lands.
func TestAConflictGoesBackToTheWorker(t *testing.T) {
	x := landingCrew(t)
	x.leadCommits(map[string]string{"a.txt": "the lead's\n"})
	old := x.finish("P1", map[string]string{"a.txt": "the worker's\n"}, false)
	x.tick(time.Second)

	w := x.worker("P1")
	if gitx.Head(w.Worktree) != old || gittest.Git(t, w.Worktree, "status", "--porcelain") != "" {
		t.Fatal("the conflicted merge was not aborted")
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

	// The worker resolves it by merging, as it is told, and reports the phase done again. The run's branch
	// moves on once more meanwhile: baton merges that in too, and the resolution stays.
	exec.Command("git", "-C", w.Worktree, "merge", "main").Run()
	gittest.Write(t, w.Worktree, "a.txt", "both\n")
	gittest.Git(t, w.Worktree, "add", "a.txt")
	gittest.Git(t, w.Worktree, "commit", "-q", "--no-edit")
	resolved := gitx.Head(w.Worktree)
	x.leadCommits(map[string]string{"b.txt": "the lead's again\n"})
	sub := x.sub("P1")
	pl, _ := sub.LoadPlan()
	sub.Update(func(st *state.State) error {
		state.TakeMail(st)
		_, err := state.Done(st, pl, "P1", x.now, state.DoneOpts{})
		st.Run.TurnOpen = false
		return err
	})
	x.tick(time.Second)
	if !x.has("merged_in", `"from":"`+resolved+`"`) {
		t.Fatalf("the run's branch was not merged into the resolved branch\n%s", x.events())
	}
	x.tick(time.Second)
	if read(t, x.repo, "a.txt") != "both\n" || read(t, x.repo, "b.txt") != "the lead's again\n" || x.worker("P1").Status != state.WorkerLanded || x.state().Conflicts != 0 {
		t.Fatalf("not landed after the worker resolved it (%d conflicts on record)\n%s", x.state().Conflicts, x.events())
	}
	for _, c := range []string{old, resolved} {
		if ok, _ := gitx.IsAncestor(context.Background(), x.repo, c, "HEAD"); !ok {
			t.Errorf("the worker's commit %s is not on the run's branch: it was rewritten", c)
		}
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

// Nothing lands in the checkout while a lead's gate runs there (--here), this run's lead's or another's:
// the gate reads the checkout's files. The landing goes ahead at its next try once the gates have ended.
// A gate in a lane holds nothing.
func TestALandingWaitsForAGateInTheCheckout(t *testing.T) {
	x := landingCrew(t)
	x.lead.Update(func(st *state.State) error {
		st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make lint", Status: state.JobRunning, Queued: x.now},
			state.Job{ID: "j2", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Here: true, Status: state.JobRunning, Queued: x.now})
		return nil
	})
	other, _ := x.proj.Bind(state.Binding{Session: "sess-2", Instance: "inst-2"})
	other.WritePlan(x.pl)
	other.Update(func(st *state.State) error {
		*st = state.Reattach(*st, x.pl, x.now, state.Origin{})
		st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Here: true, Status: state.JobRunning, Queued: x.now}}
		return nil
	})
	end := func(s *state.Store, id string) {
		s.Update(func(st *state.State) error {
			j := st.Job(id)
			j.Status, j.Started, j.Ended = state.JobPassed, x.now, x.now
			return nil
		})
	}
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	x.tick(time.Second)
	if gitx.Head(x.repo) == tip {
		t.Fatal("landed while the lead's gate ran in the checkout")
	}
	if !x.has("land_deferred", `"why":"a gate runs in the checkout (job j2)"`) || !strings.Contains(x.announced(), "baton: P1 waits to land: a gate runs in the checkout (job j2)") {
		t.Errorf("events:\n%s", x.events())
	}
	end(x.lead, "j2")
	x.tick(overlapRetry)
	if gitx.Head(x.repo) == tip {
		t.Fatal("landed while another run's lead's gate ran in the checkout")
	}
	end(other, "j1")
	x.tick(overlapRetry)
	if gitx.Head(x.repo) != tip {
		t.Fatalf("not landed once no gate ran in the checkout (a lane's holds nothing)\n%s", x.events())
	}
	if !x.has("landed", `"phase":"P1"`) {
		t.Errorf("events:\n%s", x.events())
	}
}

// A gate a worker ran in its worktree (--here) runs there again with the run's branch merged in.
func TestALandingIsVerifiedAgainInItsWorktree(t *testing.T) {
	x := landingCrew(t)
	x.leadCommits(map[string]string{"lead.txt": "lead\n"})
	x.finish("P1", map[string]string{"one.txt": "one\n"}, true)
	x.sub("P1").Update(func(st *state.State) error { st.Latest("P1").Here = true; return nil })
	x.tick(time.Second) // merged
	x.tick(time.Second) // verified again
	sst, _ := x.sub("P1").Load()
	if j := sst.Latest("P1"); j == nil || j.Status != state.JobQueued || !j.Here || j.Commit != gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") {
		t.Fatalf("job %+v", j)
	}
}

// The merge queue verifies a landing again at most once for the checkout's own commits: those the lead
// makes while that runs are landed over, unverified with it, and the lead is told. A worker's landing
// meanwhile, of this run or of another in the checkout, verifies it again; a merge that stops still hands
// the phase back.
func TestTheLeadsCommitsVerifyALandingAgainOnce(t *testing.T) {
	for _, c := range []struct {
		name string
		// meanwhile is what came on the run's branch while the merge queue's verification ran: the lead's
		// commit, recorded as a landing of this run's or of another run's when it says so.
		landedBy string
		files    map[string]string
		want     string // landed, reverify (why landed), or returned
	}{
		{"the lead's", "", map[string]string{"lead2.txt": "lead\n"}, "landed"},
		{"this run's landing", "this", map[string]string{"two.txt": "two\n"}, "reverify"},
		{"another run's landing", "other", map[string]string{"two.txt": "two\n"}, "reverify"},
		{"the lead's, in conflict", "", map[string]string{"one.txt": "the lead's\n"}, "returned"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := landingCrew(t)
			x.leadCommits(map[string]string{"lead.txt": "lead\n"})
			x.finish("P1", map[string]string{"one.txt": "one\n"}, true)
			x.tick(time.Second) // merged
			x.tick(time.Second) // verified again: only the lead's commit came since the worker's own verification
			sub := x.sub("P1")
			sst, _ := sub.Load()
			j := sst.Latest("P1")
			if j == nil || !j.Requeue || !x.has("reverify_queued", `"why":"lead"`) {
				t.Fatalf("job %+v\n%s", j, x.events())
			}

			meanwhile := x.leadCommits(c.files)
			switch c.landedBy {
			case "this":
				x.lead.Update(func(st *state.State) error {
					st.Landed = append(st.Landed, state.Landing{Phase: "P2", Commits: []string{meanwhile}, Head: meanwhile, At: x.now, Told: true})
					return nil
				})
			case "other":
				other, _ := x.proj.Bind(state.Binding{Session: "sess-2", Instance: "inst-2"})
				other.WritePlan(x.pl)
				other.Update(func(st *state.State) error {
					*st = state.Reattach(*st, x.pl, x.now, state.Origin{})
					st.Landed = []state.Landing{{Phase: "P2", Commits: []string{meanwhile}, Head: meanwhile, At: x.now}}
					return nil
				})
			}
			sub.Update(func(st *state.State) error {
				j := st.Job(j.ID)
				j.Status, j.Started, j.Ended = state.JobPassed, x.now, x.now
				return nil
			})
			x.tick(time.Second) // the worker's own loop settles its job
			x.tick(time.Second) // merged again, onto the commit that came meanwhile
			x.tick(time.Second)

			reverified := strings.Count(x.events(), `"kind":"reverify_queued"`)
			switch c.want {
			case "landed":
				st := x.state()
				if x.worker("P1").Status != state.WorkerLanded || len(st.Landed) != 1 || reverified != 1 {
					t.Fatalf("not landed after one verification again (%d)\n%s", reverified, x.events())
				}
				l := st.Landed[0]
				if l.Verified != j.Commit || !slices.Equal(l.Unverified, []string{meanwhile}) || l.Head != gitx.Head(x.repo) {
					t.Errorf("landing %+v; verified on %s, over %s", l, j.Commit, meanwhile)
				}
				if !x.has("landed", `"unverified":["`+meanwhile+`"]`, `"verified":"`+j.Commit+`"`) ||
					!strings.Contains(x.announced(), "over the checkout's commit "+short(meanwhile)+", which its gate did not run with: the lead's next gate runs them together") {
					t.Errorf("events:\n%s\nannounced %q", x.events(), x.announced())
				}
				told := decide.Landed(st.Landed, "main")
				if want := "P1's gate passed on " + short(j.Commit) + ", before your commit " + short(meanwhile) + "; your next gate runs them together."; !strings.Contains(told, want) {
					t.Errorf("the lead is told %q, want %q", told, want)
				}
			case "reverify":
				sst, _ := sub.Load()
				again := sst.Latest("P1")
				if x.worker("P1").Status != state.WorkerLanding || reverified != 2 || again.ID == j.ID || !again.Requeue ||
					again.Commit != gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") || !x.has("reverify_queued", `"job":"`+again.ID+`"`, `"why":"landed"`) {
					t.Fatalf("not verified again after a landing (%d); job %+v\n%s", reverified, again, x.events())
				}
				if !strings.Contains(again.Notes, "since other work had landed on main meanwhile") || !strings.Contains(x.announced(), "other work landed on main since P1 was verified") {
					t.Errorf("notes %q\nannounced %q", again.Notes, x.announced())
				}
			case "returned":
				sst, _ := sub.Load()
				if w := x.worker("P1"); w.Status != state.WorkerWorking || w.Merge != nil || sst.Current != "P1" || reverified != 1 || len(x.state().Landed) != 0 {
					t.Fatalf("worker %+v; sub-run on %q\n%s", w, sst.Current, x.events())
				}
				if !x.has("land_returned", `"paths":["one.txt"]`) {
					t.Errorf("events:\n%s", x.events())
				}
			}
		})
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
		t.Fatalf("merged under a worker at work\n%s", x.events())
	}
	// A stop baton refused leaves the turn going, though the state says it ended: the session must
	// have been quiet a while too.
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastActivity = false, x.now; return nil })
	x.tick(time.Second)
	if gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") != old {
		t.Fatalf("merged under a worker a moment after its last hook\n%s", x.events())
	}
	x.tick(workerQuiet)
	x.tick(time.Second)
	if x.worker("P1").Status != state.WorkerLanded {
		t.Fatalf("not landed once it stopped\n%s", x.events())
	}
}

// A message on its way to a finished phase's agent would start it again in its worktree: nothing lands
// until the mod has run it, and the turn it starts has ended.
func TestALandingWaitsForAMessageOnItsWay(t *testing.T) {
	x := landingCrew(t)
	x.leadCommits(map[string]string{"lead.txt": "lead\n"})
	old := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.modOff = true
	if err := x.c.send(x.lead, x.c.session(x.worker("P1")), "[baton] a word"); err != nil {
		t.Fatal(err)
	}
	x.tick(workerQuiet)
	x.tick(time.Second)
	if gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") != old || x.worker("P1").Status != state.WorkerLanding {
		t.Fatalf("merged with a message on its way to its agent\n%s", x.events())
	}
	x.modOff = false
	x.runMod() // the message starts it again
	x.tick(workerQuiet)
	if gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") != old || len(x.resumes) != 1 {
		t.Fatalf("merged under the turn the message started (%v)\n%s", x.resumes, x.events())
	}
	x.stops("P1")
	x.tick(workerQuiet)
	x.tick(time.Second)
	if x.worker("P1").Status != state.WorkerLanded {
		t.Fatalf("not landed once its turn ended\n%s", x.events())
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
		!strings.Contains(x.announced(), "plan complete — all 4 phases done · phase agents saved ") ||
		!strings.Contains(x.announced(), " · 1 decision made without you — /baton status") {
		t.Fatalf("mode %s, notified %v\n%s", st.Mode, st.Run.CompleteNotified, x.events())
	}
	if n := st.Run.Notices; len(n) == 0 || n[len(n)-1].Kind != "plan_complete" || !strings.HasPrefix(n[len(n)-1].Text, "Demo: all 4 phases done · phase agents saved ") ||
		!strings.HasSuffix(n[len(n)-1].Text, " · 1 decision made without you — /baton status") {
		t.Errorf("notices %+v", st.Run.Notices)
	}
}

// A phase agent's work ends when its sub-run finished the phase, not when the merge queue took it, nor when
// it landed; the landing that completes the plan says what phase agents saved it, and logs the numbers.
func TestALandingThatCompletesThePlanSaysWhatAgentsSaved(t *testing.T) {
	x := landingCrew(t)
	t0 := x.now
	x.lead.Update(func(st *state.State) error {
		for id, span := range map[string][2]time.Duration{"P0": {-3 * time.Hour, -2 * time.Hour}, "P2": {-2 * time.Hour, -time.Hour}} {
			ps := st.Phases[id]
			ps.Status, ps.StartedAt, ps.DoneAt = state.PhaseDone, t0.Add(span[0]), t0.Add(span[1])
		}
		st.Phases["P3"].Status = state.PhaseDone // done with no times on record: no work, no wall clock
		st.Current = ""
		for i := range st.Workers {
			if st.Workers[i].Phase == "P1" {
				st.Workers[i].Started = t0.Add(-2 * time.Hour) // its agent has been at work for two hours
			}
		}
		return nil
	})
	x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	finished := x.now
	x.tick(time.Second)
	if w := x.worker("P1"); w.Status != state.WorkerLanded || !w.Finished.Equal(finished) {
		t.Fatalf("worker %s, finished %v, want its sub-run's done %v\n%s", w.Status, w.Finished, finished, x.events())
	}
	// P0 and P2 an hour each, and P1's agent two hours and a few seconds: four hours of work, in three
	// hours and a few seconds from P0's start to P1's landing.
	if want := "baton: plan complete — all 4 phases done · phase agents saved about 1h00m: 4h00m of phase work in 3h00m"; !slices.Contains(x.state().Run.Announce, want) {
		t.Errorf("announced %q, want %q", x.announced(), want)
	}
	landed := x.state().Phases["P1"].DoneAt
	work, wall := 2*time.Hour+finished.Sub(t0.Add(-2*time.Hour)), landed.Sub(t0.Add(-3*time.Hour))
	if !x.logged("plan_complete", map[string]any{"by": "landed", "agent_phases": 1, "work_seconds": int(work.Seconds()),
		"wall_seconds": int(wall.Seconds()), "saved_seconds": int((work - wall).Seconds())}) {
		t.Errorf("plan_complete logs other numbers than %v of work in %v\n%s", work, wall, x.events())
	}
}

// A worker waiting to land when the host ends is suspended with the rest, and the next host resumes it;
// its phase lands once it has stopped again.
func TestALandingWorkerIsSuspendedAndResumed(t *testing.T) {
	x := landingCrew(t)
	x.lead.Update(func(st *state.State) error { st.Mode = state.ModePaused; return nil })
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	x.c.Ended("the baton session ended", nil)
	if w := x.worker("P1"); w.Status != state.WorkerSuspended || w.Merge != nil {
		t.Fatalf("worker %+v", w)
	}
	x.proj.Unbind("inst")
	x.lead.Update(func(st *state.State) error { st.Mode = state.ModeRunning; return nil })

	x.c = x.coordinator("next", 1)
	x.tick(time.Second) // its agent starts again
	x.stops("P1")       // and stops: its phase is finished
	x.tick(workerQuiet)
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
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	x.atWork("P1", "P2")
	first := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = true; return nil }) // its session still at work
	x.tick(time.Second)
	tip := x.finish("P2", map[string]string{"two.txt": "two\n"}, false)
	x.tick(time.Second)
	if gitx.Head(x.repo) != tip || x.worker("P2").Status != state.WorkerLanded || x.worker("P1").Status != state.WorkerLanding {
		t.Fatalf("P2 waited behind P1\n%s", x.events())
	}
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = false; return nil })
	x.tick(time.Second) // P2's merged in
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

// A landing held by the lead's checkout on another branch pushes the human, once a landing however long
// it waits: only a human moves the checkout back.
func TestALandingOffTheRunsBranchPushesTheHumanOnce(t *testing.T) {
	x := landingCrew(t)
	gittest.Git(t, x.repo, "checkout", "-q", "-b", "elsewhere")
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	for range 10 {
		x.tick(overlapRetry)
	}
	if n := strings.Count(x.events(), `"kind":"land_off_branch"`); n != 1 || !x.has("land_off_branch", `"phase":"P1"`, `"branch":"elsewhere"`, `"run_branch":"main"`) {
		t.Fatalf("%d pushes\n%s", n, x.events())
	}
	if n := strings.Count(x.events(), `"notice":"land_off_branch"`); n != 1 {
		t.Errorf("the notice was sent %d times\n%s", n, x.events())
	}
	if m := x.worker("P1").Merge; m == nil || m.OffBranch.IsZero() {
		t.Errorf("the push is not on record: %+v", m)
	}
	gittest.Git(t, x.repo, "checkout", "-q", "main")
	x.tick(overlapRetry)
	if gitx.Head(x.repo) != tip {
		t.Fatalf("not landed once the checkout was back on main\n%s", x.events())
	}
}

// The lead's uncommitted changes that hold a landing for LandNudge get its model a message, once a
// landing, asking it to commit those files by path; the human is told it was asked.
func TestALandingHeldByTheLeadsChangesAsksItToCommitThem(t *testing.T) {
	x := landingCrew(t)
	const nudge = 2 * time.Minute
	x.c.Lead.Timing.LandNudge = nudge
	gittest.Write(t, x.repo, "a.txt", "the lead's, uncommitted\n")
	gittest.Write(t, x.repo, "b.txt", "the lead's, untracked\n")
	x.finish("P1", map[string]string{"a.txt": "the worker's\n", "b.txt": "the worker's\n", "c.txt": "the worker's\n"}, false)
	x.tick(time.Second)
	held := x.now
	if m := x.worker("P1").Merge; m == nil || !m.Overlap.Equal(held) {
		t.Fatalf("the overlap's start is not on record: %+v\n%s", m, x.events())
	}
	commit := func() []state.Message {
		var out []state.Message
		for _, m := range x.state().Inbox {
			if m.Kind == state.MessageCommit {
				out = append(out, m)
			}
		}
		return out
	}
	for x.now.Sub(held) < nudge-overlapRetry {
		x.tick(overlapRetry)
	}
	if len(commit()) != 0 || x.has("land_nudged") {
		t.Fatalf("asked before %s\n%s", nudge, x.events())
	}
	x.tick(overlapRetry)
	got := commit()
	if len(got) != 1 || !strings.Contains(got[0].Text, "git add -- a.txt b.txt") || got[0].Phase != "" {
		t.Fatalf("the lead's mail: %+v\n%s", got, x.events())
	}
	if !x.has("land_nudged", `"phase":"P1"`, `"paths":["a.txt","b.txt"]`, `"waited":"2m0s"`) ||
		!strings.Contains(x.announced(), "baton: P1 has waited 2m to land on the lead's uncommitted changes to a.txt, b.txt → asked the lead to commit them") {
		t.Errorf("not logged or announced\n%s", x.events())
	}
	for range 18 {
		x.tick(overlapRetry)
	}
	if n := strings.Count(x.events(), `"kind":"land_nudged"`); n != 1 || len(commit()) != 1 {
		t.Errorf("asked %d times, %d messages", n, len(commit()))
	}

	// Once the lead's changes are out of the way, the landing goes on, and a later overlap would count
	// afresh.
	gittest.Git(t, x.repo, "checkout", "--", "a.txt")
	os.Remove(filepath.Join(x.repo, "b.txt"))
	x.tick(overlapRetry)
	if w := x.worker("P1"); w.Status != state.WorkerLanded {
		t.Fatalf("not landed once the lead's changes were gone: %+v\n%s", w, x.events())
	}
}

// A landing the lead's changes hold for a while, then let go of before it is held again, counts its wait
// afresh: the lead is asked only once they have held it LandNudge in a row.
func TestALeadsChangesHoldALandingInARowBeforeItIsAsked(t *testing.T) {
	x := landingCrew(t)
	const nudge = 2 * time.Minute
	x.c.Lead.Timing.LandNudge = nudge
	x.finish("P1", map[string]string{"a.txt": "the worker's\n"}, false)
	x.c.Fleet.git = &holdGit{landGit: realGit{}, overlap: []string{"a.txt"}}
	g := x.c.Fleet.git.(*holdGit)
	x.tick(time.Second)
	for x.now.Sub(x.worker("P1").Merge.Overlap) < nudge/2 {
		x.tick(overlapRetry)
	}
	g.overlap = nil
	g.busy = true // held by something else, so it does not land
	x.tick(overlapRetry)
	if m := x.worker("P1").Merge; !m.Overlap.IsZero() {
		t.Fatalf("an overlap that ended is still counted: %+v", m)
	}
	g.overlap, g.busy = []string{"a.txt"}, false
	x.tick(overlapRetry)
	again := x.now
	for x.now.Sub(again) < nudge-overlapRetry {
		x.tick(overlapRetry)
	}
	if x.has("land_nudged") {
		t.Fatalf("asked before the overlap held %s in a row\n%s", nudge, x.events())
	}
	x.tick(overlapRetry)
	if !x.has("land_nudged", `"paths":["a.txt"]`) {
		t.Errorf("not asked\n%s", x.events())
	}
}

// holdGit is git that reports the lead's local changes to overlap in a landing's way, and, while busy,
// a fast-forward refused for changes it would overwrite.
type holdGit struct {
	landGit
	overlap []string
	busy    bool
}

func (g *holdGit) Overlap(root string, changes []gitx.Change) ([]string, error) {
	return g.overlap, nil
}

func (g *holdGit) FastForward(ctx context.Context, root, commit string) error {
	if g.busy {
		return gitx.ErrOverwrite
	}
	return g.landGit.FastForward(ctx, root, commit)
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
