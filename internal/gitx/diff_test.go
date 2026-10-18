package gitx

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
)

// Diff gives the diffstat and the patch between two commits.
func TestDiff(t *testing.T) {
	gittest.Isolate(t)
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Write(t, dir, "a.go", "package a\n")
	gittest.Commit(t, dir, "init")
	from := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Write(t, dir, "a.go", "package a\n\nfunc A() {}\n")
	gittest.Commit(t, dir, "A")
	stat, patch, err := Diff(context.Background(), dir, from, "HEAD")
	if err != nil || !strings.Contains(stat, "a.go") || !strings.Contains(stat, "1 file changed") || !strings.Contains(patch, "+func A() {}") {
		t.Errorf("Diff = %q, %q, %v", stat, patch, err)
	}
	if _, _, err := Diff(context.Background(), t.TempDir(), from, "HEAD"); err == nil {
		t.Error("no error outside a repository")
	}
}

// A patch past its budget has its largest hunks clipped first, the files left aside keep only their
// headers, and what still does not fit is counted at the end.
func TestClipPatch(t *testing.T) {
	file := func(path string, lines int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1,%d @@\n", path, path, path, path, lines)
		for i := range lines {
			fmt.Fprintf(&b, "+line %d\n", i)
		}
		return b.String()
	}
	patch := file("small.go", 3) + file("big.go", 500) + file("go.sum", 50)
	leave := func(p string) bool { return p == "go.sum" }
	if got := ClipPatch(patch, 1<<20, nil); got != patch {
		t.Errorf("a patch within its budget changed:\n%s", got)
	}
	got := ClipPatch(patch, 2000, leave)
	if len(got) > 2000 || !strings.Contains(got, "+line 2\n") || !strings.Contains(got, "more lines of this hunk)") ||
		!strings.Contains(got, "+++ b/go.sum\n… (its changes left out") {
		t.Errorf("clipped to 2000 (%d):\n%s", len(got), got)
	}
	if !strings.Contains(got, "diff --git a/small.go") || strings.Count(got, "+line") > 3+60 {
		t.Errorf("the small hunk is not whole, or the big one not clipped:\n%s", got)
	}
	if got := ClipPatch(patch, 300, leave); !strings.Contains(got, "more files left out; the diffstat lists them)") {
		t.Errorf("no room for every file:\n%s", got)
	}
	if ClipPatch("", 100, nil) != "" || ClipPatch(patch, 0, nil) != "" {
		t.Error("nothing to clip gives something")
	}
}
