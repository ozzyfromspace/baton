package decide

import (
	"strings"
	"testing"
	"time"
)

// What the model is told as its background work starts is what a stop on it would be refused with, said
// for a turn that has not ended: with git, a long gate goes to baton (a phase agent's as words after --);
// without, nothing asks for a commit; a Monitor wakes it with what it reports.
func TestBackgroundStarted(t *testing.T) {
	for _, c := range []struct {
		name    string
		w       Words
		monitor bool
		want    []string
		not     []string
	}{
		{name: "the lead, with git", w: Words{Git: true, Timeout: time.Minute},
			want: []string{"[baton] Your background work runs (npm test). Keep working on what does not depend on it: Claude Code wakes you when it ends.",
				"invalidated by your own edits", "`baton verify \"<command>\" --notes \"<where the phase stands>\"`",
				"If the phase is complete and committed, report it with `baton done`; if nothing is left to do until the work ends, end your turn."},
			not: []string{"You stopped", "again"}},
		{name: "a phase agent", w: Words{Git: true, Worker: true, Timeout: time.Minute},
			want: []string{"`baton verify --notes \"<where the phase stands>\" -- <command>`"}, not: []string{`verify "<command>"`}},
		{name: "without git", w: Words{Timeout: time.Minute},
			want: []string{"If the phase is complete, report it with `baton done`"}, not: []string{"commit", "verify"}},
		{name: "a Monitor", w: Words{Git: true, Timeout: time.Minute}, monitor: true,
			want: []string{"[baton] Your monitor runs (npm test). Keep working on what does not depend on it: Claude Code wakes you with what it reports.",
				"if nothing is left to do until it reports, end your turn."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.w.BackgroundStarted("npm test", c.monitor)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("lacks %q:\n%s", w, got)
				}
			}
			for _, w := range c.not {
				if strings.Contains(strings.ToLower(got), strings.ToLower(w)) {
					t.Errorf("says %q:\n%s", w, got)
				}
			}
		})
	}
	// The stop's refusal says the same, as a stop.
	w := Words{Git: true, Timeout: time.Minute}
	if stop, start := w.BackgroundStop("npm test"), w.BackgroundStarted("npm test", false); !strings.HasPrefix(stop, "[baton] You stopped while your background work runs (npm test). ") ||
		strings.TrimSuffix(strings.TrimPrefix(stop, "[baton] You stopped while your background work runs (npm test). "), " again.") !=
			strings.TrimSuffix(strings.TrimPrefix(start, "[baton] Your background work runs (npm test). "), ".") {
		t.Errorf("the stop and the start differ:\n%s\n%s", stop, start)
	}
}
