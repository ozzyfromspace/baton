//go:build !windows

package e2e

import (
	"testing"
	"time"
)

// The watchdog: the model declares a short wait and stops (20s: long enough that the stop always comes
// inside the window, or the expired wait is refused as no status); when the wait runs out with nothing else
// happening, baton types a reminder, and the model continues and reports.
func TestWatchdogRemindsAfterADeclaredWait(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := StartEnv(t, dir, []string{"BATON_IDLE_NUDGE=10s", "BATON_WAIT_GRACE=5s"}, "--model", "haiku",
		`Run exactly: baton waiting "the kettle" --until 20s — then end your turn. When baton reminds you that the wait ran out, run `+
			"`echo hello` and then `baton done P0 --notes \"kettle boiled\"`, and end your turn.")
	s.Trust()
	waitSequence(t, s, 4*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "waiting" && e["what"] == "the kettle" },
		kind("idle_nudge"),
		func(e map[string]any) bool { return e["kind"] == "turn_started" && e["by"] == "baton" },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
	)
}
