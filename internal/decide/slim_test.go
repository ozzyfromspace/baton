package decide

import (
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says of a slim run in a QA phase: queued as the red gate's rerun, run whole with --whole --why,
// and --whole alone refused.
func TestSlimWords(t *testing.T) {
	g := state.Job{ID: "j4", Phase: "P11", Command: "./qa.sh"}
	for _, c := range []struct{ got, want string }{
		{SlimRun("P11", g), "P11's red gate is `./qa.sh` (job j4): in a QA phase it reruns what failed until all of it passes. " +
			"One full run follows at `baton done` only if files beyond the failing tests changed since the full run. " +
			"To run it whole now, add `--whole --why \"…\"`: baton tells the human your reason."},
		{SlimWholeRefused("P11", g), "not queued — P11's red gate is `./qa.sh` (job j4): in a QA phase the same command reruns what failed in it until all of it passes. " +
			"--whole runs it whole instead only with `--why \"<what a whole run can show that a rerun of what failed cannot>\"`: baton tells the human your reason. " +
			"To rerun what failed, leave out --whole."},
		{SlimWholeSaid("P11", g, "the fix touches every page"), "P11 runs `./qa.sh` whole, not rerunning what failed in job j4: the fix touches every page"},
	} {
		if c.got != c.want {
			t.Errorf("got\n%s\nwant\n%s", c.got, c.want)
		}
	}
}
