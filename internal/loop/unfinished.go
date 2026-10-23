package loop

import (
	"context"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A landing whose merge stopped on conflicts leaves that merge in progress in the phase agent's worktree
// (gitx.MergeError.Kept), and the agent resolves the files, adds them and commits the merge: it never
// runs git merge itself. In a real run, auto mode refused an agent's own git merge twice, and the phase,
// with four others waiting on it, sat blocked for hours. A phase reported done again before that merge
// is committed goes back to its agent (unfinished): baton neither aborts a merge a model may have
// resolved files of, nor lands one half done.

// whyUnfinished is land_returned's why for a phase done again with baton's merge still in progress.
const whyUnfinished = "its merge is not finished"

func (realGit) Merging(ctx context.Context, dir string) bool { return gitx.Merging(ctx, dir) }
func (realGit) Unmerged(ctx context.Context, dir string) ([]string, error) {
	return gitx.Unmerged(ctx, dir)
}

// unfinished hands worker e's phase back to it, told which paths still conflict: the merge baton left in
// its worktree is not committed. It is no new conflict: that was counted as the merge stopped. It
// reports whether it handed the phase back, which takes the step; else the landing tries again later.
func (c *Coordinator) unfinished(ctx context.Context, now time.Time, s *state.Store, w *worker, e state.Worker) bool {
	unmerged, err := c.Fleet.gitx().Unmerged(ctx, e.Worktree)
	if err != nil {
		c.deferLanding(now, s, e, "baton could not read its merge in progress: "+err.Error(), nil, failedRetry)
		return false
	}
	text := decide.LandUnfinished(e.Phase, unmerged)
	if _, err := w.store.Update(func(sub *state.State) error {
		return state.Reopen(sub, e.Phase, state.Message{Kind: state.MessageLand, Text: text}, now)
	}); err != nil {
		c.deferLanding(now, s, e, "baton could not hand its phase back: "+err.Error(), nil, failedRetry)
		return false
	}
	more := map[string]any{}
	if len(unmerged) > 0 {
		more["unmerged"] = unmerged
	}
	c.handedBack(now, s, e, whyUnfinished, nil, more)
	return true
}
