package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red staged gate's result names each stage its environment took, by its line: the machine's, not the
// code's, with the rerun alone and room before a whole run; another kind's, with the environment to fix. A
// passing gate, an await and a gate with none say nothing of it.
func TestAStageTheMachineKilledIsSaidToBeTheMachines(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	gate := func(status string) state.Job {
		return state.Job{ID: "P15.j4", Phase: "P15", Kind: state.JobVerify, Command: "bin/qa-set", Commit: "3f2a9c1b04de55aa", Status: status, Exit: 1,
			Started: t0, Ended: t0.Add(41 * time.Minute), Stages: []lane.Stage{{Name: "unit", Status: lane.StageFailed}}}
	}
	const oom = "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"
	build := state.EnvLoss{Stage: "check-bundle-budgets", Kind: lane.EnvKilled, Line: oom}
	up := state.EnvLoss{Stage: "up", Kind: "a port already in use", Line: "Bind for 0.0.0.0:5432 failed: port is `already` allocated"}
	for _, c := range []struct {
		name string
		job  state.Job
		lost []state.EnvLoss
		want []string
		not  []string
	}{
		{"one killed", gate(state.JobFailed), []state.EnvLoss{build},
			[]string{"`check-bundle-budgets` was killed by the machine, not failed by the code: `" + oom + "`.\n" +
				"Rerun it alone with `baton verify --failed`. A whole run puts it beside the heaviest stages again, so before one, give it room: " +
				"order the gate so it runs apart, or free memory, and record what it needs with `baton env`.\n\n",
				"rerun what failed with `baton verify --failed"},
			[]string{"failed on its environment", "This looks like the gate's environment"}},
		{"two killed", gate(state.JobFailed), []state.EnvLoss{build, {Stage: "build", Kind: lane.EnvKilled, Line: "signal: killed"}},
			[]string{"`build` was killed by the machine, not failed by the code: `signal: killed`.\n",
				"Rerun them alone with `baton verify --failed`. A whole run puts them beside the heaviest stages again, so before one, give them room: " +
					"order the gate so they run apart, or free memory, and record what they need with `baton env`."}, nil},
		{"another kind beside", gate(state.JobTimeout), []state.EnvLoss{up, build},
			[]string{"`up` failed on its environment (a port already in use), not on the code: `Bind for 0.0.0.0:5432 failed: port is 'already' allocated`.\n" +
				"`check-bundle-budgets` was killed by the machine",
				"Rerun it alone with `baton verify --failed`.",
				"Fix the environment, then rerun it alone with `baton verify --failed`, and record what it needs with `baton env`.\n\n"}, nil},
		{"passed", gate(state.JobPassed), []state.EnvLoss{build}, nil, []string{"killed by the machine"}},
		{"none", gate(state.JobFailed), nil, nil, []string{"killed by the machine", "on its environment"}},
	} {
		got := JobResult(c.job, Log{Path: "/x/j4.log", Tail: "exit status 1", StageEnv: c.lost}, nil, "")
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
	await := gate(state.JobFailed)
	await.Kind = state.JobAwait
	if got := JobResult(await, Log{StageEnv: []state.EnvLoss{build}}, nil, ""); strings.Contains(got, "killed by the machine") {
		t.Errorf("an await:\n%s", got)
	}
}
