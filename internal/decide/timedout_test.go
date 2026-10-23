package decide

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red gate whose failures waited out a timeout says how many, what each and all of them took of the
// gate's run, and to lower the timeouts of their runners; a pass, an await and a red gate with no such
// failures say nothing of it.
func TestARedGateSaysItsFailuresWaitedOutATimeout(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	gate := func(status string, ran time.Duration) state.Job {
		return state.Job{ID: "j7", Phase: "P11", Kind: state.JobVerify, Command: "make e2e", Commit: "3f2a9c1b04de55aa", Status: status, Exit: 1,
			Started: t0, Ran: t0.Add(time.Minute), Ended: t0.Add(time.Minute + ran)}
	}
	n := 0
	tests := func(runner string, took ...time.Duration) []lane.FailedTest {
		var fs []lane.FailedTest
		for _, d := range took {
			n++
			fs = append(fs, lane.FailedTest{Runner: runner, ID: fmt.Sprintf("tests/t.spec.ts:%d", n), Took: d})
		}
		return fs
	}
	m, s := time.Minute, time.Second
	twelve := tests(lane.RunPlaywright, 3*m, 3*m, 3*m, 3*m, 3*m, 3*m, 3*m, 3*m, 3*m, 3*m, 3*m, 3*m)
	fourteen := append(append([]lane.FailedTest{}, twelve...), tests(lane.RunPlaywright, 2*s, 4*s)...)
	const fix = "A failing test should fail in seconds; lower the timeout (%s) so a red costs seconds, not minutes.\n"
	playwright := fmt.Sprintf(fix, "Playwright `timeout` and `expect.timeout`")
	for _, c := range []struct {
		name string
		job  state.Job
		log  Log
		want string // "" for nothing said
	}{
		{"twelve of fourteen", gate(state.JobFailed, 40*m), Log{Failed: fourteen, TimedOut: twelve},
			"12 of 14 failures ran about 3m each, 36 of this gate's 40 minutes: they waited out a timeout. " + playwright},
		{"more than baton read", gate(state.JobFailed, 40*m), Log{Failed: fourteen, FailedMore: 6, TimedOut: twelve},
			"12 of 20 failures ran about 3m each, 36 of this gate's 40 minutes: they waited out a timeout. "},
		{"all of them", gate(state.JobTimeout, 40*m), Log{Failed: twelve, TimedOut: twelve},
			"All 12 failures ran about 3m each, 36 of this gate's 40 minutes: they waited out a timeout. " + playwright},
		{"side by side", gate(state.JobFailed, 15*m), Log{Failed: twelve, TimedOut: twelve},
			"All 12 failures ran about 3m each, 36 minutes in all, side by side in this gate's 15: they waited out a timeout. "},
		{"a short gate, in seconds", gate(state.JobFailed, 100*s), Log{Failed: tests(lane.RunGo, 30*s, 31*s, 30*s, s), TimedOut: tests(lane.RunGo, 30*s, 31*s, 30*s)},
			"3 of 4 failures ran about 30s each, 91 of this gate's 100 seconds: they waited out a timeout. " + fmt.Sprintf(fix, "a go test's deadline")},
		{"two runners", gate(state.JobFailed, 10*m), Log{Failed: append(tests(lane.RunVitest, 61*s, 60*s), tests(lane.RunGo, 59*s)...),
			TimedOut: append(tests(lane.RunVitest, 61*s, 60*s), tests(lane.RunGo, 59*s)...)},
			"All 3 failures ran about 60s each, 3 of this gate's 10 minutes: they waited out a timeout. " + fmt.Sprintf(fix, "vitest `testTimeout`, a go test's deadline")},
		{"no failure waited", gate(state.JobFailed, 40*m), Log{Failed: fourteen}, ""},
		{"a pass", gate(state.JobPassed, 40*m), Log{TimedOut: twelve}, ""},
		{"an await", func() state.Job { j := gate(state.JobFailed, 40*m); j.Kind = state.JobAwait; return j }(), Log{Failed: twelve, TimedOut: twelve}, ""},
	} {
		got := JobResult(c.job, c.log, nil, "")
		switch {
		case c.want == "" && strings.Contains(got, "waited out a timeout"):
			t.Errorf("%s:\n%s", c.name, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("%s lacks %q:\n%s", c.name, c.want, got)
		}
	}
	// It comes after the tests that failed and the rerun of them, before the end of the log.
	got := JobResult(gate(state.JobFailed, 40*m), Log{Failed: fourteen, TimedOut: twelve, Path: "/x/j7.log", Tail: "12 failed"}, nil, "")
	if !strings.Contains(got, "which clears this gate when it passes.\n\n12 of 14 failures ran") ||
		!strings.Contains(got, "not minutes.\n\nThe end of its log (/x/j7.log):") {
		t.Errorf("the line is out of place:\n%s", got)
	}
	if d := GateRan(gate(state.JobFailed, 40*m)); d != 40*m {
		t.Errorf("GateRan %s", d)
	}
}
