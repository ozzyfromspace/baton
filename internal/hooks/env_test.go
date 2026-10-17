package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red gate's result reads its log for an environment failure, and lists the run's facts beside it; a
// gate that failed a test, or an await, is read as before.
func TestARedGatesResultNamesItsEnvironment(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error {
		state.AddFact(st, "start colima before docker compose", "P0", f.now)
		return nil
	})
	const port = "Bind for 0.0.0.0:5432 failed: port is already allocated"
	for _, c := range []struct {
		name, log string
		job       state.Job
		env       bool
	}{
		{"a taken port", "compose up\n" + port + "\n", state.Job{Kind: state.JobVerify, Status: state.JobFailed, Exit: 1}, true},
		{"a failing test too", port + "\n--- FAIL: TestA (0.01s)\n", state.Job{Kind: state.JobVerify, Status: state.JobFailed, Exit: 1}, false},
		{"an await", port + "\n", state.Job{Kind: state.JobAwait, Status: state.JobFailed, Exit: 1}, false},
		{"a pass", port + "\n", state.Job{Kind: state.JobVerify, Status: state.JobPassed}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			j := c.job
			j.ID, j.Phase, j.Command, j.Log = "j1", "P0", "make e2e", filepath.Join(t.TempDir(), "j1.log")
			os.WriteFile(j.Log, []byte(c.log), 0o644)
			log := JobLog(f.store, j)
			if (log.Env != nil) != c.env {
				t.Fatalf("env %+v, want one: %v", log.Env, c.env)
			}
			if !c.env {
				return
			}
			if log.Env.Kind != "a port already in use" || log.Env.Line != port || len(log.Env.Facts) != 1 {
				t.Errorf("env %+v", *log.Env)
			}
			got := decide.JobResult(j, log, nil, "")
			for _, w := range []string{"This looks like the gate's environment, not the change", port, "- start colima before docker compose (P0)"} {
				if !strings.Contains(got, w) {
					t.Errorf("the result lacks %q:\n%s", w, got)
				}
			}
		})
	}
}
