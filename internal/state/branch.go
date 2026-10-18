package state

import (
	"context"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
)

// A lead may move its checkout to another branch once the plan is attached: a plan that has it make a
// session branch at approval does so the moment baton attaches. The run's branch (State.RunBranch) then
// follows the checkout's (loop.FollowBranch): workers branch off it, their work lands on it, and the record
// file is written there, so nothing waits for the checkout to come back. It follows only a branch that
// holds the run's branch's tip, so no landed or committed work is left behind; any other (one that lacks
// its commits, or a detached HEAD) holds landings as before, and the lead's model is told once a branch how
// to let them land. Nothing of baton's moves the checkout.

// BranchSeen is the lead's checkout beside the run's branch Run, as git said before the run's lock
// (SeeBranch): the branch it is on (On, "" when HEAD is detached), and whether On holds Run's tip.
type BranchSeen struct {
	Run   string
	On    string
	Holds bool
}

// Off reports a checkout that is not on the run's branch.
func (b BranchSeen) Off() bool { return b.Run != "" && b.On != b.Run }

// Followable reports a checkout on another branch than the run's that holds the run's branch's tip, and
// is not a worker's own (baton/…).
func (b BranchSeen) Followable() bool {
	return b.Off() && b.On != "" && b.Holds && !strings.HasPrefix(b.On, "baton/")
}

// SeeBranch reads checkout's branch beside run, the run's branch. ok is false when git could not tell:
// checkout is no repository, or a branch is not there.
func SeeBranch(ctx context.Context, checkout, run string) (seen BranchSeen, ok bool) {
	if run == "" || !gitx.Usable(checkout) {
		return BranchSeen{}, false
	}
	seen = BranchSeen{Run: run, On: gitx.Branch(checkout)}
	if !seen.Off() {
		return seen, true
	}
	if seen.On == "" {
		return seen, gitx.Head(checkout) != "" // detached
	}
	tip, on := gitx.RevParse(checkout, "refs/heads/"+run), gitx.RevParse(checkout, "refs/heads/"+seen.On)
	if tip == "" || on == "" {
		return seen, false
	}
	holds, err := gitx.IsAncestor(ctx, checkout, tip, on)
	if err != nil {
		return seen, false
	}
	seen.Holds = holds
	return seen, true
}

// FollowBranch makes seen.On the run's branch, when it is followable and the run's branch is still
// seen.Run, and leaves the lead's model note at its next hook (TakeBranchNote), never waking it for it. It
// reports whether it followed.
func FollowBranch(st *State, seen BranchSeen, note string) bool {
	if !seen.Followable() || st.RunBranch != seen.Run {
		return false
	}
	st.RunBranch, st.BranchNote, st.BranchTold = seen.On, note, ""
	return true
}

// TellBranch leaves the lead's model text, once for the checkout's branch in seen (BranchTold): the
// checkout is off the run's branch on one baton cannot follow, and a landing waits for it. It reports
// whether it told; the message wakes an idle lead, since only its model acts on it.
func TellBranch(st *State, seen BranchSeen, text string, now time.Time) bool {
	key := seen.Run + " " + seen.On
	if !seen.Off() || seen.Followable() || st.RunBranch != seen.Run || st.BranchTold == key {
		return false
	}
	st.BranchTold = key
	Tell(st, Message{Kind: MessageBranch, Text: text, About: "branch"}, now)
	return true
}

// TakeBranchNote returns the note of the run's branch following the checkout's that the lead's model has
// not been told of ("" for none), and marks it told. None is told while a compaction is owed or underway:
// the model hears of it after.
func TakeBranchNote(st *State) string {
	if st.Compacting() {
		return ""
	}
	note := st.BranchNote
	st.BranchNote = ""
	return note
}
