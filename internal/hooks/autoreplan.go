package hooks

import (
	"fmt"
	"os"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/replan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton opens replans of its own at the lead's phase boundary (package replan's Auto): after an upgrade
// whose planning rules changed, at the start of a QA phase over a chain, and once a phase marked `Replan:
// after` is done. Its brief comes before the brief of the phase the lead begins, in the same context, so the
// lead replans from a fresh context, runs `baton replanned`, then begins the phase.

// autoReplan opens the replan the lead's run owes at the phase boundary whose brief b is given now, with
// auto_replan on, and returns its brief, which comes before the phase's: "" when none opens, for a phase
// agent's brief, or a brief at no phase boundary. It fails open: a panic in it costs the replan, never the
// phase's brief.
func (h *handlers) autoReplan(c Context, s *state.Store, b state.Briefing) (text string) {
	if s.Worker() != "" || c.Agent != "" || !replan.AtBoundary(b.Kind) || !config.AutoReplansFromEnv(c.Env) {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "baton: the replan at %s's boundary failed open after a panic: %v\n", b.Phase, r)
			text = ""
		}
	}()
	autoReplanning()
	o, ok, err := replan.Auto(s, b.Phase, c.Now, replan.MaxFrom(c.Env))
	if err != nil || !ok {
		return ""
	}
	return o.Brief
}

// autoReplanning runs as autoReplan starts (tests: a panic there).
var autoReplanning = func() {}
