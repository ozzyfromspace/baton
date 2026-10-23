package gitx

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// LogPaths lists, under each path, the commits of main since it and the worker's branch last met that
// changed it, newest first: none for a path only the worker changed, or one nothing changed; a path is
// read as written, never as a pattern. From "", it lists the whole history of the path.
func TestLogPaths(t *testing.T) {
	l := newLanding(t)
	ctx := context.Background()
	start := l.head()
	l.work("the worker's", map[string]string{"A": "a1\nworker\na3\n", "W": "w\n"})
	write(t, l.main, "A", "a1\nP3\na3\n")
	write(t, l.main, "N", "new\n")
	commit(t, l.main, "P3: notes")
	write(t, l.main, "B", "b1\nb2\n")
	commit(t, l.main, "B only")
	write(t, l.main, "A", "a1\nthe lead's\na3\n")
	commit(t, l.main, "lead: A")
	onto := l.head()
	base, err := MergeBase(ctx, l.main, onto, "w")
	if err != nil || base != start {
		t.Fatalf("MergeBase = %q, %v", base, err)
	}

	subjects := func(m map[string][]Commit) map[string][]string {
		out := map[string][]string{}
		for p, cs := range m {
			for _, c := range cs {
				if len(c.Hash) != 40 || c.When.IsZero() {
					t.Errorf("%s: commit %+v", p, c)
				}
				out[p] = append(out[p], c.Subject)
			}
		}
		return out
	}
	for _, c := range []struct {
		name  string
		from  string
		paths []string
		want  map[string][]string
	}{
		{"since the merge base", base, []string{"A", "B", "C", "W"}, map[string][]string{"A": {"lead: A", "P3: notes"}, "B": {"B only"}}},
		{"a pattern is a name", base, []string{"A*", "?"}, map[string][]string{}},
		{"the whole history", "", []string{"B"}, map[string][]string{"B": {"B only", "init"}}},
		{"no paths", base, nil, map[string][]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := LogPaths(ctx, l.main, c.from, onto, c.paths)
			if err != nil {
				t.Fatal(err)
			}
			if s := subjects(got); !reflect.DeepEqual(s, c.want) {
				t.Errorf("LogPaths = %v, want %v", s, c.want)
			}
		})
	}

	if _, err := LogPaths(ctx, l.main, "0123456789abcdef0123456789abcdef01234567", onto, []string{"A"}); err == nil {
		t.Error("LogPaths from a missing commit: no error")
	}
	if _, err := LogPaths(ctx, t.TempDir(), base, onto, []string{"A"}); !errors.Is(err, ErrNoGit) {
		t.Errorf("LogPaths in a folder without git: %v", err)
	}
}
