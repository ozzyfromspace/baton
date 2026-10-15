package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
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
	if n == 0 {
		x.c.Fleet = nil
	}
	return x
}

// spawned lists the phases workers took up, in order.
func (x *crew) spawned() []string { return x.spawns() }

// With workers: auto, a plan four phases wide gets a worker on each phase beside the lead's, one at a time,
// on a machine with room for them; one on a laptop with room for one; and none on a machine too small for
// any, where the lead works through every phase.
func TestAutoWorkersFollowThePlanAndTheMachine(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		name string
		m    machine.Reading
		want [][]string // the phases taken up after each second
	}{
		{"a roomy machine", roomy, [][]string{{"P1"}, {"P1", "P2"}, {"P1", "P2", "P3"}, {"P1", "P2", "P3"}}},
		{"an 8 GB, 8-core laptop", machine.Reading{CPUs: 8, MemTotal: 8 * gib, MemAvailablePct: 50, Load1: 1, Known: true}, [][]string{{"P1"}, {"P1"}, {"P1"}, {"P1"}}},
		{"a 4 GB machine", machine.Reading{CPUs: 8, MemTotal: 4 * gib, MemAvailablePct: 50, Load1: 1, Known: true}, [][]string{nil, nil, nil, nil}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := autoCrew(t, wide, c.m)
			if x.c.Fleet != nil {
				x.c.Fleet.Ramp = 0 // agents start back to back here: the ramp has a test of its own
			}
			for second, want := range c.want {
				x.tick(time.Second)
				if got := x.spawned(); len(got) != len(want) || strings.Join(got, " ") != strings.Join(want, " ") {
					t.Fatalf("after %d seconds: workers on %v, want %v", second+1, got, want)
				}
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
	if h := x.state().Hold; len(x.spawns()) != 0 || h == nil || h.Kind != state.HoldUsage || h.Phase != "P1" {
		t.Fatalf("%d workers; hold %+v", len(x.spawns()), h)
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
	x.c.Fleet.Ramp = 0  // agents start back to back here: the ramp has a test of its own
	x.tick(time.Second) // no growth yet: P1 starts
	x.machine.SwapUsed = 1<<30 + 512<<20
	x.tick(30 * time.Second) // P1 is at work; swap grew 512 MiB since the reading a minute ago
	if got := x.spawned(); len(got) != 1 {
		t.Fatalf("workers on %v", got)
	}
	if h := x.state().Hold; h == nil || h.Why != "the machine is swapping (512 MiB in the last minute)" {
		t.Fatalf("hold %+v", h)
	}
	x.tick(31 * time.Second)
	x.tick(28 * time.Second) // swap steady, but the reading a minute ago was before it grew
	if got := x.spawned(); len(got) != 1 {
		t.Fatalf("workers on %v", got)
	}
	x.tick(time.Second) // a minute of steady swap
	if got := x.spawned(); len(got) != 2 || x.state().Hold != nil {
		t.Errorf("workers on %v; hold %+v", got, x.state().Hold)
	}
}

// A suspended worker starts again as soon as the machine has room for it: the usage that holds new workers
// back does not hold it.
func TestAResumeWaitsOnlyForRoom(t *testing.T) {
	x := autoCrew(t, wide, roomy)
	x.c.Fleet.Max = 1
	x.atWork("P1")
	x.c.Ended("the baton session ended", nil)
	x.proj.Unbind("inst")

	x.c = x.coordinator("next", config.AutoWorkers(roomy))
	x.machine.Load1 = 20
	x.lead.Update(func(st *state.State) error { st.Usage = &state.Usage{UsedPct: 95}; return nil })
	x.tick(time.Second)
	x.tick(time.Minute)
	if len(x.spawns()) != 1 || x.worker("P1").Status != state.WorkerSuspended {
		t.Fatalf("resumed on a busy machine: %d sessions, P1's %+v", len(x.spawns()), x.worker("P1"))
	}
	x.machine.Load1 = 3
	x.tick(time.Second)
	if got := x.spawns(); len(got) != 2 || got[1] != "P1" || x.worker("P1").Status != state.WorkerWorking { // its agent, by a message
		t.Errorf("spawned %v; P1's worker %+v", got, x.worker("P1"))
	}
	x.tick(time.Second) // the next phase waits for room under the usage
	if h := x.state().Hold; h == nil || h.Kind != state.HoldUsage || x.worker("P1").Status != state.WorkerWorking {
		t.Errorf("new workers are held by the usage: %+v", h)
	}
}

// A new phase agent starts a ramp after the newest of the run's, with workers: auto as with a number: the
// machine's readings lag the load an agent brings (busy reads a one-minute load average). The hold is told
// once, and is over as the next agent starts.
func TestAgentsStartARampApart(t *testing.T) {
	for _, c := range []struct {
		name string
		max  int
		want []string // the phases taken up, once the ramp after P2's agent is over
	}{
		{"auto", config.AutoWorkers(roomy), []string{"P1", "P2", "P3"}},
		{"a number", 2, []string{"P1", "P2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrewOn(t, c.max, wide)
			x.tick(time.Second) // P1's agent is asked for
			x.tick(time.Second) // and is at work
			why := decide.AgentRamp("P1", AgentRamp)
			if h := x.state().Hold; len(x.spawned()) != 1 || h == nil || *h != (state.Hold{Phase: "P2", Kind: state.HoldRamp, Why: why, Since: x.now}) {
				t.Fatalf("workers on %v; hold %+v", x.spawned(), h)
			}
			x.tick(AgentRamp - time.Second)
			if got := x.spawned(); len(got) != 1 {
				t.Fatalf("workers on %v before the ramp was over", got)
			}
			x.tick(time.Second)
			if got := x.spawned(); !slices.Equal(got, []string{"P1", "P2"}) || x.state().Hold != nil {
				t.Fatalf("workers on %v; hold %+v", got, x.state().Hold)
			}
			x.tick(time.Second) // P2's agent is at work
			x.tick(AgentRamp)
			x.tick(time.Second)
			if got := x.spawned(); !slices.Equal(got, c.want) {
				t.Errorf("workers on %v, want %v", got, c.want)
			}
			if ev := x.events(); strings.Count(ev, `"held":"ramp"`) != len(c.want)-1 {
				t.Errorf("events:\n%s", ev)
			}
			if ann := x.announced(); strings.Count(ann, decide.WorkerHeld("P2", why)) != 1 {
				t.Errorf("announced %q", ann)
			}
		})
	}
	if want := "the agent on P1 started under 2 minutes ago, and phase agents start 2 minutes apart"; !strings.HasPrefix(decide.AgentRamp("P1", AgentRamp), want) {
		t.Errorf("the line: %q", decide.AgentRamp("P1", AgentRamp))
	}
}

// An agent started again for a worker it had before waits for no ramp: a fresh agent for one whose agent
// is gone, and the agents a new host resumes by a message.
func TestAResumeOrARespawnWaitsForNoRamp(t *testing.T) {
	x := newCrewOn(t, 2, wide)
	x.atWork("P1", "P2") // P2's agent has just started
	x.lead.Update(func(st *state.State) error {
		e := st.WorkerOn("P1")
		e.Status, e.Since, e.Why = state.WorkerSuspended, x.now, decide.AgentCheckpointed
		return nil
	})
	x.tick(time.Second)
	x.tick(time.Second)
	if got := x.spawned(); !slices.Equal(got, []string{"P1", "P2", "P1"}) || x.status("P1") != state.WorkerWorking {
		t.Fatalf("a fresh agent on P1 waited: workers on %v, P1's %+v", got, x.worker("P1"))
	}

	// The host ends: the next resumes both by a message, one after the other.
	x.c.Ended("the baton session ended", nil)
	x.proj.Unbind("inst")
	x.c = x.coordinator("next", 2)
	for range 4 {
		x.tick(time.Second)
	}
	if got := x.spawned(); len(got) != 5 || x.status("P1") != state.WorkerWorking || x.status("P2") != state.WorkerWorking {
		t.Errorf("workers on %v; roster %+v", got, x.state().Workers)
	}
	if ev := x.events(); strings.Count(ev, `"held":"ramp"`) != 1 { // P2's, as it first started
		t.Errorf("events:\n%s", ev)
	}
}

// A gate that has waited for a lane for over a minute, the lead's or a worker's (in a lane, or run --here
// in its worktree), holds new workers back: another worker's gates would only queue behind it. A shorter
// wait does not, nor one that is over.
func TestGatesWaitingForALaneHoldWorkersBack(t *testing.T) {
	x := newCrewOn(t, 6, wide)
	x.c.Fleet.Ramp = 0 // agents start back to back here: the ramp has a test of its own
	waits := func(s *state.Store, id string, since time.Time, status string) {
		s.Update(func(st *state.State) error {
			if j := st.Job(id); j != nil {
				j.LaneWait, j.Status = since, status
			} else {
				st.Jobs = append(st.Jobs, state.Job{ID: id, Phase: st.Current, Kind: state.JobVerify, Command: "make test", Commit: "c1", Status: status,
					Queued: since, Started: since, Heartbeat: since, LaneWait: since})
			}
			return nil
		})
	}
	held := func(phase string) {
		t.Helper()
		if h := x.state().Hold; h == nil || *h != (state.Hold{Phase: phase, Kind: state.HoldLanes, Why: "a gate waits for a lane", Since: x.now}) {
			t.Fatalf("hold %+v; workers on %v", h, x.spawned())
		}
	}

	waits(x.lead, "j1", x.now.Add(-59*time.Second), state.JobRunning)
	x.tick(time.Second) // the lead's gate has waited a minute, and no more
	if got := x.spawned(); len(got) != 1 || got[0] != "P1" || x.state().Hold != nil {
		t.Fatalf("workers on %v; hold %+v", got, x.state().Hold)
	}
	x.tick(time.Second)
	held("P2")
	waits(x.lead, "j1", time.Time{}, state.JobRunning) // it holds a lane now
	x.tick(time.Second)
	if got := x.spawned(); len(got) != 2 || got[1] != "P2" || x.state().Hold != nil {
		t.Fatalf("workers on %v; hold %+v", got, x.state().Hold)
	}

	waits(x.sub("P1"), "P1.j1", x.now.Add(-2*time.Minute), state.JobRunning) // a worker's gate
	x.tick(time.Second)
	held("P3")
	waits(x.sub("P1"), "P1.j1", x.now.Add(-2*time.Minute), state.JobInterrupted) // stopped as it waited

	// A worker's gate run --here takes a lane too (lane.Job.Here), and waits for one as a gate in a lane
	// does: the hold goes on.
	x.sub("P2").Update(func(st *state.State) error {
		since := x.now.Add(-2 * time.Minute)
		st.Jobs = append(st.Jobs, state.Job{ID: "P2.j1", Phase: "P2", Kind: state.JobVerify, Command: "make e2e", Commit: "c2", Here: true, Status: state.JobRunning,
			Queued: since, Started: since, Heartbeat: since, LaneWait: since})
		return nil
	})
	x.tick(time.Second)
	if h := x.state().Hold; len(x.spawned()) != 2 || h == nil || h.Kind != state.HoldLanes || h.Phase != "P3" {
		t.Fatalf("workers on %v; hold %+v", x.spawned(), h)
	}
	waits(x.sub("P2"), "P2.j1", time.Time{}, state.JobRunning) // it holds a lane now
	x.tick(time.Second)
	if got := x.spawned(); len(got) != 3 || got[2] != "P3" || x.state().Hold != nil {
		t.Fatalf("workers on %v; hold %+v", got, x.state().Hold)
	}
	if ev := x.events(); strings.Count(ev, `"held":"lanes"`) != 2 || !strings.Contains(ev, `"why":"a gate waits for a lane"`) {
		t.Errorf("events:\n%s", ev)
	}
	if ann := x.announced(); strings.Count(ann, decide.WorkerHeld("P2", "a gate waits for a lane")) != 1 {
		t.Errorf("announced %q", ann)
	}
}

// Two of the workers' landings in a row that conflict with the run's branch hold new workers back until
// one lands: the plan's phases touch the same files, and another worker would only add to it. A
// verification that turns red on the rebased branch is no conflict.
func TestLandingsThatKeepConflictingHoldWorkersBack(t *testing.T) {
	x := newCrewOn(t, 1, wide) // until the landings conflict
	x.c.Fleet.Ramp = 0         // agents start back to back here: the ramp has a test of its own
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	x.atWork("P1")
	x.leadCommits(map[string]string{"a.txt": "the lead's\n"})
	for i, content := range []string{"the worker's\n", "the worker's, again\n"} {
		x.finish("P1", map[string]string{"a.txt": content}, false)
		x.tick(time.Second)
		if n := x.state().Conflicts; n != i+1 || x.worker("P1").Status != state.WorkerWorking {
			t.Fatalf("after landing %d: %d in a row conflicted; P1's worker %+v\n%s", i+1, n, x.worker("P1"), x.events())
		}
	}
	x.c.Fleet.Max = 3 // P2 could start
	x.tick(time.Minute)
	if h := x.state().Hold; len(x.spawned()) != 1 || h == nil || *h != (state.Hold{Phase: "P2", Kind: state.HoldConflicts, Why: "2 landings in a row conflicted", Since: x.now}) {
		t.Fatalf("workers on %v; hold %+v", x.spawned(), h)
	}
	if !x.has("worker_deferred", `"held":"conflicts"`, `"phase":"P2"`) {
		t.Errorf("events:\n%s", x.events())
	}
	// The conflict went back to the idle agent in a message, which started it again; baton's message came
	// as it ended that turn.
	if len(x.sent) != 1 || !strings.Contains(x.sent[0].text, "Your work did not land yet") || len(x.resumes) != 1 {
		t.Errorf("sent %+v; the agent started again %d times", x.sent, len(x.resumes))
	}
	if got := x.endTurn("P1"); !strings.Contains(got, "a.txt") {
		t.Errorf("its stop gave it %q", got)
	}

	// The worker resolves it, and lands: the hold is over.
	w := x.worker("P1")
	gittest.Git(t, w.Worktree, "reset", "-q", "--soft", "main")
	gittest.Write(t, w.Worktree, "a.txt", "both\n")
	gittest.Commit(t, w.Worktree, "P1: both")
	sub := x.sub("P1")
	pl, _ := sub.LoadPlan()
	sub.Update(func(st *state.State) error {
		state.TakeMail(st)
		_, err := state.Done(st, pl, "P1", x.now, state.DoneOpts{})
		st.Run.TurnOpen = false
		return err
	})
	x.tick(workerQuiet) // the message handing the conflict back started the agent again: it is quiet once more
	x.tick(time.Second)
	if st := x.state(); x.worker("P1").Status != state.WorkerLanded || st.Conflicts != 0 || st.Hold != nil || !slices.Contains(x.spawned(), "P2") {
		t.Fatalf("after P1 landed: %d conflicts, hold %+v, workers on %v\n%s", st.Conflicts, st.Hold, x.spawned(), x.events())
	}
}
