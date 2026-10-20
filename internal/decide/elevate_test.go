package decide

import (
	"strings"
	"testing"
)

// A held elevation names what it waits on, each on one line and clipped, and at most three.
func TestElevateWaitsNamesTheWork(t *testing.T) {
	long := strings.Repeat("x", 100)
	for _, c := range []struct {
		work []string
		want string
	}{
		{[]string{"tail -F app.log"}, ": tail -F app.log"},
		{[]string{"run\n  the suite", "a subagent"}, ": run the suite; a subagent"},
		{[]string{long}, ": " + strings.Repeat("x", 77) + "…"},
		{[]string{"a", "b", "c", "d", "e"}, ": a; b; c; 2 more"},
	} {
		got := ElevateWaits(c.work)
		if !strings.HasPrefix(got, "baton: starting baton waits until background work finishes (it would be stopped with claude)") || !strings.HasSuffix(got, c.want) {
			t.Errorf("%q: %q", c.work, got)
		}
	}
}
