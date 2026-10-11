package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Whether there is room for another worker's session. With workers: auto, how many may run at once is
// the plan's to say (the phases that can go ahead) and the machine's (a ceiling the host works out from
// its CPUs and memory, config.AutoWorkers); whatever the setting, a worker starts or resumes only while
// the machine has room for one, as it reads at the step (busy), and new workers start one Ramp apart, so
// that each one's load shows before the next starts. What holds back a phase that could go ahead is told
// once (hold). The workers at work are never stopped. The readings and the limits come from spike 27
// (docs/research/workers.md, "Machine readings and leftovers").

// DefaultRamp is how long after a worker's session started the next new one may, with workers: auto.
const DefaultRamp = time.Minute

const (
	// memFloor is the least share of memory, in percent, Linux must have available for another session.
	memFloor = 15
	// swapGrowth is the most swap may have grown over the last minute for another session while macOS
	// reports memory pressure at warn.
	swapGrowth = 256 << 20
)

// busy says why the machine m leaves no room for another session ("" if it has any), with its swap grown
// by grew over the last minute. Its load must stay under its CPUs. For memory, on macOS (m.Pressure set)
// the kernel's pressure must be under critical, and at warn swap must not be growing fast: macOS holds its
// free share near 36% by swapping, so that share says little. On Linux, at least memFloor percent must be
// available. A machine with no live readings has room.
func busy(m machine.Reading, grew uint64) string {
	switch {
	case !m.Known:
		return ""
	case m.Load1 >= float64(max(m.CPUs, 1)):
		return fmt.Sprintf("the machine is busy (load %.1f on %d CPUs)", m.Load1, m.CPUs)
	case m.Pressure >= machine.PressureCritical:
		return "memory pressure is critical"
	case m.Pressure == machine.PressureWarn && grew > swapGrowth:
		return fmt.Sprintf("the machine is swapping (%s in the last minute)", bytesShown(grew))
	case m.Pressure == 0 && m.MemAvailablePct < memFloor:
		return fmt.Sprintf("memory is low (%d%% free)", m.MemAvailablePct)
	}
	return ""
}

// bytesShown is a size in GiB to a tenth, or under a GiB in MiB.
func bytesShown(b uint64) string {
	if b < 1<<30 {
		return fmt.Sprintf("%d MiB", b>>20)
	}
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}

// swapAt is the machine's swap in use, as a step read it.
type swapAt struct {
	at   time.Time
	used uint64
}

// look reads the machine for a step (Fleet.Machine), and says whether it has room for another session
// (Coordinator.noRoom). It keeps the last minute of swap readings, and the newest one at least a minute
// old: how fast swap grows tells a machine short of memory on macOS.
func (c *Coordinator) look(now time.Time) {
	read := c.Fleet.Machine
	if read == nil {
		read = machine.Read
	}
	m := read()
	c.swaps = append(c.swaps, swapAt{now, m.SwapUsed})
	for len(c.swaps) > 1 && now.Sub(c.swaps[1].at) >= time.Minute {
		c.swaps = c.swaps[1:]
	}
	var grew uint64
	if m.SwapUsed > c.swaps[0].used {
		grew = m.SwapUsed - c.swaps[0].used
	}
	was := c.noRoom
	if c.noRoom = busy(m, grew); c.noRoom != was {
		if c.noRoom == "" {
			c.Lead.logf("loop: workers: the machine has room again")
		} else {
			c.Lead.logf("loop: workers: no room on the machine: %s", c.noRoom)
		}
	}
}

// room is what a step knows of the room for another worker: why the machine has none ("" if it has), and
// when this host last started a worker's session.
type room struct {
	busy    string
	started time.Time
}

// ramp is how long after a worker's session started the next new one may, with auto.
func (f *Fleet) ramp() time.Duration {
	if f.Ramp > 0 {
		return f.Ramp
	}
	return DefaultRamp
}

// hold keeps the run's record of what holds back a phase that waits for a worker (state.Hold) in step
// with what next found: phase waits ("" for none), why no worker starts ("" if one does), and held, the
// hold on it ("" for none). A hold is told once, as it starts or changes kind (worker_deferred, and a line
// in the session), and its record goes once a worker starts or no phase waits for one. The ramp between
// two workers holds nothing back, and ends nothing.
func (c *Coordinator) hold(now time.Time, s *state.Store, st state.State, phase, why, held string) {
	cur := st.Hold
	switch {
	case held != "" && (cur == nil || cur.Kind != held):
		s.Update(func(st *state.State) error {
			st.Hold = &state.Hold{Phase: phase, Kind: held, Why: why, Since: now}
			return nil
		})
		c.Lead.logf("loop: %s: no worker for now: %s", phase, why)
		s.Event("worker_deferred", map[string]any{"phase": phase, "why": why, "held": held})
		c.announce(s, decide.WorkerHeld(phase, why))
	case held != "" && cur.Phase != phase:
		s.Update(func(st *state.State) error {
			if st.Hold != nil {
				st.Hold.Phase = phase
			}
			return nil
		})
	case held == "" && cur != nil && (phase == "" || why == ""):
		s.Update(func(st *state.State) error { st.Hold = nil; return nil })
	}
}
