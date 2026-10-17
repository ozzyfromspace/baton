package cli

import "testing"

// job_finished names what failed a red gate whose log shows an environment failure and no test failure,
// and nothing for a gate that failed a test, or passed.
func TestJobFinishedNamesTheEnvironment(t *testing.T) {
	const port = "Bind for 0.0.0.0:5432 failed: port is already allocated"
	for _, c := range []struct{ name, command, want string }{
		{"a taken port", "echo '" + port + "'; exit 1", "a port already in use"},
		{"a failing test beside it", "echo 'address already in use'; echo '--- FAIL: TestA (0.00s)'; exit 1", ""},
		{"a pass", "echo 'address already in use'", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, env := laneProject(t, c.command)
			if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
				t.Fatalf("lane-run: exit %d: %s", code, errs)
			}
			ev := events(t, s)["job_finished"]
			if got, _ := ev["environment"].(string); got != c.want {
				t.Errorf("job_finished %v: environment %q, want %q", ev, got, c.want)
			}
			if c.want != "" && ev["environment_line"] != port {
				t.Errorf("job_finished %v", ev)
			}
		})
	}
}
