package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Slim runs in a QA phase (v0.8.15): a real run's QA checkpoint ran its 2h40m set in full three times, about
// 8 hours, where reruns of what failed would have done. Once a QA phase's full run found every red, the same
// command reruns what failed in it until all of it passes (SlimRun); `--whole --why "…"` runs it whole, and
// the human is told (SlimWholeSaid). One more full run follows at the phase's done only if the fixes could
// have broken what passed (confirm.go).

// SlimRun tells the model, as it queues phase's red gate g's command again in a QA phase, that it is queued
// as g's rerun.
func SlimRun(phase string, g state.Job) string {
	return fmt.Sprintf("%s's red gate is `%s` (job %s): in a QA phase it reruns what failed until all of it passes. "+
		"One full run follows at `baton done` only if files beyond the failing tests changed since the full run. "+
		"To run it whole now, add `--whole --why \"…\"`: baton tells the human your reason.", phase, g.Command, g.ID)
}

// SlimWholeRefused turns down `--whole` with no `--why` for phase's red gate g in a QA phase.
func SlimWholeRefused(phase string, g state.Job) string {
	return fmt.Sprintf("not queued — %s's red gate is `%s` (job %s): in a QA phase the same command reruns what failed in it until all of it passes. "+
		"--whole runs it whole instead only with `--why \"<what a whole run can show that a rerun of what failed cannot>\"`: baton tells the human your reason. "+
		"To rerun what failed, leave out --whole.", phase, g.Command, g.ID)
}

// SlimWholeSaid tells the human that phase runs its red gate g's command whole in its QA phase (--whole),
// with the model's reason.
func SlimWholeSaid(phase string, g state.Job, why string) string {
	return fmt.Sprintf("%s runs `%s` whole, not rerunning what failed in job %s: %s", phase, g.Command, g.ID, why)
}
