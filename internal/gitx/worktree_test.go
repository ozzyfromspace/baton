package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func worktrees(t *testing.T, repo string) string {
	t.Helper()
	return git(t, repo, "worktree", "list", "--porcelain")
}

func TestWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	first := git(t, repo, "rev-parse", "HEAD")
	write(t, repo, "a.txt", "a2\n")
	commit(t, repo, "second")
	second := git(t, repo, "rev-parse", "HEAD")
	dir := filepath.Join(t.TempDir(), "lane")

	if err := WorktreeAdd(ctx, repo, dir, first); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "a.txt"); got != "a\n" {
		t.Errorf("a.txt in the new worktree = %q, want the first commit's", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "lane")); err == nil {
		t.Error("the worktree landed inside the repository")
	}

	// A gate leaves a cache (ignored), an artifact (untracked) and an edit behind.
	write(t, dir, "cache.log", "cached\n")
	write(t, dir, "artifact.txt", "out\n")
	write(t, dir, "e.txt", "edited\n")
	if err := CheckoutDetach(ctx, dir, second); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "a2\n", "e.txt": "e\n", "cache.log": "cached\n"} {
		if got := read(t, dir, name); got != want {
			t.Errorf("after checkout, %s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "artifact.txt")); err == nil {
		t.Error("the untracked artifact survived the checkout")
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != second {
		t.Errorf("HEAD = %s, want %s", got, second)
	}
	if got := git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("the worktree is on branch %q, want a detached HEAD", got)
	}

	if err := WorktreeRemove(ctx, repo, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the worktree is still there: %v", err)
	}
	if strings.Contains(worktrees(t, repo), "lane") {
		t.Errorf("git still lists the removed worktree:\n%s", worktrees(t, repo))
	}
}

func TestWorktreeWhoseDirectoryIsGone(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	dir := filepath.Join(t.TempDir(), "lane")
	if err := WorktreeAdd(ctx, repo, dir, head); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	if err := WorktreeAdd(ctx, repo, dir, head); err != nil {
		t.Fatalf("adding over a worktree whose directory is gone: %v", err)
	}
	os.RemoveAll(dir)
	if err := PruneUnder(ctx, repo, filepath.Dir(dir)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(worktrees(t, repo), "lane") {
		t.Errorf("prune kept a worktree whose directory is gone:\n%s", worktrees(t, repo))
	}
}

// PruneUnder forgets a worktree whose directory is gone only when it was under one of the directories it
// is given, and never a locked one, nor one whose directory is there.
func TestPruneUnder(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	baton := t.TempDir()
	link := filepath.Join(t.TempDir(), "link") // baton's directory, named through a symbolic link
	if err := os.Symlink(baton, link); err != nil {
		t.Fatal(err)
	}
	human := t.TempDir()
	for _, c := range []struct {
		name   string
		dir    string
		gone   bool
		lock   bool
		under  string
		forget bool
	}{
		{"gone, under baton's", filepath.Join(baton, "key", "run", "w-gone"), true, false, baton, true},
		{"gone, named through a link", filepath.Join(baton, "key", "run", "w-link"), true, false, link, true},
		{"gone, but locked", filepath.Join(baton, "key", "run", "w-locked"), true, true, baton, false},
		{"there", filepath.Join(baton, "key", "run", "w-there"), false, false, baton, false},
		{"gone, the human's elsewhere", filepath.Join(human, "drive", "w-human"), true, false, baton, false},
		{"gone, baton's directory itself", filepath.Join(baton, "w-self"), true, false, filepath.Join(baton, "w-self"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := WorktreeAdd(ctx, repo, c.dir, head); err != nil {
				t.Fatal(err)
			}
			defer WorktreeRemove(ctx, repo, c.dir)
			if c.lock {
				git(t, repo, "worktree", "lock", c.dir)
				defer git(t, repo, "worktree", "unlock", c.dir)
			}
			if c.gone {
				os.RemoveAll(c.dir)
			}
			if err := PruneUnder(ctx, repo, c.under); err != nil {
				t.Fatal(err)
			}
			listed := strings.Contains(worktrees(t, repo), "/"+filepath.Base(c.dir)+"\n") // git prints / on Windows too
			if listed == c.forget {
				t.Errorf("forgotten %v, want %v:\n%s", !listed, c.forget, worktrees(t, repo))
			}
			if c.gone && !c.lock {
				git(t, repo, "worktree", "prune")
			}
		})
	}
}

// A worktree git records by a path relative to its own directory (worktree.useRelativePaths) is found
// the same.
func TestPruneUnderRelativePaths(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	baton := t.TempDir()
	dir := filepath.Join(baton, "key", "run", "P1")
	if err := WorktreeAdd(ctx, repo, dir, head); err != nil {
		t.Fatal(err)
	}
	gitdir := filepath.Join(repo, ".git", "worktrees", "P1", "gitdir")
	rel, err := filepath.Rel(filepath.Dir(gitdir), filepath.Join(resolve(dir), ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitdir, []byte(rel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	there := func() bool { _, err := os.Stat(filepath.Dir(gitdir)); return err == nil }
	if err := PruneUnder(ctx, repo, t.TempDir()); err != nil || !there() {
		t.Fatalf("forgotten under another directory (%v)", err)
	}
	if err := PruneUnder(ctx, repo, baton); err != nil || there() {
		t.Errorf("not forgotten under its own (%v)", err)
	}
}

func TestWorktreeWithoutGit(t *testing.T) {
	ctx := context.Background()
	plain := t.TempDir()
	for name, err := range map[string]error{
		"WorktreeAdd":    WorktreeAdd(ctx, plain, filepath.Join(plain, "x"), "HEAD"),
		"CheckoutDetach": CheckoutDetach(ctx, plain, "HEAD"),
		"WorktreeRemove": WorktreeRemove(ctx, plain, filepath.Join(plain, "x")),
		"PruneUnder":     PruneUnder(ctx, plain, plain),
	} {
		if !errors.Is(err, ErrNoGit) {
			t.Errorf("%s outside a repository = %v, want ErrNoGit", name, err)
		}
	}
}

// A worker's worktree is on a branch of its own, made from the run's branch; one taken up again after
// another worker left commits on its branch goes on from them. Its branch is removed once nothing on it
// is left to land.
func TestWorkerBranchLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	git(t, repo, "checkout", "-q", "-b", "main")
	base := RevParse(repo, "refs/heads/main")
	if base == "" || base != git(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("RevParse(main) = %q", base)
	}
	if got := RevParse(repo, "refs/heads/nope"); got != "" {
		t.Errorf("RevParse of a missing branch = %q", got)
	}
	dir := filepath.Join(t.TempDir(), "P1")
	if fresh, err := AddBranch(ctx, repo, dir, "baton/r/P1", base); err != nil || !fresh {
		t.Fatalf("AddBranch = %v, %v; want a new branch", fresh, err)
	}
	if got := Branch(dir); got != "baton/r/P1" {
		t.Errorf("the worktree is on %q", got)
	}
	if n, err := Ahead(ctx, repo, "main", "baton/r/P1"); err != nil || n != 0 {
		t.Errorf("Ahead of a new branch = %d, %v", n, err)
	}
	write(t, dir, "w.txt", "worker\n")
	commit(t, dir, "the worker's commit")
	tip := git(t, dir, "rev-parse", "HEAD")
	if n, err := Ahead(ctx, repo, "main", "baton/r/P1"); err != nil || n != 1 {
		t.Errorf("Ahead after a commit = %d, %v", n, err)
	}
	git(t, repo, "branch", "baton/r/kept/P0-20261011-1200", "main")
	git(t, repo, "branch", "batonx", "main")
	if got := Branches(repo, "baton/"); !slices.Equal(got, []string{"baton/r/P1", "baton/r/kept/P0-20261011-1200"}) {
		t.Errorf("Branches = %q", got)
	}

	// The worker went away, its worktree with it: the next one goes on from its commits.
	os.RemoveAll(dir)
	if fresh, err := AddBranch(ctx, repo, dir, "baton/r/P1", base); err != nil || fresh {
		t.Fatalf("AddBranch again = %v, %v; want the branch as it stands", fresh, err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != tip {
		t.Errorf("taken up again at %s, want %s", got, tip)
	}
	if err := WorktreeRemove(ctx, repo, dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveBranch(ctx, repo, "baton/r/P1"); err != nil {
		t.Fatal(err)
	}
	if RevParse(repo, "refs/heads/baton/r/P1") != "" {
		t.Error("the branch is still there")
	}
	if err := RemoveBranch(ctx, repo, "baton/r/P1"); err != nil {
		t.Errorf("removing a branch that is gone: %v", err)
	}
	plain := t.TempDir()
	if _, err := AddBranch(ctx, plain, dir, "b", "HEAD"); !errors.Is(err, ErrNoGit) {
		t.Errorf("AddBranch outside a repository = %v", err)
	}
	if _, err := Ahead(ctx, plain, "a", "b"); !errors.Is(err, ErrNoGit) {
		t.Errorf("Ahead outside a repository = %v", err)
	}
	if err := RemoveBranch(ctx, plain, "b"); !errors.Is(err, ErrNoGit) {
		t.Errorf("RemoveBranch outside a repository = %v", err)
	}
	if got := Branches(plain, "baton/"); got != nil {
		t.Errorf("Branches outside a repository = %q", got)
	}
}
