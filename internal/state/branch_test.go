package state

import (
	"context"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
)

// What git says of a checkout beside the run's branch main: on it, on a branch made from it (with a
// commit of its own or none), on one that lacks a commit of main's, and on a detached HEAD.
func TestSeeBranch(t *testing.T) {
	gittest.Isolate(t)
	repo := t.TempDir()
	gittest.Git(t, repo, "init", "-q", "-b", "main")
	gittest.Write(t, repo, "a.txt", "a\n")
	gittest.Commit(t, repo, "first")
	gittest.Git(t, repo, "branch", "made")
	gittest.Git(t, repo, "branch", "behind")
	gittest.Git(t, repo, "checkout", "-q", "-b", "ahead")
	gittest.Write(t, repo, "b.txt", "b\n")
	gittest.Commit(t, repo, "ahead's")
	gittest.Git(t, repo, "checkout", "-q", "main")
	gittest.Write(t, repo, "c.txt", "c\n")
	gittest.Commit(t, repo, "main's") // made and behind lack it now; ahead too

	ctx := context.Background()
	for _, c := range []struct {
		name, checkout string
		want           BranchSeen
		followable     bool
	}{
		{"on the run's branch", "main", BranchSeen{Run: "main", On: "main"}, false},
		{"on a branch that lacks its commit", "behind", BranchSeen{Run: "main", On: "behind"}, false},
		{"on a detached HEAD", "--detach", BranchSeen{Run: "main"}, false},
	} {
		gittest.Git(t, repo, "checkout", "-q", c.checkout)
		seen, ok := SeeBranch(ctx, repo, "main")
		if !ok || seen != c.want || seen.Followable() != c.followable {
			t.Errorf("%s: %+v, %v (followable %v)", c.name, seen, ok, seen.Followable())
		}
	}
	gittest.Git(t, repo, "checkout", "-q", "made")
	gittest.Git(t, repo, "merge", "-q", "--ff-only", "main")
	if seen, ok := SeeBranch(ctx, repo, "main"); !ok || seen != (BranchSeen{Run: "main", On: "made", Holds: true}) || !seen.Followable() {
		t.Errorf("on a branch made from the run's: %+v, %v", seen, ok)
	}
	if _, ok := SeeBranch(ctx, repo, "gone"); ok {
		t.Error("a run's branch that is not there was read")
	}
	if _, ok := SeeBranch(ctx, t.TempDir(), "main"); ok {
		t.Error("no repository was read")
	}
}

// The run's branch follows only a followable checkout, and only from the branch it was read beside; the
// lead's model is told once a branch the run cannot follow, and hears of a branch followed once, never
// while a compaction is owed.
func TestFollowAndTellBranch(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	fix := BranchSeen{Run: "main", On: "fix", Holds: true}
	for _, c := range []struct {
		name     string
		run      string
		seen     BranchSeen
		followed bool
	}{
		{"a branch that holds the run's", "main", fix, true},
		{"the run's branch moved meanwhile", "other", fix, false},
		{"a branch that lacks the run's commits", "main", BranchSeen{Run: "main", On: "fix"}, false},
		{"a detached HEAD", "main", BranchSeen{Run: "main", Holds: true}, false},
		{"the run's branch itself", "main", BranchSeen{Run: "main", On: "main", Holds: true}, false},
		{"a worker's branch", "main", BranchSeen{Run: "main", On: "baton/sess-1/P1", Holds: true}, false},
	} {
		st := State{RunBranch: c.run, BranchTold: "main old"}
		want := c.run
		if c.followed {
			want = c.seen.On
		}
		got := FollowBranch(&st, c.seen, "note")
		if got != c.followed || st.RunBranch != want || c.followed != (st.BranchNote == "note") || c.followed == (st.BranchTold != "") {
			t.Errorf("%s: followed %v, state %+v", c.name, got, st)
		}
	}

	st := State{RunBranch: "main"}
	behind := BranchSeen{Run: "main", On: "behind"}
	if !TellBranch(&st, behind, "merge main", now) || TellBranch(&st, behind, "merge main", now) {
		t.Error("not told once of the branch behind")
	}
	if !TellBranch(&st, BranchSeen{Run: "main"}, "detached", now) || TellBranch(&st, fix, "x", now) || TellBranch(&st, BranchSeen{Run: "main", On: "main"}, "x", now) {
		t.Error("told of a detached HEAD other than once, or of a branch it can follow, or of the run's own")
	}
	if len(st.Inbox) != 1 || st.Inbox[0].Kind != MessageBranch || st.Inbox[0].Text != "detached" {
		t.Errorf("inbox %+v: one message about the branch, the latest", st.Inbox)
	}

	st = State{BranchNote: "followed", BoundaryOwed: true}
	if TakeBranchNote(&st) != "" || st.BranchNote == "" {
		t.Error("the note was told while a compaction was owed")
	}
	st.BoundaryOwed = false
	if TakeBranchNote(&st) != "followed" || TakeBranchNote(&st) != "" {
		t.Error("the note was not told once")
	}
}
