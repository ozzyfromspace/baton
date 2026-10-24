package lane

import (
	"strings"
	"testing"
)

// A process killed for memory is known by each phrase real tools print, and a test runner's line that only
// names a test holding those words is not.
func TestTheMemorySignature(t *testing.T) {
	for _, c := range []struct {
		line   string
		killed bool
	}{
		{"FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory", true},
		{"[ 4211.093847] Out of memory: Killed process 4242 (node) total-vm:9123456kB, anon-rss:7340032kB", true},
		{"Memory cgroup out of memory: Killed process 4242 (node)", true},
		{"[ 4211.093801] oom-kill:constraint=CONSTRAINT_NONE,nodemask=(null),task=node,pid=4242,uid=1000", true},
		{"[ 4211.093512] node invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0", true},
		{"Error: the build worker was killed with SIGKILL", true},
		{" ELIFECYCLE  Command failed with exit code 137.", true},
		{"Error: Process completed with exit code 137.", true},
		{"web-1 exited with code 137", true},
		{"signal: killed", true},
		{"\x1b[31mFATAL ERROR: JavaScript heap out of memory\x1b[0m", true},

		{"--- FAIL: TestOutOfMemoryPage (0.01s)", false},
		{"=== RUN   TestOOMKill", false},
		{"  ✓ shows the out of memory page (12 ms)", false},
		{"  × reports exit code 137 to the user", false},
		{"FAIL  src/oom.test.ts > recovers after signal: killed", false},
		{"tests/test_worker.py::test_reports[out of memory] PASSED", false},
		{"  ✘  2 [chromium] › oom.spec.ts:3:1 › shows the out of memory page (1.2s)", false},
		{"  ● Worker › restarts when it was killed with SIGKILL", false},
		{"  1) reports out of memory", false},
		{"test worker::oom ... ok   (the out of memory path)", false},
		{"Command failed with exit code 1370.", false},
		{"the broom-kill switch", false},
		{"memory use: 3.1 GiB of 8 GiB", false},
	} {
		got := EnvironmentLine(c.line)
		if (got.Kind == EnvKilled) != c.killed || !c.killed && got.Kind != "" {
			t.Errorf("EnvironmentLine(%q) = %q, killed: %v", c.line, got.Kind, c.killed)
		}
	}
}

// A log that shows a process killed for memory and no test failure is the environment's, as for any other
// signature.
func TestAKilledBuildIsTheEnvironments(t *testing.T) {
	const line = "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"
	got := environmentOf(strings.NewReader("nuxt build\n<--- Last few GCs --->\n" + line + "\n ELIFECYCLE  Command failed with exit code 134.\n"))
	if got != (EnvMatch{EnvKilled, line}) {
		t.Errorf("environmentOf = %+v", got)
	}
}
