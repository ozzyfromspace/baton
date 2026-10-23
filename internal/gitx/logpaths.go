package gitx

import "context"

// A landing that conflicts goes back to the phase's agent, which merges the run's branch in itself
// (loop/merge.go). It is told, under each path that conflicted, the commits of the run's branch that
// changed it since the two branches last met (MergeBase), so it can read what the other side meant
// before it resolves the conflict.

// literalPaths has git read each path as written: a file named with a glob character or a pathspec's
// magic prefix names that file alone.
var literalPaths = []string{"GIT_LITERAL_PATHSPECS=1"}

// LogPaths lists, for each of paths (relative to the repository's root, slash-separated), the commits
// reachable from to and not from from (all of to's when from is "") that changed it, newest first: `git
// log from..to -- <path>`, with git's own history simplification, so a merge that kept one side's file
// as it was is passed over. A path no such commit changed has no entry.
func LogPaths(ctx context.Context, root, from, to string, paths []string) (map[string][]Commit, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out := make(map[string][]Commit, len(paths))
	for _, p := range paths {
		b, err := run(ctx, root, literalPaths, nil, "log", logFormat, rng, "--", p)
		if err != nil {
			return nil, err
		}
		if cs := parseLog(string(b)); len(cs) > 0 {
			out[p] = cs
		}
	}
	return out, nil
}
