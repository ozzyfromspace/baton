package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A fresh phase agent whose worktree holds the merge baton left at its conflicts is told of it first,
// right after who it is: the message that told of it may have gone to the agent before it.
func TestAFreshAgentIsBriefedOnTheMergeLeftInItsWorktree(t *testing.T) {
	w, lead, tree := newWorker(t)
	// git merge wants an identity before it starts, which the throwaway configuration does not have: macOS
	// makes one up from the machine, Linux and Windows refuse.
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	gittest.Write(t, tree, "a.txt", "a\n")
	gittest.Commit(t, tree, "base")
	gittest.Git(t, tree, "checkout", "-q", "-b", "main")
	gittest.Write(t, tree, "a.txt", "a\nlead\n")
	gittest.Commit(t, tree, "P1: a")
	gittest.Git(t, tree, "checkout", "-q", "baton/sess-1/P2")
	gittest.Write(t, tree, "a.txt", "a\nagent\n")
	gittest.Commit(t, tree, "P2: a")
	if err := gitx.Merge(context.Background(), tree, "main", decide.MergeMessage("main", "baton/sess-1/P2")); err == nil {
		t.Fatal("the merge went through; want a conflict")
	}
	lead.set(func(st *state.State) { st.RunBranch = "main" })

	brief := additionalContext(w.fire("SubagentStart", map[string]any{}))
	want := decide.MergeInProgressBrief("P2", "main", []string{"a.txt"})
	if at := strings.Index(brief, want); at < 0 || at > strings.Index(brief, "Progress:") {
		t.Errorf("the brief does not open with the merge:\n%s\nwant %q", brief, want)
	}
	if m := mergeLeft(lead.store); m != nil {
		t.Errorf("the lead's checkout, with no merge in progress: %+v", m)
	}
}
