package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A red gate whose log shows an environment failure and no test failure says it looks like the
// environment, quotes the line, lists the run's facts so far, and asks for the fix as a fact and in
// .baton-lane; nothing of it says to look for a bug, nor that other phases' work may be why.
func TestEnvFailedResult(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	gate := func(status string, here bool) state.Job {
		return state.Job{ID: "j7", Phase: "P3", Kind: state.JobVerify, Command: "make e2e", Commit: "3f2a9c1b04de55aa", Status: status, Exit: 1,
			Here: here, Started: t0, Ended: t0.Add(4 * time.Minute)}
	}
	const line = "Bind for 0.0.0.0:5432 failed: port is already allocated"
	facts := []state.Fact{{Text: "leave host port 5432 free: the dev stack holds it", Phase: "P2", At: t0}, {Text: "start colima before docker compose", At: t0}}
	for _, c := range []struct {
		name string
		job  state.Job
		env  Env
		want []string
		not  []string
	}{
		{"with facts", gate(state.JobFailed, false), Env{Kind: "a port already in use", Line: line, Facts: facts},
			[]string{"[baton] Job j7 (P3) failed (exit 1) after 4m: `make e2e` on 3f2a9c1b04de.",
				"This looks like the gate's environment, not the change: no test failed, and its log shows a port already in use:\n\n    " + line + "\n\n",
				"So do not look for a bug in P3's code because of it.",
				"verify again with the same command, `make e2e`: until it passes, P3's gate stands red and `baton done P3` is refused.",
				"record it with `baton env \"<what the environment needs>\"`", "every later brief in this run has it, and make it permanent in .baton-lane, which sets up every lane.",
				"The run's environment facts so far (one may be what this gate missed):\n- leave host port 5432 free: the dev stack holds it (P2)\n- start colima before docker compose (the lead)"},
			[]string{"Fix what failed", "other phases' work in progress", "No environment facts", "verify_timeout"}},
		{"none yet", gate(state.JobFailed, false), Env{Kind: "a full disk", Line: "no space left on device"},
			[]string{"its log shows a full disk:\n\n    no space left on device", "No environment facts are on record for this run yet."}, []string{"facts so far"}},
		{"timed out", gate(state.JobTimeout, false), Env{Kind: "a container that is unhealthy", Line: "container lane-db-1 is unhealthy"},
			[]string{"It ran past verify_timeout, so baton stopped it: that counts as a failure. This looks like the gate's environment"}, nil},
		{"in place", gate(state.JobFailed, true), Env{Kind: "a port already in use", Line: line},
			[]string{"so that every later brief in this run has it.\n"}, []string{".baton-lane"}},
	} {
		got := JobResult(c.job, Log{Path: "/x/j7.log", Tail: line, Env: &c.env}, nil, "P15 (QA)")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s has %q:\n%s", c.name, w, got)
			}
		}
	}

	// Without an environment failure, a red gate's result is as it was.
	if got := JobResult(gate(state.JobFailed, false), Log{Path: "/x/j7.log", Tail: "FAIL"}, nil, ""); strings.Contains(got, "environment") || !strings.Contains(got, "Fix what failed") {
		t.Errorf("no environment:\n%s", got)
	}
}
