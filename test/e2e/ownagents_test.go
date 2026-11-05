//go:build !windows

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// studyPlan's P0 starts a background subagent of the lead's own model, a study that outlives the phase,
// and reports P0 done at once.
const studyPlan = `# Study plan

## P0 — Start a study
Use the Agent tool with subagent_type general-purpose and run_in_background set to true, with exactly this
prompt: "Run exactly sleep 150 with the Bash tool, with its timeout set to 200000, then reply with the single word done."
Do not wait for it. As soon as it is launched, run ` + "`baton done P0 --notes \"the study runs\"`" + ` and end your turn.

## P1 — Say goodbye
Run echo goodbye, then report P1 done. If the study's result arrives, it needs nothing from you.
`

// The lead's own background subagent holds nothing baton types (v0.8.20), as a phase agent does not: P1's
// boundary compaction goes ahead, and P1 starts, while the study still runs, and the study's end
// (background_ended) comes after. Before, the gate held the compaction until the study ended or for 20
// minutes, which in a 74-phase campaign kept the lead idle through two studies the human had asked for.
func TestTheLeadsOwnSubagentHoldsNoCompaction(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, studyPlan)
	s := StartEnv(t, dir, []string{"BATON_FOREGROUND_MAX=10m"}, "--model", "haiku",
		"--allowedTools", "Bash(sleep 150)", "Bash(echo:*)", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	s.ApprovePermissions()
	waitSequence(t, s, 6*time.Minute,
		is("background_told", "via", "start", "tool", "Agent"),
		is("phase_done", "phase", "P0"),
		is("compact_queued", "reason", "boundary"),
		is("compact_started", "by_baton", "true"),
		is("phase_started", "phase", "P1"),
	)
	ev := s.Events()
	told := ev[first(ev, is("background_told", "via", "start", "tool", "Agent"))]
	task := fmt.Sprint(told["task"])
	if at := first(ev, is("background_ended", "task", task)); at >= 0 {
		t.Fatalf("the study ended before P1 started: the compaction waited on it\n%s", eventLog(s))
	}
	s.Until("the study to end", 5*time.Minute, func() bool { return first(s.Events(), is("background_ended", "task", task)) >= 0 })
	s.Until("the plan to complete", 5*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev = s.Events()
	if first(ev, is("background_ended", "task", task)) < first(ev, is("phase_started", "phase", "P1")) || first(ev, is("plan_complete")) < 0 {
		t.Fatalf("want P1 started before the study ended, and the plan complete\n%s", eventLog(s))
	}
	none(t, s, "agents_overridden", "compact_failed", "escalated")
}
