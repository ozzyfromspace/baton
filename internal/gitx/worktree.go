package gitx

import "context"

// Lanes (package lane) are worktrees of the project's repository, kept outside its working tree, where
// baton checks a commit out and runs a gate on it while the session works on.

// WorktreeAdd makes dir a new worktree of the repository at root, with commit checked out and no branch.
// It takes the place of a worktree git still has on record at dir whose directory is gone.
func WorktreeAdd(ctx context.Context, root, dir, commit string) error {
	if !Usable(root) {
		return ErrNoGit
	}
	_, err := run(ctx, root, nil, nil, "worktree", "add", "-q", "--force", "--detach", dir, commit)
	return err
}

// CheckoutDetach moves the worktree at dir to commit, with no branch, discarding every change and
// untracked file in it. Ignored files stay, so caches such as node_modules survive from one gate to the
// next.
func CheckoutDetach(ctx context.Context, dir, commit string) error {
	if !Usable(dir) {
		return ErrNoGit
	}
	if _, err := run(ctx, dir, nil, nil, "checkout", "-q", "--force", "--detach", commit); err != nil {
		return err
	}
	_, err := run(ctx, dir, nil, nil, "clean", "-q", "-d", "--force")
	return err
}

// WorktreeRemove removes the worktree at dir from the repository at root, whatever it holds.
func WorktreeRemove(ctx context.Context, root, dir string) error {
	if !Usable(root) {
		return ErrNoGit
	}
	_, err := run(ctx, root, nil, nil, "worktree", "remove", "--force", dir)
	return err
}

// WorktreePrune makes the repository at root forget the worktrees whose directories are gone.
func WorktreePrune(ctx context.Context, root string) error {
	if !Usable(root) {
		return ErrNoGit
	}
	_, err := run(ctx, root, nil, nil, "worktree", "prune")
	return err
}
