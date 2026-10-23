package gitx

import (
	"context"
	"reflect"
	"testing"
)

// Uncommitted lists the tracked changes a merge would not start over, staged or not, and leaves out
// untracked and ignored files, which a merge leaves be.
func TestUncommitted(t *testing.T) {
	for _, c := range []struct {
		name  string
		local func(l *landing)
		want  []string
	}{
		{"clean", func(l *landing) {}, nil},
		{"untracked and ignored files", func(l *landing) { write(t, l.wt, "U", "u\n"); write(t, l.wt, "x.log", "log\n") }, nil},
		{"an unstaged change", func(l *landing) { write(t, l.wt, "A", "a-local\n") }, []string{"A"}},
		{"a staged change and a deletion", func(l *landing) {
			write(t, l.wt, "B", "b-local\n")
			git(t, l.wt, "add", "B")
			git(t, l.wt, "rm", "-q", "C")
		}, []string{"B", "C"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLanding(t)
			c.local(l)
			got, err := Uncommitted(context.Background(), l.wt)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Uncommitted = %q, want %q", got, c.want)
			}
		})
	}
}
