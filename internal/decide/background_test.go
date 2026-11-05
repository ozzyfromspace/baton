package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What the model is told as its background work starts is what a stop on it would be refused with, said
// for a turn that has not ended: with git, a long gate goes to baton (a phase agent's as words after --);
// without, nothing asks for a commit; a Monitor wakes it with what it reports. A shell is also told when
// baton may stop it: on its reviewer's verdict, over signals such as task_quiet with nothing written.
func TestBackgroundStarted(t *testing.T) {
	const stops = "baton's reviewer looks at a background shell that writes nothing for 30 minutes, outlives its phase or the run time its description declares, " +
		"or waits on a task that has ended, and baton stops it only when the reviewer finds it can do nothing more for the run, and tells you why."
	for _, c := range []struct {
		name string
		w    Words
		kind string
		want []string
		not  []string
	}{
		{name: "the lead, with git", w: Words{Git: true, Timeout: time.Minute}, kind: state.TaskShell,
			want: []string{"[baton] Your background work runs (npm test). Keep working on what does not depend on it: Claude Code wakes you when it ends.",
				"invalidated by your own edits", "`baton verify \"<command>\" --notes \"<where the phase stands>\"`",
				"If the phase is complete and committed, report it with `baton done`; if nothing is left to do until the work ends, end your turn. " + stops},
			not: []string{"You stopped", "again"}},
		{name: "a phase agent", w: Words{Git: true, Worker: true, Timeout: time.Minute}, kind: state.TaskShell,
			want: []string{"`baton verify --notes \"<where the phase stands>\" -- <command>`", stops}, not: []string{`verify "<command>"`}},
		{name: "without git", w: Words{Timeout: time.Minute}, kind: state.TaskShell,
			want: []string{"If the phase is complete, report it with `baton done`", stops}, not: []string{"commit", "verify"}},
		{name: "task_quiet set", w: Words{Git: true, Timeout: time.Minute, TaskQuiet: 45 * time.Minute}, kind: state.TaskShell,
			want: []string{"baton's reviewer looks at a background shell that writes nothing for 45 minutes, outlives"}},
		{name: "a Monitor", w: Words{Git: true, Timeout: time.Minute}, kind: state.TaskMonitor,
			want: []string{"[baton] Your monitor runs (npm test). Keep working on what does not depend on it: Claude Code wakes you with what it reports.",
				"if nothing is left to do until it reports, end your turn."},
			not: []string{"reviewer"}},
		{name: "an agent", w: Words{Git: true, Timeout: time.Minute}, kind: state.TaskSubagent,
			want: []string{"[baton] Your background work runs (npm test). Keep working on what does not depend on it: Claude Code wakes you when it ends."},
			not:  []string{"reviewer"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.w.BackgroundStarted("npm test", c.kind)
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
	// The stop's refusal says the same, as a stop; a shell's start says when baton stops it besides.
	w := Words{Git: true, Timeout: time.Minute}
	if stop, start := w.BackgroundStop("npm test"), w.BackgroundStarted("npm test", state.TaskSubagent); !strings.HasPrefix(stop, "[baton] You stopped while your background work runs (npm test). ") ||
		strings.TrimSuffix(strings.TrimPrefix(stop, "[baton] You stopped while your background work runs (npm test). "), " again.") !=
			strings.TrimSuffix(strings.TrimPrefix(start, "[baton] Your background work runs (npm test). "), ".") {
		t.Errorf("the stop and the start differ:\n%s\n%s", stop, start)
	}
	if shell, agent := w.BackgroundStarted("npm test", state.TaskShell), w.BackgroundStarted("npm test", state.TaskSubagent); shell != agent+" "+stops {
		t.Errorf("a shell's start is not an agent's with when baton stops it:\n%s\n%s", shell, agent)
	}
}

// Background work wakes the model when it ends, so nothing else need wait for it: what it is told as the
// work starts, and at a stop on it, says never to start a loop or another task only to wait for it, for a
// command, a Monitor and a phase agent's work alike.
func TestBackgroundWorkIsNeverPolled(t *testing.T) {
	const never = "Never start a loop or another background task only to wait for it."
	lead, agent, plain := Words{Git: true, Timeout: time.Minute}, Words{Git: true, Worker: true, Timeout: time.Minute}, Words{Timeout: time.Minute}
	for _, c := range []struct {
		name, got string
	}{
		{"started, the lead", lead.BackgroundStarted("npm test", state.TaskShell)},
		{"started, a phase agent", agent.BackgroundStarted("npm test", state.TaskShell)},
		{"started, without git", plain.BackgroundStarted("npm test", state.TaskShell)},
		{"an agent started", lead.BackgroundStarted("explore the parser", state.TaskSubagent)},
		{"a Monitor", lead.BackgroundStarted("tail -F app.log", state.TaskMonitor)},
		{"a stop on it", lead.BackgroundStop("npm test")},
	} {
		if strings.Count(c.got, never) != 1 {
			t.Errorf("%s: lacks %q:\n%s", c.name, never, c.got)
		}
		if wakes := strings.Index(c.got, "wakes you"); wakes < 0 || wakes > strings.Index(c.got, never) {
			t.Errorf("%s: says it before what wakes the model:\n%s", c.name, c.got)
		}
	}
}
