package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What the lead and the human are told of a landing that waits on them: the files to commit, by path, and
// nothing that discards work; the branch the checkout is on.
func TestALandingThatWaits(t *testing.T) {
	for _, c := range []struct {
		name string
		got  string
		want []string
		not  []string
	}{
		{"the lead is asked to commit", LandCommit("P3", "main", []string{"a.go", "lib/b.go"}, 10*time.Minute),
			[]string{"[baton] P3's work", "has waited 10m to land on main", "it changes a.go, lib/b.go", "by path (git add -- a.go lib/b.go, then git commit)",
				"Never stash, reset or discard"}, nil},
		{"one file", LandCommit("P3", "main", []string{"a.go"}, 11*time.Minute), []string{"Commit your changes to it now"}, []string{"them now"}},
		{"the human is told", LandNudged("P3", []string{"a.go", "b.go"}, 10*time.Minute),
			[]string{"baton: P3 has waited 10m to land on the lead's uncommitted changes to a.go, b.go → asked the lead to commit them"}, nil},
		{"off the branch", LandOffBranch("P3", "spike", "main"),
			[]string{"P3's work waits to land: the checkout is on the branch spike, not on the run's branch main", "once the checkout is back on main"}, nil},
		{"on a detached HEAD", LandOffBranch("P3", "", "main"), []string{"the checkout is on a detached HEAD"}, []string{"branch \"\""}},
		{"the typed line", MailTyped([]string{state.MessageCommit}), []string{"waits to land on your uncommitted changes", "which files to commit"}, nil},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, c.got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(c.got, w) {
				t.Errorf("%s has %q:\n%s", c.name, w, c.got)
			}
		}
	}
}
