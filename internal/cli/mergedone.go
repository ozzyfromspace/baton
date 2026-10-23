package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A merge baton started in a phase agent's worktree and left at its conflicts (as it landed the phase, or
// for baton sync) is the agent's to finish: it resolves the files and adds them. No model runs git merge
// (decide/sync.go). The agent's `baton done` or `baton sync` over it commits it, once nothing is unmerged
// and no conflict marker is left in what is staged (gitx.CommitMerge), and is refused otherwise, saying
// how to finish it, whatever --keep-dirty says: a merge left half done would never land.

// mergeWait bounds a merge, or its commit, that baton runs for a phase agent's command: the project's
// commit hooks run there, as for the agent's own commits.
const mergeWait = 2 * time.Minute

// concludeMerge commits the merge in progress in the worktree of s, a phase agent's sub-run, for command
// ("done", "sync") on phase: it reports whether command goes on, and the exit code when it does not. A
// lead's run, and a worktree with no merge in progress, go on with nothing done.
func concludeMerge(io IO, s *state.Store, command, phase string) (int, bool) {
	if s.Worker() == "" {
		return 0, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), mergeWait)
	defer cancel()
	if !gitx.Merging(ctx, s.Root) {
		return 0, true
	}
	verb, again := "not synced", "sync"
	if command == "done" {
		verb, again = "not done", "done "+phase
	}
	err := gitx.CommitMerge(ctx, s.Root)
	var ue *gitx.UnresolvedError
	switch {
	case errors.As(err, &ue):
		fields := map[string]any{"command": command, "phase": phase, "why": "merge unfinished"}
		if len(ue.Paths) > 0 {
			fields["paths"] = ue.Paths
		}
		if len(ue.Markers) > 0 {
			fields["markers"] = ue.Markers
		}
		s.Event("refused", fields)
		return fail(io, "%s", decide.MergeUnfinished(verb, again, ue.Paths, ue.Markers)), false
	case errors.Is(err, gitx.ErrNoMerge):
		return 0, true // committed meanwhile, by the agent's own git commit
	case err != nil:
		s.Event("refused", map[string]any{"command": command, "phase": phase, "why": "merge not committed", "error": err.Error()})
		return fail(io, "%s", decide.MergeNotCommitted(verb, again, err.Error())), false
	}
	head := gitx.Head(s.Root)
	s.Event("merge_concluded", map[string]any{"phase": phase, "by": command, "commit": head})
	tellLead(s, decide.MergeConcludedLine(phase, head))
	fmt.Fprintln(io.Out, decide.MergeConcluded(head))
	return 0, true
}
