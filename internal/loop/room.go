package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Whether there is room for another phase agent. With workers: auto, how many may run at once is the
// plan's to say (the phases that can go ahead) and the machine's (a ceiling the host works out from its
// CPUs and memory, config.AutoWorkers); whatever the setting, an agent starts only while the machine has
// room for one, as it reads at the step (busy), one at a time (mayLaunch), and a ramp after the one before
// (Fleet.Ramp), since a reading lags the load an agent brings. Nor does a new agent start while the
// project's lanes fall behind, a gate of the run's waiting for one (lanes), or while the workers'
// landings keep conflicting with the run's branch (conflicted): another agent would only add to either.
// What holds back a phase that could go ahead is told once (hold). The agents at work are never stopped.
// The machine's readings and limits come from spike 27 (docs/research/workers.md, "Machine readings and
// leftovers").

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

// laneBacklog is how long a gate may wait for a lane, every lane being in use, before new workers wait.
const laneBacklog = time.Minute

// maxConflicts is how many of the workers' landings in a row may conflict with the run's branch before
// new workers wait for one to land.
const maxConflicts = 2

// lanes says why the project's lanes leave no room for another worker's gates ("" if they do): a verify
// job of run st, kept in s, or of one of its workers, has waited for a lane for longer than laneBacklog.
// The workers' jobs are read without their sub-runs' locks.
func lanes(now time.Time, s *state.Store, st state.State) string {
	for _, j := range append(st.Unfinished(), s.WorkerJobs(st)...) {
		if !j.Finished() && !j.LaneWait.IsZero() && now.Sub(j.LaneWait) > laneBacklog {
			return "a gate waits for a lane"
		}
	}
	return ""
}

// conflicted says why the workers' landings leave no room for another worker ("" if they do): the last
// maxConflicts or more of them conflicted with the run's branch, and a worker of the plan still at work
// may land to end it. With none left to, a worker starts: the lead has no landing of its own to end it.
func conflicted(st state.State) string {
	if st.Conflicts < maxConflicts || planWorkers(st) == 0 {
		return ""
	}
	return fmt.Sprintf("%d landings in a row conflicted", st.Conflicts)
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

// room is what a step knows of the room for another phase agent: why the machine has none ("" if it has),
// and why the lanes have none (lanes).
type room struct {
	busy  string
	lanes string
}

// hold keeps the run's record of what holds back a phase that waits for a worker (state.Hold) in step
// with what next found: phase waits ("" for none), why no worker starts ("" if one does), and held, the
// hold on it ("" for none). A hold is told once, as it starts or changes kind (worker_deferred, and a line
// in the session), and its record goes once a worker starts or no phase waits for one.
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
