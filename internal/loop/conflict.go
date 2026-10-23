package loop

import (
	"context"
	"os"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/replan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A landing that conflicts goes back to its phase agent, which merges the run's branch in itself (merge.go
// mergeIn), told whose changes it merges: under each path, the commits of the run's branch that changed it
// since the two branches last met, and the phase whose landing brought each (conflictHistory). A file in a
// second conflict of the run is a look-in for the lead only while phases not started name it, which a
// replan can still give files of their own (lookin.go conflicted, conflictNames), and cues that replan
// (state.CueConflict). Both read git and the plan's text before any lock.

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

// conflictNames is the plan of run s, and for each of paths the phases whose sections in its text name it
// (replan.Naming), read before any lock: none when the plan or its file cannot be read.
func conflictNames(s *state.Store, paths []string) (plan.Plan, map[string][]string) {
	pl, err := s.LoadPlan()
	if err != nil {
		return pl, nil
	}
	doc, err := os.ReadFile(pl.File)
	if err != nil {
		return pl, nil
	}
	ids := make([]string, 0, len(pl.Phases))
	for _, ph := range pl.Phases {
		ids = append(ids, ph.ID)
	}
	return pl, replan.Naming(pl, doc, ids, paths)
}

// unstartedNaming keeps, for each of paths, the phases of named (those whose sections name it) that have
// not started on run st of plan pl (state.Frozen): a path none of them names has no entry, and told lists
// the paths that have one, in order.
func unstartedNaming(st state.State, pl plan.Plan, named map[string][]string, paths []string) (told []string, naming map[string][]string) {
	frozen := state.Frozen(st, pl)
	naming = map[string][]string{}
	for _, p := range paths {
		var ids []string
		for _, id := range named[p] {
			if pl.Index(id) >= 0 && !frozen[id] {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			told, naming[p] = append(told, p), ids
		}
	}
	return told, naming
}
