package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Every agent slot at work holds back the phases that could go ahead, as a hold of its own (state.HoldCap,
// with the cap): recorded for the board and baton watch to say why ready phases wait, and logged once, but
// told in no line of the session, since it is the run going as it should. A free slot ends it, and with no
// phase left waiting, nothing is held.
func TestEveryAgentSlotAtWorkIsAHold(t *testing.T) {
	x := newCrewOn(t, 1, wide) // P1, P2 and P3 can go ahead beside the lead's P0
	x.c.Fleet.Ramp = 0         // agents start back to back here: the ramp has a test of its own
	x.atWork("P1")
	for range 3 {
		x.tick(time.Second)
	}
	h := x.state().Hold
	if got := x.spawned(); !slices.Equal(got, []string{"P1"}) || h == nil || h.Phase != "P2" || h.Kind != state.HoldCap || h.Cap != 1 || h.Why != decide.SlotsAtWork(1) {
		t.Fatalf("workers on %v; hold %+v", got, h)
	}
	if n := x.count("worker_deferred", map[string]any{"phase": "P2", "held": state.HoldCap, "cap": 1, "why": decide.SlotsAtWork(1)}); n != 1 {
		t.Errorf("logged %d times:\n%s", n, x.events())
	}
	if ann := x.announced(); strings.Contains(ann, "no worker on") {
		t.Errorf("announced %q", ann)
	}

	// Another slot: P2 gets its agent, and the hold is over; the next phase waits on the slots again.
	x.c.Fleet.Max = 2
	x.atWork("P2")
	if h := x.state().Hold; h != nil && h.Kind == state.HoldCap && h.Phase == "P2" {
		t.Fatalf("the hold outlived its phase's agent: %+v", h)
	}
	for range 3 {
		x.tick(time.Second)
	}
	if h := x.state().Hold; h == nil || h.Phase != "P3" || h.Kind != state.HoldCap || h.Cap != 2 || h.Why != "all 2 agent slots at work" {
		t.Fatalf("hold %+v", h)
	}

	// No phase left to go ahead: nothing is held.
	x.c.Fleet.Max = 3
	x.atWork("P3")
	for range 3 {
		x.tick(time.Second)
	}
	if h := x.state().Hold; h != nil {
		t.Errorf("held with no phase waiting: %+v", h)
	}
}
