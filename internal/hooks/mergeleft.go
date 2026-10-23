package hooks

import (
	"context"
	"time"

	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// mergeLeftWait bounds the git commands that read a worktree's merge in progress for a brief.
const mergeLeftWait = 5 * time.Second

// mergeLeft is the merge in progress in the worktree of the phase agent of s (its sub-run), which baton
// started and left at its conflicts (as it landed the phase, or for baton sync) for the agent to finish:
// a fresh agent's brief opens with it, since the message that told of it may have gone to the agent this
// one replaced. nil for none. It runs git, so it is called with no lock held.
func mergeLeft(s *state.Store) *brief.MergeLeft {
	ctx, cancel := context.WithTimeout(context.Background(), mergeLeftWait)
	defer cancel()
	if !gitx.Merging(ctx, s.Root) {
		return nil
	}
	paths, _ := gitx.Unmerged(ctx, s.Root)
	m := &brief.MergeLeft{Unmerged: paths}
	if up, err := s.ParentRun(); err == nil {
		if st, err := up.Load(); err == nil {
			m.Branch = st.RunBranch
		}
	}
	return m
}
