//go:build !windows

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// badTasksPlan's P0 starts a background shell that waits for a file nothing creates, and reports done at
// once; P1 starts a background computation that prints nothing for 150 seconds, declared ~3m, and works on
// through a committed pause; P2 says goodbye. Every wait over 25 seconds runs through a committed script:
// Claude Code (2.1.318) refuses a foreground command that starts with a long sleep.
const badTasksPlan = `# Background tasks plan

## P0 — Start a wait
Run exactly ` + "`sh waitfile.sh`" + ` with the Bash tool, with run_in_background set to true and the description
"Wait for ready.flag, which nothing in this plan creates". Do not wait for it. As soon as it has started, run
` + "`baton done P0 --notes \"the wait runs\"`" + ` and end your turn.

## P1 — Compute and pause
After: P0
Run exactly ` + "`sh compute.sh`" + ` with the Bash tool, with run_in_background set to true and the description
"Compute the table ~3m". Then run exactly ` + "`sh pause.sh`" + ` with the Bash tool in the foreground, with its
timeout set to 300000. Then, once the computation has printed its result, run ` + "`baton done P1 --notes \"computed\"`" + `.

## P2 — Say goodbye
After: P1
Run echo goodbye, then report P2 done.
`

// baton's reviewer (v0.8.21), on its default model: P0's wait outlives its phase, so at P1's boundary it is
// reviewed (its signals include phase_over) and stopped through baton's mod. P1's computation writes nothing
// for task_quiet (1 minute here) while it works, and is reviewed as quiet and kept. Both end, and the plan
// completes.
func TestBadBackgroundTasksAreReviewedAndOnlyTheStuckOneStopped(t *testing.T) {
	dir := NewProject(t)
	writeFile(t, dir, "waitfile.sh", "echo 'waiting for ready.flag'\nuntil [ -f ready.flag ]; do sleep 2; done\necho ready\n")
	writeFile(t, dir, "compute.sh", "end=$(( $(date +%s) + 150 ))\ni=0\nwhile [ \"$(date +%s)\" -lt \"$end\" ]; do i=$((i+1)); done\necho \"computed $i rows\"\n")
	writeFile(t, dir, "pause.sh", "sleep 200\necho paused\n")
	Git(t, dir, "add", "waitfile.sh", "compute.sh", "pause.sh")
	Git(t, dir, "commit", "-q", "-m", "test: the background tasks")
	Attach(t, dir, badTasksPlan)
	// The prompt goes first: --allowedTools takes every argument after it.
	s := StartEnv(t, dir, []string{"BATON_FOREGROUND_MAX=10m", "BATON_TASK_QUIET=1m", "BATON_TASK_REVIEW=1"}, "--model", "haiku",
		"Run the plan's phase P0 now, exactly as it says.",
		"--allowedTools", "Bash(sh waitfile.sh)", "Bash(sh compute.sh)", "Bash(sh pause.sh)", "Bash(echo:*)")
	s.Trust()
	s.ApprovePermissions()
	s.Until("both background tasks to start", 8*time.Minute, func() bool {
		ev := s.Events()
		return first(ev, is("background_told", "command", "sh waitfile.sh")) >= 0 && first(ev, is("background_told", "command", "sh compute.sh")) >= 0
	})
	ev := s.Events()
	wait := fmt.Sprint(ev[first(ev, is("background_told", "command", "sh waitfile.sh"))]["task"])
	compute := fmt.Sprint(ev[first(ev, is("background_told", "command", "sh compute.sh"))]["task"])
	s.Until("the plan to complete", 15*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev = s.Events()
	reviewed := ev[first(ev, is("task_reviewed", "task", wait))]
	if first(ev, is("task_reviewed", "task", wait, "verdict", "stop")) < 0 || first(ev, is("background_stopped", "task", wait, "via", "task_stop")) < 0 {
		t.Fatalf("P0's wait was not reviewed and stopped (%v)\n%s", reviewed, eventLog(s))
	}
	if signals := fmt.Sprint(reviewed["signals"]); !strings.Contains(signals, "phase_over") {
		t.Errorf("its signals %s, want phase_over among them", signals)
	}
	if first(ev, is("task_reviewed", "task", compute, "verdict", "keep")) < 0 || first(ev, is("background_stopped", "task", compute)) >= 0 {
		t.Fatalf("P1's computation was not reviewed and kept\n%s", eventLog(s))
	}
	if first(ev, is("background_ended", "task", wait)) < 0 || first(ev, is("background_ended", "task", compute)) < 0 {
		t.Fatalf("want both tasks ended\n%s", eventLog(s))
	}
	none(t, s, "background_stop_failed", "escalated")
}
