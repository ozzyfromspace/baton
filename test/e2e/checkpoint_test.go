//go:build !windows

package e2e

import (
	"testing"
	"time"
)

const longPhasePlan = `# Long plan

## P0 — Count to twelve
This phase is long. Run ` + "`echo step N`" + ` for every N from 1 to 12, one Bash call per step, in order.

## P1 — Wrap up
Run ` + "`echo done`" + `.
`

// Mid-phase valve. The nudge is asserted as an event (it is emitted by a hook, deterministically); whether
// a model checkpoints immediately or finishes a short phase first is its judgment, so the checkpoint
// itself is requested explicitly here: baton compacts, briefs the same phase, and wakes the model.
func TestCheckpointCompactsAndContinuesThePhase(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, longPhasePlan)
	s := StartEnv(t, dir, []string{"BATON_CHECKPOINT_PCT=5"}, "--model", "haiku",
		"Run `echo step 1` with the Bash tool. Then run `baton checkpoint --notes \"step 1 done; next is step 2\"` and end your turn. After that, follow baton's instructions.")
	s.Trust()
	waitSequence(t, s, 4*time.Minute,
		kind("context_nudge"),
		kind("checkpoint"),
		func(e map[string]any) bool { return e["kind"] == "compact_queued" && e["reason"] == "checkpoint" },
		func(e map[string]any) bool { return e["kind"] == "compact_started" && e["by_baton"] == true },
		func(e map[string]any) bool {
			return e["kind"] == "brief" && e["type"] == "checkpoint" && e["phase"] == "P0"
		},
		kind("rewake"),
		func(e map[string]any) bool { return e["kind"] == "turn_started" && e["by"] == "baton" },
	)
}
