package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// landing is spike 25's layout: a checkout on main, with A, B and C committed, and a worker's worktree on
// the branch w made from it, outside the checkout.
type landing struct {
	t        *testing.T
	main, wt string
}

func newLanding(t *testing.T) *landing {
	t.Helper()
	isolate(t)
	// A rebase makes commits: it needs an identity, which the throwaway configuration does not have.
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	l := &landing{t: t, main: t.TempDir(), wt: filepath.Join(t.TempDir(), "w")}
	git(t, l.main, "init", "-q", "-b", "main")
	write(t, l.main, "A", "a1\na2\na3\n")
	write(t, l.main, "B", "b1\n")
	write(t, l.main, "C", "c1\nc2\n")
	write(t, l.main, ".gitignore", "*.log\n")
	commit(t, l.main, "init")
	git(t, l.main, "worktree", "add", "-q", "-b", "w", l.wt, "main")
	return l
}

// work commits files (name → content) on the worker's branch.
func (l *landing) work(msg string, files map[string]string) string {
	for name, content := range files {
		write(l.t, l.wt, name, content)
	}
	commit(l.t, l.wt, msg)
	return git(l.t, l.wt, "rev-parse", "HEAD")
}

func (l *landing) head() string { return git(l.t, l.main, "rev-parse", "HEAD") }

// land probes and fast-forwards the checkout to the worker's branch, as the merge queue does once the
// branch is rebased: the paths in the way, and the fast-forward's error.
func (l *landing) land() ([]string, error) {
	ctx := context.Background()
	tip := git(l.t, l.wt, "rev-parse", "HEAD")
	changes, err := Changes(ctx, l.main, l.head(), tip)
	if err != nil {
		l.t.Fatal(err)
	}
	overlap, err := Overlap(l.main, changes)
	if err != nil {
		l.t.Fatal(err)
	}
	return overlap, FastForward(ctx, l.main, tip)
}

// The cases of spike 25: the fast-forward is all or nothing, never touches unrelated local changes, and
// the probe finds every local change in the way, ignored files included, which the fast-forward alone
// would overwrite.
func TestLandOnALiveCheckout(t *testing.T) {
	for _, c := range []struct {
		name    string
		local   func(l *landing) // the lead's local changes
		work    map[string]string
		overlap []string
		err     error
		files   map[string]string // the checkout's files afterwards
	}{
		{
			name:  "a: unrelated unstaged and untracked work is kept",
			local: func(l *landing) { write(t, l.main, "C", "c1\nc2\nc-local\n"); write(t, l.main, "U", "untracked\n") },
			work:  map[string]string{"A": "a1\na2\na3\na4\n", "B": "b1\nb2\n", "N": "new\n"},
			files: map[string]string{"A": "a1\na2\na3\na4\n", "B": "b1\nb2\n", "N": "new\n", "C": "c1\nc2\nc-local\n", "U": "untracked\n"},
		},
		{
			name:    "b: a path the lead changed is refused, and nothing is half applied",
			local:   func(l *landing) { write(t, l.main, "C", "c1\nc2\nc-local\n") },
			work:    map[string]string{"A": "a1\na2\na3\na4\n", "C": "c1\nc2\nc-worker\n"},
			overlap: []string{"C"},
			err:     ErrOverwrite,
			files:   map[string]string{"A": "a1\na2\na3\n", "C": "c1\nc2\nc-local\n"},
		},
		{
			name:    "c1: a path the lead staged is refused",
			local:   func(l *landing) { write(t, l.main, "A", "a1\na2\na3\na-staged\n"); git(t, l.main, "add", "A") },
			work:    map[string]string{"A": "a1\na2\na3\na4\n"},
			overlap: []string{"A"},
			err:     ErrOverwrite,
			files:   map[string]string{"A": "a1\na2\na3\na-staged\n"},
		},
		{
			name:  "c2: an unrelated staged change stays staged",
			local: func(l *landing) { write(t, l.main, "C", "c1\nc2\nc-staged\n"); git(t, l.main, "add", "C") },
			work:  map[string]string{"A": "a1\na2\na3\na4\n", "B": "b1\nb2\n"},
			files: map[string]string{"A": "a1\na2\na3\na4\n", "C": "c1\nc2\nc-staged\n"},
		},
		{
			name:    "c3: the lead's change, the same as the worker's, still counts as in the way",
			local:   func(l *landing) { write(t, l.main, "A", "a1\na2\na3\na4\n") },
			work:    map[string]string{"A": "a1\na2\na3\na4\n"},
			overlap: []string{"A"},
			err:     ErrOverwrite,
		},
		{
			name:    "e1: an untracked file where the branch adds one is refused",
			local:   func(l *landing) { write(t, l.main, "N", "mine\n") },
			work:    map[string]string{"N": "theirs\n"},
			overlap: []string{"N"},
			err:     ErrOverwrite,
			files:   map[string]string{"N": "mine\n"},
		},
		{
			name:    "a file where a directory the branch adds would go is in the way",
			local:   func(l *landing) { write(t, l.main, "dir", "a file\n") },
			work:    map[string]string{"dir/new": "theirs\n"},
			overlap: []string{"dir/new"},
			err:     ErrOverwrite,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLanding(t)
			before := l.head()
			tip := l.work("worker", c.work)
			c.local(l)
			overlap, err := l.land()
			if !reflect.DeepEqual(overlap, c.overlap) {
				t.Errorf("overlap = %q, want %q", overlap, c.overlap)
			}
			if !errors.Is(err, c.err) || (err == nil) != (c.err == nil) {
				t.Fatalf("fast-forward: %v, want %v", err, c.err)
			}
			want := tip
			if c.err != nil {
				want = before
			}
			if got := l.head(); got != want {
				t.Errorf("HEAD = %s, want %s", got, want)
			}
			for name, content := range c.files {
				if got := read(t, l.main, name); got != content {
					t.Errorf("%s = %q, want %q", name, got, content)
				}
			}
		})
	}
}

// e2: a fast-forward overwrites an ignored file without a word, so only the probe stands between it and
// the lead's file.
func TestLandProbesIgnoredFiles(t *testing.T) {
	l := newLanding(t)
	write(t, l.wt, "N.log", "theirs\n")
	git(t, l.wt, "add", "-f", "N.log")
	commit(t, l.wt, "worker: forced past the ignore")
	write(t, l.main, "N.log", "mine, ignored\n")
	overlap, err := l.land()
	if !reflect.DeepEqual(overlap, []string{"N.log"}) {
		t.Errorf("overlap = %q, want the ignored N.log", overlap)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, l.main, "N.log"); got != "theirs\n" {
		t.Errorf("N.log = %q: git did not overwrite it, so the probe may be stale", got)
	}
}

// f: the checkout's branch moved on after the rebase, so the branch is no longer ahead of it.
func TestLandNotAFastForward(t *testing.T) {
	ctx := context.Background()
	l := newLanding(t)
	tip := l.work("worker", map[string]string{"A": "a1\na2\na3\na4\n"})
	write(t, l.main, "B", "b1\nb-lead\n")
	commit(t, l.main, "lead: B")
	moved := l.head()
	if ok, err := IsAncestor(ctx, l.main, moved, tip); ok || err != nil {
		t.Errorf("IsAncestor(the moved branch, the worker's) = %v, %v; want false", ok, err)
	}
	if ok, err := IsAncestor(ctx, l.main, git(t, l.main, "rev-parse", "HEAD~1"), tip); !ok || err != nil {
		t.Errorf("IsAncestor(the branch's base, the worker's) = %v, %v; want true", ok, err)
	}
	if _, err := IsAncestor(ctx, l.main, "no-such-commit", tip); err == nil {
		t.Error("IsAncestor of a commit that is not there: no error")
	}
	if err := FastForward(ctx, l.main, tip); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("fast-forward: %v, want ErrNotFastForward", err)
	}
	if l.head() != moved {
		t.Error("the refused fast-forward moved HEAD")
	}
}

// Another git command holding the checkout's index: nothing is applied, and the error says so.
func TestLandOnALockedIndex(t *testing.T) {
	l := newLanding(t)
	before := l.head()
	l.work("worker", map[string]string{"A": "a1\na2\na3\na4\n"})
	lock := filepath.Join(l.main, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.land(); !errors.Is(err, ErrLocked) {
		t.Fatalf("fast-forward: %v, want ErrLocked", err)
	}
	if l.head() != before || read(t, l.main, "A") != "a1\na2\na3\n" {
		t.Error("the fast-forward applied something under a locked index")
	}
	os.Remove(lock)
	if _, err := l.land(); err != nil {
		t.Fatalf("once the lock is gone: %v", err)
	}
}

// Merge brings the worker's branch up to date with the moved run's branch by merging it in: nothing is
// rewritten, so every commit of the worker's stays on its branch, and the checkout lands it all by a
// fast-forward.
func TestMergeTheMovedBranchIn(t *testing.T) {
	ctx := context.Background()
	l := newLanding(t)
	tip := l.work("worker: A", map[string]string{"A": "a1\na2\na3\na4\n"})
	write(t, l.main, "B", "b1\nb-lead\n")
	commit(t, l.main, "lead: B")
	if err := Merge(ctx, l.wt, l.head(), "Merge branch 'main' into w"); err != nil {
		t.Fatal(err)
	}
	merged := git(t, l.wt, "rev-parse", "HEAD")
	if msg := git(t, l.wt, "log", "-1", "--format=%s"); msg != "Merge branch 'main' into w" {
		t.Errorf("the merge's message is %q", msg)
	}
	if ok, _ := IsAncestor(ctx, l.main, tip, merged); !ok {
		t.Error("the merge rewrote the worker's commit")
	}
	if ok, _ := IsAncestor(ctx, l.main, l.head(), merged); !ok {
		t.Error("the merged branch is not on the moved branch")
	}
	commits, err := Commits(ctx, l.main, l.head(), merged)
	if err != nil || len(commits) != 2 || commits[0] != tip || commits[1] != merged {
		t.Errorf("commits to land = %q, %v; want the worker's and the merge", commits, err)
	}
	changes, err := Changes(ctx, l.main, l.head(), merged)
	if want := []Change{{Path: "A"}}; err != nil || !reflect.DeepEqual(changes, want) {
		t.Errorf("changes = %+v, %v; want %+v", changes, err, want)
	}
	if _, err := l.land(); err != nil {
		t.Fatal(err)
	}
	if read(t, l.main, "B") != "b1\nb-lead\n" || read(t, l.main, "A") != "a1\na2\na3\na4\n" {
		t.Error("the landing lost the lead's commit or the worker's")
	}
}

// resolved makes the worker's branch hold a conflict it resolved: its change to C conflicts with the
// lead's, and it merges the run's branch in, keeping both lines. It returns the merge commit.
func (l *landing) resolved() string {
	t := l.t
	l.work("worker: C", map[string]string{"C": "c1\nc-worker\n"})
	write(t, l.main, "C", "c1\nc-lead\n")
	commit(t, l.main, "lead: C")
	exec.Command("git", "-C", l.wt, "merge", "-q", "--no-edit", "main").Run()
	write(t, l.wt, "C", "c1\nc-lead\nc-worker\n")
	git(t, l.wt, "add", "C")
	git(t, l.wt, "commit", "-q", "--no-edit")
	return git(t, l.wt, "rev-parse", "HEAD")
}

// A conflict the worker resolved stays resolved when the run's branch moves on again, where a rebase
// would drop its merge and replay the conflict, or, once the lead takes its line back, drop the
// resolution without a word.
func TestMergeKeepsAResolution(t *testing.T) {
	ctx := context.Background()
	l := newLanding(t)
	resolved := l.resolved()
	write(t, l.main, "B", "b1\nb-lead\n")
	commit(t, l.main, "lead: B")
	if err := Merge(ctx, l.wt, l.head(), "Merge branch 'main' into w"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsAncestor(ctx, l.main, resolved, "w"); !ok {
		t.Error("the resolution is not on the branch")
	}
	if read(t, l.wt, "C") != "c1\nc-lead\nc-worker\n" || read(t, l.wt, "B") != "b1\nb-lead\n" {
		t.Error("the merge lost the resolution or the lead's commit")
	}
}

// Whatever stops a merge short of a conflict leaves the worker's branch, its index and its files as they
// were: uncommitted work (never stashed or merged over), an untracked file in the way (kept), and a merge
// commit that cannot be signed (aborted).
func TestAMergeThatStopsChangesNothing(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		setup func(l *landing)
		kept  map[string]string // the worktree's files afterwards
	}{
		{"uncommitted work", func(l *landing) {
			l.moved()
			git(l.t, l.wt, "config", "merge.autoStash", "true")
			write(l.t, l.wt, "A", "half done\n")
		}, map[string]string{"A": "half done\n"}},
		{"an untracked file in the way", func(l *landing) {
			l.work("worker: A", map[string]string{"A": "a1\na2\na3\na4\n"})
			write(l.t, l.main, "N", "the lead's\n")
			commit(l.t, l.main, "lead: N")
			write(l.t, l.wt, "N", "the worker's, untracked\n")
		}, map[string]string{"N": "the worker's, untracked\n"}},
		{"a merge commit that cannot be signed", func(l *landing) {
			l.moved()
			git(l.t, l.wt, "config", "commit.gpgsign", "true")
			git(l.t, l.wt, "config", "gpg.program", "false")
		}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLanding(t)
			c.setup(l)
			tip, status := git(t, l.wt, "rev-parse", "HEAD"), git(t, l.wt, "status", "--porcelain")
			err := Merge(ctx, l.wt, l.head(), "Merge branch 'main' into w")
			var me *MergeError
			if !errors.As(err, &me) || me.Paths != nil || me.Kept || errors.Is(err, ErrConflict) || me.Msg == "" {
				t.Fatalf("merge: %v, want one stopped with no conflict and a reason", err)
			}
			if got := git(t, l.wt, "rev-parse", "HEAD"); got != tip || Merging(ctx, l.wt) {
				t.Errorf("HEAD = %s (want %s), still merging %v", got, tip, Merging(ctx, l.wt))
			}
			if got := git(t, l.wt, "status", "--porcelain"); got != status {
				t.Errorf("the worktree was %q, and is %q", status, got)
			}
			for name, content := range c.kept {
				if got := read(t, l.wt, name); got != content {
					t.Errorf("%s = %q, want %q", name, got, content)
				}
			}
			if got := git(t, l.wt, "stash", "list"); got != "" {
				t.Errorf("the merge stashed: %s", got)
			}
		})
	}
}

// A merge that stops on conflicts is kept for the worktree's agent: still in progress, its paths named,
// the markers in the files, and the branch's commits as they were. Since it started on a clean worktree,
// `git merge --abort` alone would give everything back as it was.
func TestAConflictedMergeIsKept(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		setup  func(l *landing)
		paths  []string
		before map[string]string // the worktree's files before the merge
	}{
		{"a conflict", func(l *landing) { l.conflict() }, []string{"C"}, map[string]string{"C": "c1\nc-worker\n"}},
		{"a conflict with a resolution on the branch", func(l *landing) {
			l.resolved()
			write(l.t, l.main, "C", "c1\n")
			commit(l.t, l.main, "lead: takes its line back")
		}, []string{"C"}, map[string]string{"C": "c1\nc-lead\nc-worker\n"}},
		{"two paths", func(l *landing) {
			l.work("worker: A and C", map[string]string{"A": "a-worker\n", "C": "c1\nc-worker\n"})
			write(l.t, l.main, "A", "a-lead\n")
			write(l.t, l.main, "C", "c1\nc-lead\n")
			commit(l.t, l.main, "lead: A and C")
		}, []string{"A", "C"}, map[string]string{"A": "a-worker\n", "C": "c1\nc-worker\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLanding(t)
			c.setup(l)
			tip, status := git(t, l.wt, "rev-parse", "HEAD"), git(t, l.wt, "status", "--porcelain")
			err := Merge(ctx, l.wt, l.head(), "Merge branch 'main' into w")
			var me *MergeError
			if !errors.As(err, &me) || !reflect.DeepEqual(me.Paths, c.paths) || !me.Kept || !errors.Is(err, ErrConflict) {
				t.Fatalf("merge: %v, want one kept with conflicts in %q", err, c.paths)
			}
			if got := git(t, l.wt, "rev-parse", "HEAD"); got != tip || !Merging(ctx, l.wt) {
				t.Errorf("HEAD = %s (want %s), merging %v (want true)", got, tip, Merging(ctx, l.wt))
			}
			if got, err := Unmerged(ctx, l.wt); err != nil || !reflect.DeepEqual(got, c.paths) {
				t.Errorf("Unmerged = %q, %v; want %q", got, err, c.paths)
			}
			for _, p := range c.paths {
				got := read(t, l.wt, p)
				for _, m := range []string{"<<<<<<< HEAD\n", "\n=======\n", "\n>>>>>>> "} {
					if !strings.Contains(got, m) {
						t.Errorf("%s = %q, without the marker %q", p, got, m)
					}
				}
			}
			if got := git(t, l.wt, "stash", "list"); got != "" {
				t.Errorf("the merge stashed: %s", got)
			}
			git(t, l.wt, "merge", "--abort")
			if got := git(t, l.wt, "status", "--porcelain"); got != status || Merging(ctx, l.wt) {
				t.Errorf("after an abort the worktree is %q (was %q), merging %v", got, status, Merging(ctx, l.wt))
			}
			for name, content := range c.before {
				if got := read(t, l.wt, name); got != content {
					t.Errorf("after an abort %s = %q, want %q", name, got, content)
				}
			}
		})
	}
}

// CommitMerge commits a merge the agent resolved, and refuses one with a path still unmerged or a
// conflict marker left in what is staged, leaving it in progress. A marker one side already had is no
// leftover.
func TestCommitMerge(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		lead     map[string]string // committed on the run's branch beside its change to C
		setup    func(l *landing)  // after the run's branch is merged in, kept
		unmerged []string
		markers  []string
		fails    bool // an error of git's
	}{
		{name: "resolved", setup: func(l *landing) { l.resolve("c1\nc-lead\nc-worker\n") }},
		{name: "a path unmerged", setup: func(l *landing) {}, unmerged: []string{"C"}},
		{name: "a marker left", setup: func(l *landing) {
			l.resolve("c1\n<<<<<<< HEAD\nc-worker\nc-lead\n")
		}, markers: []string{"C:2"}},
		{name: "every marker left", setup: func(l *landing) {
			write(l.t, l.wt, "C", read(l.t, l.wt, "C"))
			git(l.t, l.wt, "add", "C")
		}, markers: []string{"C:2", "C:4", "C:6"}},
		{name: "a marker the run's branch had", lead: map[string]string{"M.md": "Title\n=======\n"}, setup: func(l *landing) {
			l.resolve("c1\nc-lead\nc-worker\n")
		}},
		{name: "a commit that cannot be signed", setup: func(l *landing) {
			l.resolve("c1\nc-lead\nc-worker\n")
			git(l.t, l.wt, "config", "commit.gpgsign", "true")
			git(l.t, l.wt, "config", "gpg.program", "false")
		}, fails: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLanding(t)
			l.work("worker: C", map[string]string{"C": "c1\nc-worker\n"})
			write(t, l.main, "C", "c1\nc-lead\n")
			for name, content := range c.lead {
				write(t, l.main, name, content)
			}
			commit(t, l.main, "lead: C")
			tip := git(t, l.wt, "rev-parse", "HEAD")
			if err := Merge(ctx, l.wt, l.head(), "Merge branch 'main' into w"); !errors.Is(err, ErrConflict) {
				t.Fatalf("merge: %v, want a kept conflict", err)
			}
			c.setup(l)
			err := CommitMerge(ctx, l.wt)
			var ue *UnresolvedError
			switch {
			case c.unmerged != nil || c.markers != nil:
				if !errors.As(err, &ue) || !reflect.DeepEqual(ue.Paths, c.unmerged) || !reflect.DeepEqual(ue.Markers, c.markers) {
					t.Fatalf("CommitMerge: %v, want unmerged %q and markers %q", err, c.unmerged, c.markers)
				}
			case c.fails:
				if err == nil || errors.As(err, &ue) {
					t.Fatalf("CommitMerge: %v, want git's error", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if Merging(ctx, l.wt) {
					t.Error("still merging after the commit")
				}
				if parents := strings.Fields(git(t, l.wt, "log", "-1", "--format=%P")); len(parents) != 2 || parents[0] != tip || parents[1] != l.head() {
					t.Errorf("the commit's parents are %q, want the branch's tip and the run's", parents)
				}
				if msg := git(t, l.wt, "log", "-1", "--format=%s"); msg != "Merge branch 'main' into w" {
					t.Errorf("the commit's message is %q, want the one Merge gave", msg)
				}
				return
			}
			if got := git(t, l.wt, "rev-parse", "HEAD"); got != tip || !Merging(ctx, l.wt) {
				t.Errorf("HEAD = %s (want %s), merging %v (want true): a refused commit changed the merge", got, tip, Merging(ctx, l.wt))
			}
		})
	}
	t.Run("no merge in progress", func(t *testing.T) {
		if err := CommitMerge(ctx, newLanding(t).wt); !errors.Is(err, ErrNoMerge) {
			t.Errorf("CommitMerge: %v, want ErrNoMerge", err)
		}
	})
}

// conflict commits the worker's change to C and the lead's, which conflict.
func (l *landing) conflict() {
	l.work("worker: C", map[string]string{"C": "c1\nc-worker\n"})
	write(l.t, l.main, "C", "c1\nc-lead\n")
	commit(l.t, l.main, "lead: C")
}

// resolve writes C as the agent resolved it, and adds it.
func (l *landing) resolve(content string) {
	write(l.t, l.wt, "C", content)
	git(l.t, l.wt, "add", "C")
}

// moved commits the worker's change to A, and the lead's to B: a merge of the two goes through.
func (l *landing) moved() {
	l.work("worker: A", map[string]string{"A": "a1\na2\na3\na4\n"})
	write(l.t, l.main, "B", "b1\nb-lead\n")
	commit(l.t, l.main, "lead: B")
}

func TestChangesAddedAndDeleted(t *testing.T) {
	l := newLanding(t)
	base := l.head()
	git(t, l.wt, "rm", "-q", "B")
	write(t, l.wt, "sub/N", "new\n")
	write(t, l.wt, "A", "a1\n")
	commit(t, l.wt, "worker")
	changes, err := Changes(context.Background(), l.main, base, git(t, l.wt, "rev-parse", "HEAD"))
	want := []Change{{Path: "A"}, {Path: "B"}, {Path: "sub/N", Added: true}}
	if err != nil || !reflect.DeepEqual(changes, want) {
		t.Errorf("changes = %+v, %v; want %+v", changes, err, want)
	}
}

func TestLandWithoutGit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := IsAncestor(ctx, dir, "a", "b"); err != ErrNoGit {
		t.Errorf("IsAncestor: %v", err)
	}
	if err := Merge(ctx, dir, "a", "m"); err != ErrNoGit {
		t.Errorf("Merge: %v", err)
	}
	if _, err := Unmerged(ctx, dir); err != ErrNoGit {
		t.Errorf("Unmerged: %v", err)
	}
	if err := CommitMerge(ctx, dir); err != ErrNoGit {
		t.Errorf("CommitMerge: %v", err)
	}
	if _, err := Changes(ctx, dir, "a", "b"); err != ErrNoGit {
		t.Errorf("Changes: %v", err)
	}
	if _, err := Commits(ctx, dir, "a", "b"); err != ErrNoGit {
		t.Errorf("Commits: %v", err)
	}
	if _, err := Overlap(dir, nil); err != ErrNoGit {
		t.Errorf("Overlap: %v", err)
	}
	if err := FastForward(ctx, dir, "a"); err != ErrNoGit {
		t.Errorf("FastForward: %v", err)
	}
}
