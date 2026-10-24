package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red staged gate's result reads each failed stage's part of its log: a build the machine killed beside a
// test stage that failed on its own is named as the machine's, which the whole log, with a test failure in
// it, could not say. Where no test failed, the stage is named, not the log as a whole, whose result would
// ask for a whole run again. A gate that passed says nothing of it.
func TestARedStagedGatesResultNamesTheStageTheMachineKilled(t *testing.T) {
	const oom = "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"
	killed := "::baton stage check-bundle-budgets start\nnuxt build\n" + oom + "\n::baton stage check-bundle-budgets failed exit=134 seconds=612\n"
	build := lane.Stage{Name: "check-bundle-budgets", Status: lane.StageFailed, Exit: 134, Unnarrowed: lane.UnnarrowedNoIDs}
	for _, c := range []struct {
		name, log string
		unit      lane.Stage
	}{
		{"beside a red test stage", "::baton stage unit start\n--- FAIL: TestLogin (0.20s)\n::baton stage unit failed exit=1 seconds=95\n" + killed,
			lane.Stage{Name: "unit", Status: lane.StageFailed, Exit: 1, Failures: []string{"TestLogin"}, Runner: "go"}},
		{"alone", "::baton stage unit start\nok  \tgithub.com/x/y\t0.3s\n::baton stage unit passed exit=0 seconds=95\n" + killed,
			lane.Stage{Name: "unit", Status: lane.StagePassed}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			j := state.Job{ID: "j4", Phase: "P0", Kind: state.JobVerify, Command: "bin/qa-set", Status: state.JobFailed, Exit: 1}
			path := f.store.JobLog(j.ID)
			os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, []byte(c.log), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, s := range []lane.Stage{c.unit, build} {
				if err := lane.AppendStage(f.store.JobStages(j.ID), s); err != nil {
					t.Fatal(err)
				}
			}
			got := JobLog(f.store, j)
			if got.Env != nil || len(got.StageEnv) != 1 || got.StageEnv[0] != (state.EnvLoss{Stage: "check-bundle-budgets", Kind: lane.EnvKilled, Line: oom}) {
				t.Fatalf("env %+v, stages %+v", got.Env, got.StageEnv)
			}
			r := decide.JobResult(j, got, nil, "")
			if !strings.Contains(r, "`check-bundle-budgets` was killed by the machine, not failed by the code: `"+oom+"`.") ||
				strings.Contains(r, "This looks like the gate's environment") {
				t.Errorf("the result:\n%s", r)
			}

			j.Status = state.JobPassed
			if got := JobLog(f.store, j); got.Env != nil || got.StageEnv != nil {
				t.Errorf("a pass: %+v, %+v", got.Env, got.StageEnv)
			}
		})
	}
}
