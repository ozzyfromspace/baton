//go:build !windows

package e2e

import (
	"testing"
	"time"
)

// backgroundPlan's first phase cannot be finished before baton's reminder about the background work, so
// the model stops on that work rather than reporting the phase done.
const backgroundPlan = `# Background plan

## P0 — Outlast the reminder
Use the Bash tool with run_in_background set to true to run exactly: tail -f /dev/null
This phase is not complete until baton tells you that background work has been running for a while.
Until then there is nothing else to do: whenever you are told to keep working, end your turn again.
After that message, run echo hello, and report the phase done.

## P1 — Say goodbye
Run echo goodbye.
`

// The watchdog: the model starts background work that outlives its turn and stops on it. baton tells it
// of that work with the result of the call that started it (background_told), so the stop is allowed, not
// refused. Background work keeps reminders away only so long (20s here, an hour for real): then baton
// types one, and the model continues and reports.
func TestWatchdogLooksInOnLongBackgroundWork(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, backgroundPlan)
	s := StartEnv(t, dir, []string{"BATON_IDLE_NUDGE=10s", "BATON_BACKGROUND_IDLE=20s"}, "--model", "haiku", "Run the plan's phase P0 now.")
	s.Trust()
	waitSequence(t, s, 4*time.Minute,
		is("background_told", "via", "start", "tool", "Bash"),
		kind("idle_on_background"),
		kind("idle_nudge"),
		func(e map[string]any) bool { return e["kind"] == "turn_started" && e["by"] == "baton" },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
	)
	none(t, s, "stop_refused")
}
