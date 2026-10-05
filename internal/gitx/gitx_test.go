package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
)

var (
	isolate = gittest.Isolate
	git     = gittest.Git
	write   = gittest.Write
	commit  = gittest.Commit
	noGit   = gittest.NoGit
)

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// newRepo is a repository with one commit (a.txt, e.txt and a .gitignore for *.log), and no identity.
func newRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "a.txt", "a\n")
	write(t, dir, "e.txt", "e\n")
	write(t, dir, ".gitignore", "*.log\n")
	commit(t, dir, "first")
	return dir
}

func TestUsable(t *testing.T) {
	repo := newRepo(t)
	plain := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", worktree)
	sub := filepath.Join(repo, "sub")
	os.MkdirAll(sub, 0o755)
	for dir, want := range map[string]bool{repo: true, worktree: true, plain: false, sub: false} {
		if got := Usable(dir); got != want {
			t.Errorf("Usable(%s) = %v, want %v", dir, got, want)
		}
	}
	noGit(t)
	if Usable(repo) {
		t.Error("usable with no git on the PATH")
	}
}

func TestDirty(t *testing.T) {
	dir := newRepo(t)
	if got, err := Dirty(dir); err != nil || len(got) != 0 {
		t.Fatalf("clean tree: %q %v", got, err)
	}
	write(t, dir, "a.txt", "changed\n")          // modified, unstaged
	write(t, dir, "b.txt", "new, staged\n")      // added
	write(t, dir, "deep/c d.txt", "untracked\n") // untracked, in a new directory, with a space
	write(t, dir, "skip.log", "ignored\n")
	git(t, dir, "add", "b.txt")
	git(t, dir, "mv", "e.txt", "moved.txt") // a rename: both sides count
	got, err := Dirty(dir)
	want := []string{"a.txt", "b.txt", "deep/c d.txt", "e.txt", "moved.txt"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Dirty = %q, %v; want %q", got, err, want)
	}

	if _, err := Dirty(t.TempDir()); !errors.Is(err, ErrNoGit) {
		t.Errorf("plain folder: %v", err)
	}
	noGit(t)
	if _, err := Dirty(dir); !errors.Is(err, ErrNoGit) {
		t.Errorf("no git: %v", err)
	}
}

// The gate compares a phase's end with its start: new paths and changed content are fresh, the rest
// was already there.
func TestSinceTellsFreshWorkFromWhatWasThere(t *testing.T) {
	dir := t.TempDir() // Fingerprint reads files; it needs no git
	write(t, dir, "old.txt", "there before\n")
	write(t, dir, "edited.txt", "v1\n")
	write(t, dir, "deleted.txt", "x\n")
	start := Fingerprint(dir, []string{"old.txt", "edited.txt", "deleted.txt", "gone-before.txt"})
	write(t, dir, "edited.txt", "v2\n")
	write(t, dir, "new.txt", "fresh\n")
	os.Remove(filepath.Join(dir, "deleted.txt"))
	now := Fingerprint(dir, []string{"old.txt", "edited.txt", "deleted.txt", "gone-before.txt", "new.txt"})
	fresh, kept := Since(start, now)
	if want := []string{"deleted.txt", "edited.txt", "new.txt"}; !reflect.DeepEqual(fresh, want) {
		t.Errorf("fresh %q, want %q", fresh, want)
	}
	if want := []string{"gone-before.txt", "old.txt"}; !reflect.DeepEqual(kept, want) {
		t.Errorf("kept %q, want %q", kept, want)
	}
	// Rewriting a file with the same content is no change.
	write(t, dir, "old.txt", "there before\n")
	if fresh, _ := Since(start, Fingerprint(dir, []string{"old.txt"})); len(fresh) != 0 {
		t.Errorf("rewritten, same content: %q", fresh)
	}
}

// signingTrap makes every signature fail and leaves a mark if gpg is ever run.
func signingTrap(t *testing.T) (marker string) {
	t.Helper()
	home := os.Getenv("HOME")
	marker = filepath.Join(home, "gpg-was-called")
	gpg := filepath.Join(home, "fake-gpg")
	os.WriteFile(gpg, []byte("#!/bin/sh\ntouch '"+marker+"'\necho 'gpg: signing failed: Timeout' >&2\nexit 2\n"), 0o755)
	cfg := "[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = " + gpg + "\n[user]\n\tuseConfigOnly = true\n"
	os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL"), []byte(cfg), 0o644)
	return marker
}

func TestSnapshotSavesTheWorkAndTouchesNothing(t *testing.T) {
	dir := newRepo(t)
	marker := signingTrap(t) // signing is on and broken, and there is no identity
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "b.txt", "staged\n")
	git(t, dir, "add", "b.txt")
	write(t, dir, "b.txt", "staged, then edited\n")
	write(t, dir, "new/c.txt", "untracked\n")
	write(t, dir, "skip.log", "ignored\n")
	os.Remove(filepath.Join(dir, "e.txt"))

	indexBefore := read(t, dir, ".git/index")
	headBefore, statusBefore := git(t, dir, "rev-parse", "HEAD"), git(t, dir, "status", "--porcelain")
	at := time.Date(2026, 10, 5, 15, 28, 35, 0, time.UTC)
	s, err := Snapshot(dir, at, "P4", "blocked: gpg timed out")
	if err != nil {
		t.Fatal(err)
	}
	if want := "refs/baton/snapshots/P4-" + "1791214115"; s.Ref != want || s.Files != 4 || len(s.Skipped) != 0 || s.Unchanged {
		t.Fatalf("saved %+v, want ref %s and 4 files", s, want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("gpg was called")
	}
	if read(t, dir, ".git/index") != indexBefore || git(t, dir, "rev-parse", "HEAD") != headBefore || git(t, dir, "status", "--porcelain") != statusBefore {
		t.Error("the snapshot changed the index, HEAD or the working tree")
	}
	if got := git(t, dir, "ls-tree", "-r", "--name-only", s.Ref); got != ".gitignore\na.txt\nb.txt\nnew/c.txt" {
		t.Errorf("snapshot holds:\n%s", got)
	}
	for name, want := range map[string]string{"a.txt": "changed\n", "b.txt": "staged, then edited\n", "new/c.txt": "untracked\n"} {
		if got := git(t, dir, "show", s.Ref+":"+name); got != strings.TrimSpace(want) {
			t.Errorf("%s in the snapshot: %q", name, got)
		}
	}
	if got := git(t, dir, "log", "-1", "--format=%an <%ae> %P|%s|%G?", s.Ref); got != "baton <baton@localhost> "+headBefore+"|baton snapshot: P4 · 4 files|N" {
		t.Errorf("commit: %s", got)
	}
	if got := git(t, dir, "for-each-ref", "--format=%(refname)", "refs/heads"); got != "refs/heads/"+git(t, dir, "branch", "--show-current") {
		t.Errorf("branches: %s", got)
	}
	if list := Snapshots(dir); len(list) != 1 || list[0].Ref != s.Ref || !list[0].At.Equal(at) || list[0].Subject != "baton snapshot: P4 · 4 files" {
		t.Errorf("Snapshots: %+v", list)
	}

	// The same work again: the newest snapshot already holds it.
	again, err := Snapshot(dir, at.Add(time.Minute), "P4", "paused")
	if err != nil || again.Ref != s.Ref || !again.Unchanged || again.Files != 4 || len(Snapshots(dir)) != 1 {
		t.Errorf("again: %+v %v", again, err)
	}
}

// The command baton prints must actually bring the work back after it is lost.
func TestRestoreCommandBringsTheWorkBack(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "b.txt", "staged\n")
	git(t, dir, "add", "b.txt")
	write(t, dir, "new/c.txt", "untracked\n")
	write(t, dir, "skip.log", "ignored\n")
	os.Remove(filepath.Join(dir, "e.txt"))
	s, err := Snapshot(dir, time.Now(), "P1", "")
	if err != nil || s.Ref == "" {
		t.Fatal(s, err)
	}
	git(t, dir, "reset", "-q", "--hard")
	git(t, dir, "clean", "-q", "-fd")
	write(t, dir, "later.txt", "untracked, made after the snapshot\n")

	cmd := exec.Command("sh", "-c", RestoreCommand(s.Ref))
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", RestoreCommand(s.Ref), err, out)
	}
	for name, want := range map[string]string{
		"a.txt": "changed\n", "b.txt": "staged\n", "new/c.txt": "untracked\n", "skip.log": "ignored\n",
		"later.txt": "untracked, made after the snapshot\n", "e.txt": "<open " + filepath.Join(dir, "e.txt") + ": no such file or directory>",
	} {
		if got := read(t, dir, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if staged := git(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("the restore changed the index: %s", staged)
	}
}

func TestSnapshotWithoutACommitOrAnything(t *testing.T) {
	isolate(t)
	signingTrap(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if s, err := Snapshot(dir, time.Now(), "P0", ""); err != nil || s.Ref != "" {
		t.Fatalf("an empty repository: %+v %v", s, err)
	}
	write(t, dir, "first.txt", "hello\n")
	s, err := Snapshot(dir, time.Now(), "P0", "")
	if err != nil || s.Ref == "" || s.Files != 1 {
		t.Fatalf("no HEAD yet: %+v %v", s, err)
	}
	if parents := git(t, dir, "log", "-1", "--format=%P", s.Ref); parents != "" {
		t.Errorf("parents %q", parents)
	}

	clean := newRepo(t)
	write(t, clean, "only.log", "ignored\n")
	if s, err := Snapshot(clean, time.Now(), "P0", ""); err != nil || s.Ref != "" || len(Snapshots(clean)) != 0 {
		t.Errorf("a clean tree saved %+v %v", s, err)
	}
}

func TestSnapshotSkipsTheBiggestUntrackedFiles(t *testing.T) {
	dir := newRepo(t)
	defer func(n int64) { MaxUntracked = n }(MaxUntracked)
	MaxUntracked = 10
	write(t, dir, "four.txt", "1234")
	write(t, dir, "five.txt", "12345")
	write(t, dir, "big.bin", strings.Repeat("x", 20))
	write(t, dir, "two.txt", "12") // smallest first: 2 and 4 fit in 10 bytes, 5 more would not
	s, err := Snapshot(dir, time.Now(), "P1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Skipped, []string{"big.bin", "five.txt"}) || s.Files != 2 {
		t.Errorf("saved %d files, skipped %q", s.Files, s.Skipped)
	}
}

func TestOnlyTheNewestSnapshotsAreKept(t *testing.T) {
	dir := newRepo(t)
	defer func(n int) { KeepSnapshots = n }(KeepSnapshots)
	KeepSnapshots = 3
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var refs []string
	for i := range 5 {
		write(t, dir, "a.txt", strings.Repeat("v", i+1))
		s, err := Snapshot(dir, at.Add(time.Duration(i)*time.Minute), "P2", "")
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, s.Ref)
	}
	var got []string
	for _, s := range Snapshots(dir) {
		got = append(got, s.Ref)
	}
	if want := []string{refs[4], refs[3], refs[2]}; !reflect.DeepEqual(got, want) {
		t.Errorf("kept %q, want %q", got, want)
	}
	// Two in the same second get different names.
	write(t, dir, "a.txt", "same second")
	s, _ := Snapshot(dir, at.Add(4*time.Minute), "P2", "")
	if s.Ref != refs[4]+"-2" || Snapshots(dir)[0].Ref != s.Ref {
		t.Errorf("same second: %s; newest %s", s.Ref, Snapshots(dir)[0].Ref)
	}
}

func TestWithoutGitNothingHappens(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "a.txt", "changed\n")
	plain := t.TempDir()
	write(t, plain, "a.txt", "work\n")
	check := func(name, dir string) {
		if s, err := Snapshot(dir, time.Now(), "P1", ""); !errors.Is(err, ErrNoGit) || s.Ref != "" {
			t.Errorf("%s: Snapshot %+v %v", name, s, err)
		}
		if Head(dir) != "" || Snapshots(dir) != nil {
			t.Errorf("%s: Head or Snapshots found something", name)
		}
	}
	check("plain folder", plain)
	noGit(t)
	check("no git installed", repo)
	if _, err := os.Stat(filepath.Join(plain, ".git")); err == nil {
		t.Error("a repository was created")
	}
}

func TestParseStatus(t *testing.T) {
	out := " M a.txt\x00R  new name.txt\x00old name.txt\x00?? dir/x\x00A  b\x00 D gone\x00"
	want := []string{"a.txt", "b", "dir/x", "gone", "new name.txt", "old name.txt"}
	if got := parseStatus([]byte(out)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRefPart(t *testing.T) {
	for in, want := range map[string]string{"P4": "P4", "Phase 3": "Phase-3", "../x.lock": "x-lock", "": "run", "P1a_b": "P1a_b"} {
		if got := refPart(in); got != want {
			t.Errorf("refPart(%q) = %q, want %q", in, got, want)
		}
	}
}
