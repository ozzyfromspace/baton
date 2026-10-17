package decide

import (
	"strings"
	"testing"
	"time"
)

// A check started as a background task is pointed to baton verify with git (a phase agent's job as words
// after --), to baton await without; either way the refusal says why, and that the same call again goes
// through.
func TestBackgroundCheck(t *testing.T) {
	for _, c := range []struct {
		name      string
		w         Words
		want, not []string
		said      string
	}{
		{name: "the lead, with git", w: Words{Git: true, Timeout: time.Minute},
			want: []string{"commit, then `baton verify \"<command>\" --notes \"<where the phase stands>\"` runs it on that commit in a lane of its own, records it, and tells you of its failures as they happen"},
			not:  []string{"await", "-- <command>"}, said: "to baton verify, once"},
		{name: "a phase agent", w: Words{Git: true, Worker: true, Timeout: time.Minute},
			want: []string{"`baton verify --notes \"<where the phase stands>\" -- <command>`"}, not: []string{`verify "<command>"`}, said: "to baton verify, once"},
		{name: "without git", w: Words{Timeout: time.Minute},
			want: []string{"`baton await \"<command>\" --notes \"<where the phase stands>\"` runs it from the working tree"}, not: []string{"commit", "verify"}, said: "to baton await, once"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.w.BackgroundCheck("go test ./...")
			for _, w := range append([]string{
				"[baton] Not run yet: this starts a check (go test ./...) as a background task of your own, which baton cannot see: nothing records it, it holds no lane, and you learn of a failure only when it ends.",
				"A quick check (the tests of what you changed, by name) runs in the foreground, in place.",
				"If this one must run as a background task all the same, make the same call again: it goes through.",
			}, c.want...) {
				if !strings.Contains(got, w) {
					t.Errorf("lacks %q:\n%s", w, got)
				}
			}
			for _, w := range c.not {
				if strings.Contains(got, w) {
					t.Errorf("says %q:\n%s", w, got)
				}
			}
			if said := c.w.BackgroundCheckSaid("go test ./..."); !strings.HasPrefix(said, "baton: pointed a check started in the background (go test ./...) ") ||
				!strings.Contains(said, c.said) || !strings.HasSuffix(said, "the same call again runs it") {
				t.Errorf("said %q", said)
			}
		})
	}
}
