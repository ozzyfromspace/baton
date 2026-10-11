package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// wide is a plan four phases wide: P0 to P3 can all go ahead at once, and P4 needs them all.
var wide = plan.Plan{Version: 1, Title: "Wide", File: "/plans/wide.md", Phases: []plan.Phase{
	{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}}, {ID: "P2", Title: "two", After: []string{}},
	{ID: "P3", Title: "three", After: []string{}}, {ID: "P4", Title: "four", After: []string{"P0", "P1", "P2", "P3"}}}}

// autoCrew is a crew with workers: auto on the machine m, as the host sizes it: a Fleet up to what m
// allows, or none on a machine with no room for one.
func autoCrew(t *testing.T, pl plan.Plan, m machine.Reading) *crew {
	n := config.AutoWorkers(m)
	x := newCrewOn(t, n, pl)
	x.machine = m
	x.c.Fleet.Auto = true
	if n == 0 {
		x.c.Fleet = nil
	}
	return x
}

// spawned lists the phases workers took up, in order.
func (x *crew) spawned() []string {
	var out []string
	for _, s := range x.specs {
		out = append(out, s.Name)
	}
	return out
}

// With workers: auto, a plan four phases wide gets a worker on each phase beside the lead's, one a minute
// apart, on a machine with room for them; one on a laptop with room for one; and none on a machine too
// small for any, where the lead works through every phase.
func TestAutoWorkersFollowThePlanAndTheMachine(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		name string
		m    machine.Reading
		want [][]string // the phases taken up after each minute
	}{
		{"a roomy machine", roomy, [][]string{{"P1"}, {"P1", "P2"}, {"P1", "P2", "P3"}, {"P1", "P2", "P3"}}},
		{"an 8 GB, 8-core laptop", machine.Reading{CPUs: 8, MemTotal: 8 * gib, MemAvailablePct: 50, Load1: 1, Known: true}, [][]string{{"P1"}, {"P1"}, {"P1"}, {"P1"}}},
		{"a 4 GB machine", machine.Reading{CPUs: 8, MemTotal: 4 * gib, MemAvailablePct: 50, Load1: 1, Known: true}, [][]string{nil, nil, nil, nil}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := autoCrew(t, wide, c.m)
			x.tick(time.Second)
			for minute, want := range c.want {
				for _, p := range x.spawned() {
					x.started(p)
				}
				x.tick(59 * time.Second)
				if got := x.spawned(); len(got) != len(want) || strings.Join(got, " ") != strings.Join(want, " ") {
					t.Fatalf("after %d minutes: workers on %v, want %v", minute+1, got, want)
				}
				x.tick(time.Second)
			}
			if c.want[0] == nil && (len(x.state().Workers) != 0 || strings.Contains(x.events(), "worker_")) {
				t.Errorf("workers on a machine with no room for one: %+v\n%s", x.state().Workers, x.events())
			}
		})
	}
}

// A phase that could go ahead gets no worker while the machine has no room for another session: the hold
// is told once, as it starts, and recorded for baton status and the status line; it ends once a worker
// starts. The workers at work go on.
func TestNoRoomOnTheMachineHoldsNewWorkersBack(t *testing.T) {
	x := autoCrew(t, wide, roomy)
	x.tick(time.Second)
	x.started("P1")
	x.machine.Load1 = 15
	for range 5 {
		x.tick(time.Minute)
	}
	why := "the machine is busy (load 15.0 on 14 CPUs)"
	st := x.state()
	if got := x.spawned(); len(got) != 1 || x.worker("P1").Status != state.WorkerWorking {
		t.Fatalf("workers on %v; P1's %+v", got, x.worker("P1"))
	}
	if h := st.Hold; h == nil || *h != (state.Hold{Phase: "P2", Kind: state.HoldBusy, Why: why, Since: x.now.Add(-4 * time.Minute)}) {
		t.Errorf("hold %+v", h)
	}
	if ev := x.events(); strings.Count(ev, `"kind":"worker_deferred"`) != 1 || !strings.Contains(ev, `{"held":"busy","instance":"inst","kind":"worker_deferred","phase":"P2"`) ||
		!strings.Contains(ev, `"why":"`+why+`"`) {
		t.Errorf("events:\n%s", ev)
	}
	if ann := strings.Join(st.Run.Announce, "\n"); strings.Count(ann, decide.WorkerHeld("P2", why)) != 1 {
		t.Errorf("announced %q", ann)
	}

	// Memory runs short instead: told once more, as the hold is another.
	x.machine.Load1, x.machine.Pressure = 3, machine.PressureCritical
	x.tick(time.Minute)
	x.tick(time.Minute)
	if h := x.state().Hold; h == nil || h.Kind != state.HoldBusy || h.Why != why {
		t.Errorf("a hold of the same kind is the same hold: %+v", h)
	}
	if ev := x.events(); strings.Count(ev, `"kind":"worker_deferred"`) != 1 {
		t.Errorf("events:\n%s", ev)
	}

	// Room again: a worker starts, and the hold is over.
	x.machine.Pressure = machine.PressureNormal
	x.tick(time.Second)
	if got := x.spawned(); len(got) != 2 || got[1] != "P2" || x.state().Hold != nil {
		t.Errorf("workers on %v; hold %+v", got, x.state().Hold)
	}
	if !strings.Contains(decide.WorkerHeld("P2", why), "baton: no worker on P2 for now: "+why+". One starts once there is room") {
		t.Errorf("the line: %q", decide.WorkerHeld("P2", why))
	}
}

// The five-hour usage holds new workers back, under worker_usage_max, as a hold of its own. A hold ends
// when no phase waits for a worker any more, and in a host that runs none.
func TestAHoldEndsWhenNoPhaseWaits(t *testing.T) {
	x := newCrew(t, 3) // P1 and P2 can go ahead beside the lead's P0
	x.lead.Update(func(st *state.State) error { st.Usage = &state.Usage{UsedPct: 90}; return nil })
	x.tick(time.Second)
	if h := x.state().Hold; len(x.specs) != 0 || h == nil || h.Kind != state.HoldUsage || h.Phase != "P1" {
		t.Fatalf("%d workers; hold %+v", len(x.specs), h)
	}
	x.lead.Update(func(st *state.State) error { st.Mode = state.ModePaused; return nil })
	x.tick(time.Second)
	if h := x.state().Hold; h != nil {
		t.Errorf("held while the run is paused: %+v", h)
	}
	if ev := x.events(); strings.Count(ev, `"kind":"worker_deferred"`) != 1 || !strings.Contains(ev, `"held":"usage"`) {
		t.Errorf("events:\n%s", ev)
	}

	// A host that runs no workers holds nothing back, whatever an earlier host recorded.
	x.lead.Update(func(st *state.State) error {
		st.Mode, st.Hold = state.ModeRunning, &state.Hold{Phase: "P1", Kind: state.HoldBusy, Why: "memory pressure is critical", Since: x.now}
		return nil
	})
	x.c.Fleet = nil
	x.tick(time.Second)
	if h := x.state().Hold; h != nil {
		t.Errorf("held in a host with no workers: %+v", h)
	}
}

// On macOS at memory pressure warn, a machine whose swap grows fast has no room for another session: the
// growth is measured over the last minute of readings.
func TestSwapGrowingFastHoldsWorkersBack(t *testing.T) {
	m := roomy
	m.Pressure, m.SwapUsed = machine.PressureWarn, 1<<30
	x := autoCrew(t, wide, m)
	x.tick(time.Second) // no growth yet: P1 starts
	x.started("P1")
	x.machine.SwapUsed = 1<<30 + 512<<20
	x.tick(30 * time.Second)
	x.tick(31 * time.Second) // past the ramp, swap grew 512 MiB since the reading a minute ago
	if got := x.spawned(); len(got) != 1 {
		t.Fatalf("workers on %v", got)
	}
	if h := x.state().Hold; h == nil || h.Why != "the machine is swapping (512 MiB in the last minute)" {
		t.Fatalf("hold %+v", h)
	}
	x.tick(28 * time.Second) // swap steady, but the reading a minute ago was before it grew
	if got := x.spawned(); len(got) != 1 {
		t.Fatalf("workers on %v", got)
	}
	x.tick(time.Second) // a minute of steady swap
	if got := x.spawned(); len(got) != 2 || x.state().Hold != nil {
		t.Errorf("workers on %v; hold %+v", got, x.state().Hold)
	}
}

// A suspended worker resumes as soon as the machine has room for it: neither the ramp nor the usage that
// holds new workers back holds it.
func TestAResumeWaitsOnlyForRoom(t *testing.T) {
	x := autoCrew(t, wide, roomy)
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	_, f := x.last()
	f.exit(0)
	x.c.Ended("the baton session ended")
	x.proj.Unbind("inst-P1")
	x.proj.Unbind("inst")

	x.c = x.coordinator("next", config.AutoWorkers(roomy))
	x.c.Fleet.Auto = true
	x.machine.Load1 = 20
	x.lead.Update(func(st *state.State) error { st.Usage = &state.Usage{UsedPct: 95}; return nil })
	x.tick(time.Second)
	x.tick(time.Minute)
	if len(x.specs) != 1 || x.worker("P1").Status != state.WorkerSuspended {
		t.Fatalf("resumed on a busy machine: %d sessions, P1's %+v", len(x.specs), x.worker("P1"))
	}
	x.machine.Load1 = 3
	x.tick(time.Second)
	if spec, _ := x.last(); len(x.specs) != 2 || spec.Name != "P1" || x.worker("P1").Status != state.WorkerStarting {
		t.Errorf("%d sessions; P1's worker %+v", len(x.specs), x.worker("P1"))
	}
	if h := x.state().Hold; h == nil || h.Kind != state.HoldUsage {
		t.Errorf("new workers are held by the usage: %+v", h)
	}
}
