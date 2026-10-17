package gitx

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// MergeBase finds where the worker's branch and main last met, also once main was merged into it; and
// ChangedBetween lists what the branch changes from there, a rename as both its paths, sorted.
func TestMergeBaseAndChangedBetween(t *testing.T) {
	l := newLanding(t)
	ctx := context.Background()
	start := l.head()
	git(t, l.wt, "mv", "C", "C2")
	tip := l.work("the worker's", map[string]string{"B": "b1\nb2\n", "N": "new\n"})
	write(t, l.main, "A", "a1\na2\na3\na4\n")
	commit(t, l.main, "the lead's")
	onto := l.head()

	for _, c := range []struct {
		name     string
		a, b     string
		base     string
		from, to string
		changed  []string
	}{
		{"the branch and main", onto, tip, start, start, tip, []string{"B", "C", "C2", "N"}},
		{"either way round", tip, onto, start, start, onto, []string{"A"}},
		{"an ancestor", start, tip, start, start, tip, []string{"B", "C", "C2", "N"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, err := MergeBase(ctx, l.main, c.a, c.b)
			if err != nil || base != c.base {
				t.Fatalf("MergeBase = %q, %v; want %q", base, err, c.base)
			}
			changed, err := ChangedBetween(ctx, l.main, c.from, c.to)
			if err != nil || !reflect.DeepEqual(changed, c.changed) {
				t.Errorf("ChangedBetween = %q, %v; want %q", changed, err, c.changed)
			}
		})
	}

	// Once main is merged into the branch, they last met at main's tip: from there, the branch changes only
	// its own files.
	git(t, l.wt, "merge", "-q", "--no-edit", "main")
	merged := git(t, l.wt, "rev-parse", "HEAD")
	if base, err := MergeBase(ctx, l.main, onto, merged); err != nil || base != onto {
		t.Fatalf("MergeBase after the merge = %q, %v; want main's %s", base, err, onto)
	}
	if changed, err := ChangedBetween(ctx, l.main, onto, merged); err != nil || !reflect.DeepEqual(changed, []string{"B", "C", "C2", "N"}) {
		t.Errorf("ChangedBetween after the merge = %q, %v", changed, err)
	}
	if changed, err := ChangedBetween(ctx, l.main, onto, onto); err != nil || len(changed) != 0 {
		t.Errorf("ChangedBetween a commit and itself = %q, %v", changed, err)
	}
}

// Two histories that share nothing have no merge base, and a commit that is not there is an error; a
// folder without git is ErrNoGit, for both.
func TestMergeBaseFails(t *testing.T) {
	l := newLanding(t)
	ctx := context.Background()
	git(t, l.main, "checkout", "-q", "--orphan", "other")
	write(t, l.main, "O", "other\n")
	commit(t, l.main, "unrelated")
	other := l.head()
	git(t, l.main, "checkout", "-q", "main")
	if base, err := MergeBase(ctx, l.main, other, l.head()); err == nil {
		t.Errorf("MergeBase of unrelated histories = %q, want an error", base)
	}
	if _, err := MergeBase(ctx, l.main, "0123456789abcdef0123456789abcdef01234567", l.head()); err == nil {
		t.Error("MergeBase of a missing commit: no error")
	}
	if _, err := ChangedBetween(ctx, l.main, "0123456789abcdef0123456789abcdef01234567", l.head()); err == nil {
		t.Error("ChangedBetween from a missing commit: no error")
	}
	plain := t.TempDir()
	if _, err := MergeBase(ctx, plain, "a", "b"); !errors.Is(err, ErrNoGit) {
		t.Errorf("MergeBase without git: %v", err)
	}
	if _, err := ChangedBetween(ctx, plain, "a", "b"); !errors.Is(err, ErrNoGit) {
		t.Errorf("ChangedBetween without git: %v", err)
	}
}
