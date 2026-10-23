package cli

import "testing"

// job_finished counts the failures of a red gate that waited out a timeout, and the seconds they took in
// all; a gate whose failures failed fast, and a pass, log neither.
func TestJobFinishedLogsTheFailuresThatWaitedOutATimeout(t *testing.T) {
	const waited = "echo '--- FAIL: TestA (40.00s)'; echo '--- FAIL: TestB (41.00s)'; echo '--- FAIL: TestC (40.50s)'; echo '--- FAIL: TestD (0.01s)'"
	for _, c := range []struct {
		name, command  string
		failures, secs float64
	}{
		{"three waited", waited + "; exit 1", 3, 121},
		{"they failed fast", "echo '--- FAIL: TestA (0.40s)'; echo '--- FAIL: TestB (0.41s)'; echo '--- FAIL: TestC (0.40s)'; exit 1", 0, 0},
		{"a pass", waited, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, env := laneProject(t, c.command)
			if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
				t.Fatalf("lane-run: exit %d: %s", code, errs)
			}
			ev := events(t, s)["job_finished"]
			n, _ := ev["timeout_failures"].(float64)
			secs, _ := ev["timeout_seconds"].(float64)
			if n != c.failures || secs != c.secs {
				t.Errorf("job_finished %v: timeout_failures %v, timeout_seconds %v; want %v, %v", ev, n, secs, c.failures, c.secs)
			}
		})
	}
}
