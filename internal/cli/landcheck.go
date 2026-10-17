package cli

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// `baton done <phase> --land-check "<command>"` names the focused check a phase agent's landing is
// verified again with, when other work landed on the run's branch since its gate: in its gate's place,
// which may be long (a measurement, an end-to-end suite). It runs as that gate did, in a lane or in the
// agent's worktree (--here). Only the merge queue verifies a phase again, so the lead's own phases record
// none (loop/merge.go reverify, PhaseState.LandCheck).

// landCheckOf is the land check given (given) that a done in s records: none for the lead's own phase.
func landCheckOf(s *state.Store, given string) string {
	if s.Worker() == "" {
		return ""
	}
	return strings.TrimSpace(given)
}

// recordLandCheck records cmd as phase id's land check in st, once its done went through. A done without
// one keeps the land check an earlier done of the phase gave.
func recordLandCheck(st *state.State, id, cmd string) {
	if ps := st.Phases[id]; ps != nil && cmd != "" {
		ps.LandCheck = cmd
	}
}

// reportLandCheck says what became of the land check given with the done of phase id: recorded (the
// command), or nothing for a phase of the lead's own.
func reportLandCheck(io IO, id, given, recorded string) {
	switch {
	case strings.TrimSpace(given) == "":
	case recorded != "":
		fmt.Fprintln(io.Out, decide.LandCheckRecorded(id, recorded))
	default:
		fmt.Fprintln(io.Out, decide.LandCheckLead(id))
	}
}
