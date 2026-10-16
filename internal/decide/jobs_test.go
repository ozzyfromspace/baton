package decide

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says about a phase's gate and its wait on something outside, side by side.
func TestJobsOfEachKindWords(t *testing.T) {
	gate := state.Job{ID: "j64", Phase: "P13", Kind: state.JobVerify, Command: "sh qa.sh", Commit: "0123456789abcdef", Here: true, Status: state.JobRunning}
	watch := state.Job{ID: "j65", Phase: "P13", Kind: state.JobAwait, Command: "gh run watch 7 --exit-status", Status: state.JobQueued}
	for _, c := range []struct {
		name, got, want string
	}{
		{"another gate refused", Clash(state.JobVerify, "make e2e", gate),
			"not queued — P13's job j64 (`sh qa.sh` on 0123456789ab in the working tree), running, has not ended, and a phase runs one gate at a time " +
				"(one gate beside one wait on something outside: `baton verify` and `baton await` never stop each other). " +
				"`make e2e` is not the same check, so baton does not stop j64 unasked. Either:\n" +
				"- run it again with --replace: baton stops j64 and runs this one instead;\n" +
				"- fold both checks into one command, and queue that with --replace; or\n" +
				"- queue this one once j64's result comes: baton gives it to you when it ends."},
		{"a wait beside a gate", Beside(watch, gate),
			"j65 runs beside j64 (`sh qa.sh`), a gate: neither stops the other, and `baton done P13` waits for both, and is refused while either is red."},
		{"a gate beside a wait", Beside(gate, watch),
			"j64 runs beside j65 (`gh run watch 7 --exit-status`), a wait on something outside: neither stops the other, and `baton done P13` waits for both, and is refused while either is red."},
		{"the human's line", BesideShort(watch), "it runs beside j65, an await"},
		{"the human's line, beside a gate", BesideShort(gate), "it runs beside j64, a gate"},
		{"the same check again", Replaced(gate, false), "it replaces j64, the same check queued before, which baton stops."},
		{"another, asked", Replaced(gate, true), "it replaces j64 (`sh qa.sh`), which baton stops, as --replace asked."},
		{"verifying on one", VerifyingOn("P13", []state.Job{gate}),
			"baton: P13 verifying — job j64 (`sh qa.sh`) runs on; P13 is done once it passes, and reopens if it fails"},
		{"verifying on both", VerifyingOn("P13", []state.Job{gate, watch}),
			"baton: P13 verifying — jobs j64 (`sh qa.sh`) and j65 (`gh run watch 7 --exit-status`) run on; P13 is done once both pass, and reopens if either fails"},
		{"finished, verifying on both", FinishedVerifying("P13", []state.Job{gate, watch}),
			"baton: P13 is finished, and verifying: jobs j64 (`sh qa.sh` on 0123456789ab in the working tree) and j65 (`gh run watch 7 --exit-status`) are still running. " +
				"baton marks P13 done when both pass; if either fails, P13 reopens, and is taken up again from your notes on that job."},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}

	for _, c := range []struct{ name, got, prefix string }{
		{"another wait refused", Clash(state.JobAwait, "gh run watch 8", watch),
			"not queued — P13's job j65 (`gh run watch 7 --exit-status`), queued, has not ended, and a phase runs one wait on something outside at a time"},
		{"a worker's, verifying on both", WorkerDone("P13", []state.Job{gate, watch}),
			"P13 is finished, and verifying: jobs j64 (`sh qa.sh` on 0123456789ab in the working tree) and j65 (`gh run watch 7 --exit-status`) are still running. " +
				"baton lands your work on the run's branch once both pass; if either fails, P13 reopens here"},
	} {
		if !strings.HasPrefix(c.got, c.prefix) {
			t.Errorf("%s:\n got %q\nwant it to begin %q", c.name, c.got, c.prefix)
		}
	}

	red := watch
	red.Status, red.Exit = state.JobFailed, 1
	if got := RedRefusal(red); !strings.HasPrefix(got, "not done — P13's latest wait on something outside, job j65 (`gh run watch 7 --exit-status`), failed (exit 1)") ||
		!strings.Contains(got, "Fix what failed, commit, and wait on it again (`baton await`, the same command); `baton done P13` then waits for it.") {
		t.Errorf("a red wait refuses done with %q", got)
	}
}
