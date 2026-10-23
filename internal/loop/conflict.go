package loop

import (
	"context"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A landing that conflicts goes back to its phase agent, which merges the run's branch in itself (merge.go
// mergeIn), told whose changes it merges: under each path, the commits of the run's branch that changed it
// since the two branches last met, and the phase whose landing brought each (conflictHistory), read from
// git before any lock.

// conflictHistory is, under each of paths a merge of the run's branch (at onto) into a phase's branch (at
// tip) conflicted in, the commits of the run's branch that changed it since the two last met (their merge
// base), newest first, each with the phase whose landing of run st brought it (State.Landed): what the
// phase's agent is told as it merges (decide.LandConflict). Best effort: nil when git cannot say.
func (c *Coordinator) conflictHistory(ctx context.Context, s *state.Store, st state.State, onto, tip string, paths []string) map[string][]decide.ConflictCommit {
	g := c.Fleet.gitx()
	base, err := g.MergeBase(ctx, s.Checkout, onto, tip)
	if err != nil {
		c.Lead.logf("loop: no history for the conflict in %v: %v", paths, err)
		return nil
	}
	logs, err := g.LogPaths(ctx, s.Checkout, base, onto, paths)
	if err != nil {
		c.Lead.logf("loop: no history for the conflict in %v: %v", paths, err)
		return nil
	}
	by := map[string]string{}
	for _, l := range st.Landed {
		for _, x := range l.Commits {
			by[x] = l.Phase
		}
	}
	out := make(map[string][]decide.ConflictCommit, len(logs))
	for p, cs := range logs {
		for _, x := range cs {
			out[p] = append(out[p], decide.ConflictCommit{Hash: x.Short(), Subject: x.Subject, Phase: by[x.Hash]})
		}
	}
	return out
}
