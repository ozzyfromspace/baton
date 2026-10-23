package gitx

import "context"

// Uncommitted lists the tracked paths of the worktree dir with uncommitted changes, staged or not: those
// Merge refuses to start over, since `git merge --abort` cannot always give them back. Untracked files are
// left out, as Merge leaves them: a merge leaves them be, and git refuses one in its way before it changes
// anything. It is sorted, and never takes git's optional locks.
func Uncommitted(ctx context.Context, dir string) ([]string, error) {
	if !Usable(dir) {
		return nil, ErrNoGit
	}
	out, err := run(ctx, dir, nil, nil, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}
