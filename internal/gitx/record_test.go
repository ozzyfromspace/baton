package gitx

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
)

// recordRepo is a checkout on main with a.txt and b.txt committed, and git's identity set as a user's is.
func recordRepo(t *testing.T) string {
	t.Helper()
	gittest.Isolate(t)
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Write(t, dir, "a.txt", "a\n")
	gittest.Write(t, dir, "b.txt", "b\n")
	gittest.Commit(t, dir, "first")
	return dir
}

// CommitOnly commits the record file alone, new or tracked, as it is in the working tree: what the
// checkout's own work staged stays staged, its other changes and untracked files stay as they were, and
// the commit holds the one path.
func TestCommitOnlyCommitsTheOnePath(t *testing.T) {
	ctx := context.Background()
	for _, tracked := range []bool{false, true} {
		name := map[bool]string{false: "a new record file", true: "a tracked one"}[tracked]
		t.Run(name, func(t *testing.T) {
			dir := recordRepo(t)
			if tracked {
				gittest.Write(t, dir, "docs/RECORD.md", "# Record\n")
				gittest.Commit(t, dir, "the record", "docs/RECORD.md")
			}
			gittest.Write(t, dir, "a.txt", "a, staged\n")
			gittest.Git(t, dir, "add", "a.txt")
			gittest.Write(t, dir, "a.txt", "a, staged, then changed\n")
			gittest.Write(t, dir, "b.txt", "b, changed\n")
			gittest.Write(t, dir, "new.txt", "untracked\n")
			before := gittest.Git(t, dir, "rev-parse", "HEAD")
			gittest.Write(t, dir, "docs/RECORD.md", "# Record\n\n## P0 — First\n")

			commit, err := CommitOnly(ctx, dir, "docs/RECORD.md", "chore(baton): record P0 — First")
			if err != nil {
				t.Fatal(err)
			}
			if head := gittest.Git(t, dir, "rev-parse", "HEAD"); commit != head || gittest.Git(t, dir, "rev-parse", "HEAD^") != before {
				t.Errorf("commit %s, HEAD %s: want a new commit on %s", commit, head, before)
			}
			if files := gittest.Git(t, dir, "show", "--name-only", "--format=", "HEAD"); files != "docs/RECORD.md" {
				t.Errorf("the commit holds %q", files)
			}
			if msg := gittest.Git(t, dir, "log", "-1", "--format=%s"); msg != "chore(baton): record P0 — First" {
				t.Errorf("message %q", msg)
			}
			if got := gittest.Git(t, dir, "show", "HEAD:docs/RECORD.md"); got != "# Record\n\n## P0 — First" {
				t.Errorf("committed %q", got)
			}
			if staged := gittest.Git(t, dir, "show", ":a.txt"); staged != "a, staged" {
				t.Errorf("a.txt's staged content is %q: the checkout's own staging changed", staged)
			}
			status := gittest.Git(t, dir, "status", "--porcelain")
			for _, want := range []string{"MM a.txt", " M b.txt", "?? new.txt"} {
				if !strings.Contains(status, want) {
					t.Errorf("status lacks %q:\n%s", want, status)
				}
			}
			if strings.Contains(status, "RECORD") {
				t.Errorf("the record file is still dirty:\n%s", status)
			}
			if dirty, err := PathDirty(dir, "docs/RECORD.md"); dirty || err != nil {
				t.Errorf("PathDirty = %v, %v", dirty, err)
			}
		})
	}
}

// A commit that fails leaves nothing of baton's staged: a new record file is taken out of the index again.
func TestCommitOnlyThatFailsStagesNothing(t *testing.T) {
	dir := recordRepo(t)
	for _, k := range []string{"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL"} {
		os.Unsetenv(k) // no identity: git refuses to commit
	}
	gittest.Git(t, dir, "config", "user.useConfigOnly", "true")
	gittest.Write(t, dir, "RECORD.md", "## P0\n")
	if _, err := CommitOnly(context.Background(), dir, "RECORD.md", "record"); err == nil {
		t.Fatal("committed with no identity")
	}
	if Tracked(context.Background(), dir, "RECORD.md") {
		t.Error("the new record file stays in the index")
	}
	if status := gittest.Git(t, dir, "status", "--porcelain"); status != "?? RECORD.md" {
		t.Errorf("status %q", status)
	}
}

// PathDirty says whether one path has uncommitted work: changed, staged or untracked.
func TestPathDirty(t *testing.T) {
	dir := recordRepo(t)
	gittest.Write(t, dir, "b.txt", "b, changed\n")
	for _, c := range []struct {
		name  string
		setup func()
		want  bool
	}{
		{"clean, another file changed", func() {}, false},
		{"absent", func() {}, false},
		{"untracked", func() { gittest.Write(t, dir, "R.md", "x\n") }, true},
		{"staged", func() { gittest.Git(t, dir, "add", "R.md") }, true},
		{"committed", func() { gittest.Commit(t, dir, "r", "R.md") }, false},
		{"changed", func() { gittest.Write(t, dir, "R.md", "y\n") }, true},
	} {
		c.setup()
		path := "R.md"
		if c.name == "clean, another file changed" {
			path = "a.txt"
		}
		if got, err := PathDirty(dir, path); got != c.want || err != nil {
			t.Errorf("%s: PathDirty = %v, %v", c.name, got, err)
		}
	}
}

// Log and Described read commits with their subjects and dates, oldest first or in the order asked, and
// LastCommitOf finds the latest commit of a path.
func TestLogAndDescribed(t *testing.T) {
	ctx := context.Background()
	dir := recordRepo(t)
	first := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Write(t, dir, "a.txt", "two\n")
	gittest.Commit(t, dir, "second: a")
	second := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Write(t, dir, "b.txt", "three\n")
	gittest.Commit(t, dir, "third: b")
	third := gittest.Git(t, dir, "rev-parse", "HEAD")

	log, err := Log(ctx, dir, first, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range log {
		got = append(got, c.Short()+" "+c.Subject)
		if c.When.IsZero() {
			t.Errorf("%s has no date", c.Hash)
		}
	}
	if want := []string{second[:7] + " second: a", third[:7] + " third: b"}; !slices.Equal(got, want) {
		t.Errorf("Log = %v, want %v", got, want)
	}
	d, err := Described(ctx, dir, []string{third, first})
	if err != nil || len(d) != 2 || d[0].Hash != third || d[1].Hash != first || d[1].Subject != "first" {
		t.Errorf("Described = %+v, %v", d, err)
	}
	if c := LastCommitOf(ctx, dir, "a.txt"); c != second {
		t.Errorf("LastCommitOf(a.txt) = %s, want %s", c, second)
	}
	if c := LastCommitOf(ctx, dir, "none.md"); c != "" {
		t.Errorf("LastCommitOf(none.md) = %s", c)
	}
}
