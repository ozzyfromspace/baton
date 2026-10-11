package gitx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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

// PruneUnder makes the repository at root forget the worktrees whose directories are gone, among those
// that were under one of dirs (symbolic links resolved). Unlike git's own prune, it never forgets a
// worktree anywhere else, such as the human's on a drive that is not mounted, nor a locked one.
func PruneUnder(ctx context.Context, root string, dirs ...string) error {
	if !Usable(root) {
		return ErrNoGit
	}
	out, err := run(ctx, root, nil, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	admin := strings.TrimSpace(string(out))
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(root, admin)
	}
	admin = filepath.Join(admin, "worktrees")
	var under []string
	for _, d := range dirs {
		if d != "" {
			under = append(under, resolve(d))
		}
	}
	entries, err := os.ReadDir(admin)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var errs []error
	for _, e := range entries {
		entry := filepath.Join(admin, e.Name())
		if _, err := os.Lstat(filepath.Join(entry, "locked")); err == nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(entry, "gitdir"))
		gitdir := strings.TrimSpace(string(b))
		if err != nil || gitdir == "" {
			continue
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(entry, gitdir)
		}
		tree := filepath.Dir(gitdir) // the worktree's directory, which holds the .git file gitdir names
		if _, err := os.Lstat(tree); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if tree = resolve(tree); !within(tree, under) {
			continue
		}
		if err := os.RemoveAll(entry); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resolve returns path absolute, with the symbolic links of the longest part of it that exists resolved:
// a directory that is gone is named as it would be if it were there.
func resolve(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	var rest []string
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		if filepath.Dir(dir) == dir {
			return filepath.Clean(path)
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

// within reports whether path is inside one of dirs (not one of them itself).
func within(path string, dirs []string) bool {
	for _, d := range dirs {
		if rel, err := filepath.Rel(d, path); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Within reports whether path is inside dir (not dir itself), both resolved as PruneUnder does.
func Within(path, dir string) bool { return within(resolve(path), []string{resolve(dir)}) }

// A worker (package loop) works on its phase in a worktree of its own, on a branch of its own, outside the
// project's checkout, and its work lands on the run's branch once it is done.

// AddBranch makes dir a new worktree of the repository at root, on branch: a new branch from commit, or
// the branch as it stands if it exists already (a worker taking up a phase another worker left commits
// on). It reports whether the branch is new. A worktree git still has on record beside dir (in its
// parent directory) whose directory is gone, such as an earlier one at dir, is forgotten first.
func AddBranch(ctx context.Context, root, dir, branch, commit string) (bool, error) {
	if !Usable(root) {
		return false, ErrNoGit
	}
	if err := PruneUnder(ctx, root, filepath.Dir(dir)); err != nil {
		return false, err
	}
	if revParse(ctx, root, "refs/heads/"+branch) != "" {
		_, err := run(ctx, root, nil, nil, "worktree", "add", "-q", dir, branch)
		return false, err
	}
	_, err := run(ctx, root, nil, nil, "worktree", "add", "-q", "-b", branch, dir, commit)
	return err == nil, err
}

// RemoveBranch deletes branch from the repository at root, merged or not. A branch that is not there is
// not an error.
func RemoveBranch(ctx context.Context, root, branch string) error {
	if !Usable(root) {
		return ErrNoGit
	}
	if revParse(ctx, root, "refs/heads/"+branch) == "" {
		return nil
	}
	_, err := run(ctx, root, nil, nil, "branch", "-q", "-D", branch)
	return err
}

// RenameBranch renames branch from to to in the repository at root, and any worktree that has it checked
// out with it.
func RenameBranch(ctx context.Context, root, from, to string) error {
	if !Usable(root) {
		return ErrNoGit
	}
	_, err := run(ctx, root, nil, nil, "branch", "-m", from, to)
	return err
}

// RevParse returns the commit rev names in the repository at root, or "" (no git, or no such commit).
func RevParse(root, rev string) string {
	if !Usable(root) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return revParse(ctx, root, rev)
}

func revParse(ctx context.Context, root, rev string) string {
	out, err := run(ctx, root, nil, nil, "rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Ahead counts the commits on branch that into does not have: a worker's commits that never landed on
// the run's branch.
func Ahead(ctx context.Context, root, into, branch string) (int, error) {
	if !Usable(root) {
		return 0, ErrNoGit
	}
	out, err := run(ctx, root, nil, nil, "rev-list", "--count", into+".."+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}
