package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// landUntil ticks until phase's worker landed, a few times at most.
func (x *crew) landUntil(phase string) {
	x.t.Helper()
	for range 4 {
		if x.status(phase) == state.WorkerLanded {
			return
		}
		x.tick(time.Second)
	}
	x.t.Fatalf("%s did not land\n%s", phase, x.events())
}

// The lead moved its checkout to a branch of its own, made from the run's and with a commit of the lead's
// on it, as a plan that makes a session branch at approval does: the run's branch follows it, told to the
// human and, at its next hook, the lead's model, and a finished phase lands there, with that commit
// merged into its branch. The branch the run had stays as it was, and the next agent starts from the
// branch followed.
func TestTheRunFollowsTheLeadsBranch(t *testing.T) {
	x := landingCrew(t)
	main := gitx.Head(x.repo)
	gittest.Git(t, x.repo, "checkout", "-q", "-b", "fix")
	lead := x.leadCommits(map[string]string{"fix.txt": "the lead's\n"})
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.landUntil("P1")

	st := x.state()
	if st.RunBranch != "fix" || !landedOn(t, x.repo, "fix", tip) || !landedOn(t, x.repo, "fix", lead) || gitx.RevParse(x.repo, "refs/heads/main") != main {
		t.Fatalf("run's branch %q; main %s (was %s)\n%s", st.RunBranch, gitx.RevParse(x.repo, "refs/heads/main"), main, x.events())
	}
	if n := strings.Count(x.events(), `"kind":"run_branch_followed"`); n != 1 || !x.has("run_branch_followed", `"from":"main"`, `"to":"fix"`) {
		t.Errorf("followed %d times\n%s", n, x.events())
	}
	if x.has("land_off_branch") || x.has("run_branch_unfollowed") || x.has("land_deferred", "not on the run's branch") {
		t.Errorf("the landing waited on the branch:\n%s", x.events())
	}
	if !strings.Contains(x.announced(), decide.BranchFollowed("main", "fix", true)) {
		t.Errorf("announced %q", x.announced())
	}
	if st.BranchNote != decide.BranchFollowedNote("main", "fix", true) {
		t.Errorf("the lead's note: %q", st.BranchNote)
	}
	for range 4 {
		if x.status("P2") != "" {
			break
		}
		x.tick(time.Second)
	}
	if w := x.worker("P2"); w.Base != gitx.RevParse(x.repo, "refs/heads/fix") {
		t.Errorf("P2 started from %q, not fix's tip\n%s", w.Base, x.events())
	}
}

// A detached HEAD is followed nowhere: the landing waits, the human is pushed once, and the lead's model
// is told once, however long it waits, how to let the work land.
func TestADetachedCheckoutTellsTheLeadOnce(t *testing.T) {
	x := landingCrew(t)
	gittest.Git(t, x.repo, "checkout", "-q", "--detach")
	x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	for range 6 {
		x.tick(overlapRetry)
	}
	st := x.state()
	if st.RunBranch != "main" || x.status("P1") == state.WorkerLanded || x.has("run_branch_followed") {
		t.Fatalf("followed a detached HEAD: %q\n%s", st.RunBranch, x.events())
	}
	if n := strings.Count(x.events(), `"kind":"run_branch_unfollowed"`); n != 1 || !x.has("run_branch_unfollowed", `"branch":""`, `"run_branch":"main"`) {
		t.Errorf("told %d times\n%s", n, x.events())
	}
	var told []state.Message
	for _, m := range st.Inbox {
		if m.Kind == state.MessageBranch {
			told = append(told, m)
		}
	}
	if len(told) != 1 || told[0].Text != decide.BranchUnfollowed("P1", "", "main") {
		t.Errorf("the lead's messages: %+v", told)
	}
}

// With the checkout on the run's branch, nothing follows and nobody is told: the landing lands there.
func TestOnTheRunsBranchNothingFollows(t *testing.T) {
	x := landingCrew(t)
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.landUntil("P1")
	st := x.state()
	if st.RunBranch != "main" || !landedOn(t, x.repo, "main", tip) || st.BranchNote != "" || st.BranchTold != "" {
		t.Fatalf("run's branch %q, note %q, told %q", st.RunBranch, st.BranchNote, st.BranchTold)
	}
	if x.has("run_branch_followed") || x.has("run_branch_unfollowed") {
		t.Errorf("events:\n%s", x.events())
	}
}

// A record due while the lead's checkout is on a branch that holds the run's is written there, the run's
// branch following it.
func TestARecordFollowsTheLeadsBranch(t *testing.T) {
	x := recordCrew(t, 0, recordPath)
	gittest.Git(t, x.repo, "checkout", "-q", "-b", "fix")
	x.leadPhase("P0", map[string]string{"zero.txt": "0\n"}, "Zero.", x.now)
	x.tick(recordEvery)
	if st := x.state(); st.RunBranch != "fix" || len(st.Records) != 1 || x.has("record_held") {
		t.Fatalf("run's branch %q, records %+v\n%s", st.RunBranch, st.Records, x.events())
	}
	if gitx.Branch(x.repo) != "fix" || !strings.Contains(read(t, x.repo, recordPath), "## P0") {
		t.Errorf("the record is not on fix:\n%s", read(t, x.repo, recordPath))
	}
}

// Once the lead merges the run's branch into the branch its checkout is on, as it was told to, the run
// follows that branch, and the work waiting lands there.
func TestTheRunFollowsABranchOnceItHoldsTheRuns(t *testing.T) {
	x := landingCrew(t)
	x.offRunsBranch()
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	for range 3 {
		x.tick(overlapRetry)
	}
	if !x.has("run_branch_unfollowed", `"branch":"elsewhere"`) || x.status("P1") == state.WorkerLanded {
		t.Fatalf("events:\n%s", x.events())
	}
	gittest.Git(t, x.repo, "merge", "-q", "--no-edit", "main")
	x.tick(overlapRetry)
	x.landUntil("P1")
	if st := x.state(); st.RunBranch != "elsewhere" || !landedOn(t, x.repo, "elsewhere", tip) || !x.has("run_branch_followed", `"from":"main"`, `"to":"elsewhere"`) {
		t.Fatalf("run's branch %q\n%s", st.RunBranch, x.events())
	}
}

// A plan whose first phase cuts the lead a branch of its own from elsewhere (a fresh branch off the remote's
// main, the run's branch being an older plan's) before any of the plan's work is on the run's branch: the
// run follows it, though it lacks the run's branch's commits, and a finished phase lands there. Once it
// followed, only a branch that holds it is followed.
func TestTheRunFollowsANewBranchBeforeAnyWorkIsOnIt(t *testing.T) {
	x := landingCrew(t)
	x.leadCommits(map[string]string{"old.txt": "the older plan's\n"})
	if _, err := x.lead.Update(func(st *state.State) error { st.BranchFrom = gitx.Head(x.repo); return nil }); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, x.repo, "checkout", "-q", "-b", "fresh", "HEAD~1")
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.landUntil("P1")
	st := x.state()
	if st.RunBranch != "fresh" || st.BranchFrom != "" || !landedOn(t, x.repo, "fresh", tip) {
		t.Fatalf("run's branch %q, from %q\n%s", st.RunBranch, st.BranchFrom, x.events())
	}
	if !x.has("run_branch_followed", `"from":"main"`, `"to":"fresh"`, `"holds":false`) || x.has("land_off_branch") || x.has("run_branch_unfollowed") {
		t.Errorf("events:\n%s", x.events())
	}
	if st.BranchNote != decide.BranchFollowedNote("main", "fresh", false) {
		t.Errorf("the lead's note: %q", st.BranchNote)
	}
	gittest.Git(t, x.repo, "checkout", "-q", "-b", "other", "HEAD~1")
	x.finish("P2", map[string]string{"two.txt": "two\n"}, false)
	for range 3 {
		x.tick(overlapRetry)
	}
	if st := x.state(); st.RunBranch != "fresh" || !x.has("run_branch_unfollowed", `"branch":"other"`) {
		t.Errorf("followed a branch lacking the plan's work: %q\n%s", st.RunBranch, x.events())
	}
}
