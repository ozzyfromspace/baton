package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if err := WorktreePrune(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(worktrees(t, repo), "lane") {
		t.Errorf("prune kept a worktree whose directory is gone:\n%s", worktrees(t, repo))
	}
}

func TestWorktreeWithoutGit(t *testing.T) {
	ctx := context.Background()
	plain := t.TempDir()
	for name, err := range map[string]error{
		"WorktreeAdd":    WorktreeAdd(ctx, plain, filepath.Join(plain, "x"), "HEAD"),
		"CheckoutDetach": CheckoutDetach(ctx, plain, "HEAD"),
		"WorktreeRemove": WorktreeRemove(ctx, plain, filepath.Join(plain, "x")),
		"WorktreePrune":  WorktreePrune(ctx, plain),
	} {
		if !errors.Is(err, ErrNoGit) {
			t.Errorf("%s outside a repository = %v, want ErrNoGit", name, err)
		}
	}
}
