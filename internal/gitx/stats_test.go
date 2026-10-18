package gitx

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"
)

// Stats sums a set of commits' changes path by path, merges left out, renames as a deletion and an
// addition, a binary file without counts; a file created and deleted by them is neither, and one deleted
// that was there before is removed. CommitsUntil lists a range's commits, merges left out, up to a time.
func TestStatsAndCommitsUntil(t *testing.T) {
	l := newLanding(t)
	ctx := context.Background()
	start := l.head()
	w1 := l.work("one", map[string]string{"A": "a1\na2\na3\na4\n", "T": "temp\n", "dir/x y.go": "package x\n"})
	write(t, l.wt, "bin", "\x00\x01\x02")
	git(t, l.wt, "rm", "-q", "C", "T")
	git(t, l.wt, "mv", "B", "B2")
	w2 := l.work("two", map[string]string{"A": "a1\na3\na4\n"})
	write(t, l.main, "M", "m\n")
	commit(t, l.main, "the lead's")
	git(t, l.wt, "merge", "-q", "--no-edit", "main")
	merge := git(t, l.wt, "rev-parse", "HEAD")

	got, err := Stats(ctx, l.main, []string{w1, w2, merge})
	if err != nil {
		t.Fatal(err)
	}
	want := []FileStat{
		{Path: "A", Added: 1, Deleted: 1},
		{Path: "B", Deleted: 1, Removed: true},
		{Path: "B2", Added: 1, Created: true},
		{Path: "C", Deleted: 2, Removed: true},
		{Path: "T", Added: 1, Deleted: 1},
		{Path: "bin", Binary: true, Created: true},
		{Path: "dir/x y.go", Added: 1, Created: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Stats =\n%+v\nwant\n%+v", got, want)
	}
	if got, err := Stats(ctx, l.main, nil); err != nil || got != nil {
		t.Errorf("Stats of no commits = %+v, %v", got, err)
	}

	all, err := CommitsUntil(ctx, l.main, start, merge, time.Time{})
	if err != nil || len(all) != 3 || slices.Index(all, w1) > slices.Index(all, w2) || slices.Contains(all, merge) {
		t.Errorf("CommitsUntil = %q, %v; want w1, w2 and the lead's, no merge", all, err)
	}
	if none, err := CommitsUntil(ctx, l.main, start, merge, time.Unix(1000, 0)); err != nil || len(none) != 0 {
		t.Errorf("CommitsUntil long ago = %q, %v", none, err)
	}
	if _, err := Stats(ctx, t.TempDir(), []string{w1}); err == nil {
		t.Error("Stats outside a repository: no error")
	}
}

// parseStats reads git's quoting of a path with unusual characters.
func TestParseStatsQuoted(t *testing.T) {
	out := "\x01abc\n\n3\t1\t\"a\\tb.go\"\n create mode 100644 \"a\\tb.go\"\n"
	got := parseStats([]byte(out))
	want := []FileStat{{Path: "a\tb.go", Added: 3, Deleted: 1, Created: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseStats = %+v, want %+v", got, want)
	}
}
