package decide

import (
	"strings"
	"testing"
	"time"
)

// A loop that only waits is pointed to the wake Claude Code gives as a background task ends, and to baton
// await (a phase agent's as words after --): one started in the background says what such a loop can cost,
// one in the foreground the timeout it holds the turn for; either way the same call again goes through.
func TestWaitLoop(t *testing.T) {
	const cmd = "until grep -q done out.log; do sleep 3; done"
	common := []string{
		"Claude Code wakes you when a background task ends, and its result names the task's output file, so wait for that wake: keep working on what does not depend on it, and with nothing left to do, end your turn.",
		"Never poll another task's output.",
		"To wait on something outside the project (a CI run, a deploy, a server coming up), ",
		"runs it, records it and gives you the result when it ends.",
		"If this loop must run all the same, make the same call again: it goes through.",
	}
	for _, c := range []struct {
		name      string
		w         Words
		timeout   time.Duration
		want, not []string
		said      string
	}{
		{name: "the lead, in the background", w: Words{Git: true, Timeout: time.Minute},
			want: []string{"[baton] Not run yet: this starts a loop that only waits (" + cmd + ") as a background task of your own. ",
				"in a real run, one polled for 22 hours.", "`baton await \"<a command that ends when it is ready>\" --notes \"<where the phase stands>\"`"},
			not: []string{"timeout", "verify"}, said: "baton: questioned a loop that only waits, started in the background (" + cmd + "), once; the same call again runs it"},
		{name: "a phase agent", w: Words{Git: true, Worker: true, Timeout: time.Minute},
			want: []string{"`baton await --notes \"<where the phase stands>\" -- <a command that ends when it is ready>`"}, not: []string{`await "<`},
			said: "started in the background"},
		{name: "in the foreground", w: Words{Git: true, Timeout: time.Minute}, timeout: 10 * time.Minute,
			want: []string{"[baton] Not run yet: this waits in a loop (" + cmd + ") with a timeout of 10 minutes, holding your turn while it polls. "},
			not:  []string{"background task of your own", "22 hours"},
			said: "baton: questioned a loop that only waits, in the foreground with a timeout of 10 minutes (" + cmd + "), once; the same call again runs it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.w.WaitLoop(cmd, c.timeout)
			for _, w := range append(common, c.want...) {
				if !strings.Contains(got, w) {
					t.Errorf("lacks %q:\n%s", w, got)
				}
			}
			for _, w := range c.not {
				if strings.Contains(got, w) {
					t.Errorf("says %q:\n%s", w, got)
				}
			}
			if said := c.w.WaitLoopSaid(cmd, c.timeout); !strings.Contains(said, c.said) {
				t.Errorf("said %q", said)
			}
		})
	}
}

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
