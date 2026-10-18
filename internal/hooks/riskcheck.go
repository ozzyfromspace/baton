package hooks

import (
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// countCheck records a check command (lane.IsCheck: a test suite, a lint, a type-check, a measurement) the
// model's shell ran in the phase in hand, passing or not, for the risk ledger: a phase that ran no check at
// all, and queued no job, scores for it (package risk). It is counted where the phase is worked, the lead's
// run or a phase agent's sub-run, and only while a phase is in hand.
func countCheck(st *state.State, tool string, in map[string]any) {
	if tool != "Bash" && tool != "PowerShell" || !st.InHand() {
		return
	}
	ti, _ := in["tool_input"].(map[string]any)
	if cmd := str(ti, "command"); cmd != "" && lane.IsCheck(cmd) {
		state.CountCheck(st, st.Current)
	}
}
