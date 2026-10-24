package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A whole repeat refused while the run wraps up says what ran whole and when, then what to run instead: for a
// pass, nothing (it is the evidence); for a red staged gate, --failed; for a red gate whose failures are
// known, their own rerun; for one with none known, the tests by name. Every refusal says --why does not pass
// it and that only the human lifts it.
func TestWrapRefused(t *testing.T) {
	ended := time.Date(2026, 10, 24, 7, 29, 0, 0, time.Local)
	gate := func(status string) state.Job {
		return state.Job{ID: "P15.j1", Phase: "P15", Command: "bin/qa-set", Status: status, Ended: ended}
	}
	stopped := gate(state.JobInterrupted)
	stopped.Failing = &state.Failing{At: ended, Line: "FAIL"}
	const ran = "not queued — the human is wrapping the run up, so no gate runs whole a second time: job P15.j1 ran `bin/qa-set` whole for P15 (45m, ending at 07:29), and it "
	for _, c := range []struct {
		name string
		r    Repeat
		want []string
		not  []string
	}{
		{"it passed", Repeat{Prev: gate(state.JobPassed), Took: 45 * time.Minute},
			[]string{ran + "passed. That is P15's evidence: report P15 done on it."}, []string{"--despite-red", "Rerun"}},
		{"red, staged", Repeat{Prev: gate(state.JobFailed), Took: 45 * time.Minute, Failed: true},
			[]string{ran + "failed. Rerun only what failed instead: `baton verify --failed --notes \"…\"`", "`baton done P15 --despite-red`"}, nil},
		{"red, its failures known", Repeat{Prev: gate(state.JobFailed), Took: 45 * time.Minute,
			Failures: []lane.FailedTest{{Runner: lane.RunGo, ID: "TestA", Scope: "ex.com/m/a"}}},
			[]string{ran + "failed. Rerun only what failed in P15.j1 (TestA): `baton verify --failed --notes \"…\" -- go test"}, nil},
		{"red, nothing known", Repeat{Prev: gate(state.JobTimeout), Took: 45 * time.Minute},
			[]string{ran + "ran out of time. Rerun only the tests that failed in it, by name: a command of their own queued with --replace becomes P15's gate"}, nil},
		{"stopped at its first failure", Repeat{Prev: stopped, Took: 45 * time.Minute, Failed: true},
			[]string{ran + "was stopped after it showed a failure. Rerun only what failed instead"}, nil},
	} {
		got := WrapRefused("P15", c.r)
		for _, want := range append(c.want, " --why does not change this: only the human lifts it, with /baton wrap --off.") {
			if !strings.Contains(got, want) {
				t.Errorf("%s:\n%s\nwant %q", c.name, got, want)
			}
		}
		for _, not := range c.not {
			if strings.Contains(got, not) {
				t.Errorf("%s:\n%s\nhas %q", c.name, got, not)
			}
		}
	}
}

// What wrapping up did is said to the lead's model in full, each decision with its undo, and to the human in
// one line; a second wrap says it looked again and tells nobody.
func TestWrapSaid(t *testing.T) {
	stoppedConf := WrapSettle{Phase: "P15", Job: "P15.j9", Rerun: "P15.j8", Command: "bin/qa-set", Decision: "P15.d3"}
	owed := WrapSettle{Phase: "P7", Rerun: "P7.j6", Command: "make qa", Decision: "P7.d1"}
	repeat := WrapStop{Job: state.Job{ID: "P3.j4", Phase: "P3", Command: "make e2e"}, Whole: "P3.j2"}
	first := state.Job{ID: "P9.j1", Phase: "P9", Command: "make test"}
	for _, c := range []struct {
		name       string
		w          Wrapped
		said, line []string
		not        []string
	}{
		{"everything", Wrapped{Settled: []WrapSettle{stoppedConf, owed}, Stopped: []WrapStop{repeat}, Kept: []state.Job{first}, Told: []string{"P3", "P7"}, Lead: true},
			[]string{"baton: wrapping the run up. Every phase still runs and lands",
				"- stopped P15's confirmation run (job P15.j9, `bin/qa-set`) as the human wrapped the run up: P15 is done on its rerun P15.j8, which passed (decision P15.d3; undo: reopen P15 and run `bin/qa-set`)\n",
				"- dropped the full run of `make qa` owed to confirm P7's reruns, as the human wrapped the run up: P7 is done once its rerun P7.j6 passes (decision P7.d1; undo: reopen P7 and run `make qa`)\n",
				"- stopped job P3.j4, a whole rerun of `make e2e` for P3 (it ran whole as job P3.j2)\n",
				"- kept job P9.j1, P9's first run of `make test`\n",
				"- told you and the agents on P3 and P7: finish on the evidence you have",
				"Report this to the human. /baton wrap --off lifts it."},
			[]string{"baton: wrapping the run up → no gate runs whole a second time; stopped P15.j9 (P15's confirmation) and P3.j4; dropped P7's confirmation; told the agents on P3 and P7. /baton wrap --off lifts it"},
			[]string{"no confirmation run or whole rerun"}},
		{"nothing under way", Wrapped{Lead: true},
			[]string{"- no confirmation run or whole rerun was under way, or owed\n", "- told you: finish on the evidence you have"},
			[]string{"baton: wrapping the run up → no gate runs whole a second time. /baton wrap --off lifts it"}, nil},
		{"again", Wrapped{Again: true, Since: time.Date(2026, 10, 24, 7, 0, 0, 0, time.Local), Stopped: []WrapStop{repeat}},
			[]string{"baton: the run is wrapping up already, since 07:00; baton looked again for runs to stop.\n", "- stopped job P3.j4"},
			[]string{"baton: the run wraps up still → looked again for runs to stop; stopped P3.j4. /baton wrap --off lifts it"},
			[]string{"told", "Every phase still runs"}},
	} {
		said, line := WrapSaid(c.w), WrapLine(c.w)
		for _, want := range c.said {
			if !strings.Contains(said, want) {
				t.Errorf("%s says:\n%s\nwant %q", c.name, said, want)
			}
		}
		for _, want := range c.line {
			if line != want {
				t.Errorf("%s: the line %q, want %q", c.name, line, want)
			}
		}
		for _, not := range c.not {
			if strings.Contains(said, not) {
				t.Errorf("%s says:\n%s\nhas %q", c.name, said, not)
			}
		}
	}
}

// Only the human wraps the run up: the refusal names who started the turn.
func TestWrapTurnRefused(t *testing.T) {
	for by, want := range map[string]string{
		"":       "and no turn of theirs is open.",
		"baton":  "and baton started this turn, not the human.",
		"system": "and a notification started this turn, not the human.",
	} {
		if got := WrapTurnRefused(by); !strings.HasPrefix(got, "not wrapped — only the human wraps the run up, by typing /baton wrap in the lead's session, ") ||
			!strings.Contains(got, want) {
			t.Errorf("%q: %s", by, got)
		}
	}
}
