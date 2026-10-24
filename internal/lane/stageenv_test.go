package lane

import (
	"os"
	"path/filepath"
	"testing"
)

// Each failed stage that recorded no failure ids is read in its own part of the log: a killed build stage
// beside a red test stage is the machine's, which Environment, reading the whole log, could not say. A stage
// that failed tests by name, one that passed, a line outside every stage and baton's own lines are passed
// over.
func TestStageEnvironment(t *testing.T) {
	const (
		oom  = "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"
		port = "Bind for 0.0.0.0:5432 failed: port is already allocated"
		code = " ELIFECYCLE  Command failed with exit code 137."
	)
	log := "[baton] job j4: bin/qa-set, on 8b0ff5d in lane 1\n" +
		port + "\n" + // before any stage
		"::baton stage lint start\n" +
		"lint ok\n" +
		"::baton stage lint passed exit=0 seconds=40\n" +
		"::baton stage unit start\n" +
		"--- FAIL: TestLogin (0.20s)\n" +
		"FAIL\tgithub.com/x/y/auth\t0.3s\n" +
		"::baton stage unit failed exit=1 seconds=95\n" +
		"::baton stage check-bundle-budgets start\n" +
		"nuxt build\n" +
		"<--- Last few GCs --->\n" +
		oom + "\n" +
		code + "\n" +
		"::baton stage check-bundle-budgets failed exit=137 seconds=612\n" +
		"::baton stage e2e start\n" +
		"  ✓ shows the out of memory page (12 ms)\n" +
		"  ✘ logs in (3.1s)\n" +
		"::baton stage e2e failed exit=1 seconds=300\n" +
		"::baton stage many start\n" +
		"signal: killed\n" +
		"::baton stage many failed exit=1 seconds=30\n" +
		"::baton stage up start\n" +
		"  ::baton stage migrate start\n" +
		port + "\n" +
		"  ::baton stage migrate failed exit=1 seconds=2\n" +
		"[baton 14:02:11] signal: killed\n" +
		"::baton stage up failed exit=1 seconds=5\n" +
		"[baton 14:02:11] failed (exit 1) after 21m\n"
	path := filepath.Join(t.TempDir(), "j4.log")
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	stages := []Stage{
		{Name: "lint", Status: StagePassed},
		{Name: "unit", Status: StageFailed, Exit: 1, Failures: []string{"TestLogin"}, Runner: "go"},
		{Name: "check-bundle-budgets", Status: StageFailed, Exit: 137, Unnarrowed: UnnarrowedNoIDs},
		{Name: "e2e", Status: StageFailed, Exit: 1, Unnarrowed: UnnarrowedNoIDs},
		{Name: "many", Status: StageFailed, Exit: 1, Unnarrowed: UnnarrowedMany},
		{Name: "migrate", Status: StageFailed, Exit: 1},
		{Name: "up", Status: StageFailed, Exit: 1},
	}
	got := StageEnvironment(path, stages)
	want := map[string]EnvMatch{
		"check-bundle-budgets": {EnvKilled, oom},
		"migrate":              {"a port already in use", port},
	}
	if len(got) != len(want) {
		t.Errorf("StageEnvironment = %+v, want %+v", got, want)
	}
	for name, m := range want {
		if got[name] != m {
			t.Errorf("stage %s: %+v, want %+v", name, got[name], m)
		}
	}
	if m := Environment(path); m != (EnvMatch{}) {
		t.Errorf("Environment, over the whole log with a test failure in it: %+v", m)
	}

	for _, c := range []struct {
		name   string
		path   string
		stages []Stage
	}{
		{"no failed stage", path, stages[:1]},
		{"no stage", path, nil},
		{"no log", filepath.Join(t.TempDir(), "none.log"), stages},
	} {
		if got := StageEnvironment(c.path, c.stages); got != nil {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}
