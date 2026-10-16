package loop

import (
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/replan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A replan of the phases not started (package replan) holds new phase agents while it is open (Fleet.next),
// so it never stays open for long: one open for Timing.ReplanMax lapses, running or paused. Its draft is
// dropped, the plan goes on as it was, and the human is told. The clock starts over after the machine
// sleeps, as every wait of the loop's does.

// lapseReplan lapses the replan open on the lead's run st once it has been open Timing.ReplanMax.
func (l *Loop) lapseReplan(v host.View, st state.State) {
	r := st.Replan
	if r == nil || l.Timing.ReplanMax <= 0 || v.Now.Before(l.since(r.Since).Add(l.Timing.ReplanMax)) {
		return
	}
	if replan.Lapse(l.Store, r.N, v.Now, l.Timing.ReplanMax) {
		l.logf("loop: replan %d lapsed after %s; the plan goes on as it was", r.N, l.Timing.ReplanMax)
	}
}
