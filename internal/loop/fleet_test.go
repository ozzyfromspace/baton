package loop

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent starts on a phase that can go ahead beside the lead's, while a slot is free: its worker
// on the roster and its sub-run first, then the mod is asked to start it. The worktree hook makes its
// worktree outside the checkout, on a branch made from the run's, and links it; once the mod acks the
// spawn, it is at work. One starts at a time.
func TestAPhaseAgentTakesUpAPhaseThatCanStart(t *testing.T) {
	x := newCrew(t, 2)
	x.modOff = true
	x.tick(time.Second)
	w := x.worker("P1")
	st := x.state()
	if w.Status != state.WorkerStarting || st.Phases["P1"].Status != state.PhaseWorking || st.Phases["P1"].Worker != "sess-1/P1" {
		t.Fatalf("roster %+v; P1 %+v", st.Workers, st.Phases["P1"])
	}
	real, _ := filepath.EvalSymlinks(x.home)
	if want := filepath.Join(real, "worktrees", lane.Key(x.repo), "sess-1", "P1"); w.Worktree != want || w.Branch != "baton/sess-1/P1" || w.Base != gitx.Head(x.repo) {
		t.Errorf("worktree %q (want %q), branch %q, base %s", w.Worktree, want, w.Branch, w.Base)
	}
	cmds, _ := x.bridge().Commands()
	if len(cmds) != 1 || cmds[0].Kind != bridge.Spawn || cmds[0].Phase != "P1" || cmds[0].Name != "P1" || cmds[0].Instance != "inst" ||
		cmds[0].Description != decide.AgentDescription("P1", "one") || cmds[0].Prompt != decide.AgentBegin("P1") {
		t.Errorf("commands %+v", cmds)
	}
	sub := x.sub("P1")
	sst, _ := sub.Load()
	spl, _ := sub.LoadPlan()
	if sst.Current != "P1" || sst.IDPrefix != "P1." || sst.Mode != state.ModeRunning || len(spl.Phases) != 1 || spl.Phases[0].ID != "P1" || sub.Root != w.Worktree {
		t.Errorf("the sub-run: %+v, plan %+v, root %s", sst, spl.Phases, sub.Root)
	}
	if ev := x.events(); !strings.Contains(ev, `"kind":"worker_spawned"`) || !strings.Contains(ev, `"branch":"baton/sess-1/P1"`) {
		t.Errorf("events:\n%s", ev)
	}
	if ann := strings.Join(st.Run.Announce, "\n"); !strings.Contains(ann, "baton: a phase agent takes up P1 (one), in a worktree of its own on the branch baton/sess-1/P1") {
		t.Errorf("announced %q", ann)
	}

	// While its spawn is open, no other starts.
	x.tick(time.Second)
	if got := x.spawns(); !slices.Equal(got, []string{"P1"}) {
		t.Fatalf("spawned %v while P1's was open", got)
	}

	// The mod starts it: the worktree hook places it, its brief starts its phase, and its ack puts it to
	// work. The next one starts.
	x.modOff = false
	x.runMod()
	a := x.agent("P1")
	if a.tree != w.Worktree || strings.HasPrefix(a.tree, x.repo) || gitx.Branch(a.tree) != w.Branch || gitx.Head(a.tree) != w.Base {
		t.Errorf("the agent works in %s, on %q at %s", a.tree, gitx.Branch(a.tree), gitx.Head(a.tree))
	}
	if sst, _ := sub.Load(); sst.Phases["P1"].Status != state.PhaseActive || sst.Run.Briefed != a.id {
		t.Errorf("not briefed: %+v", sst.Run)
	}
	x.tick(time.Second)
	if w := x.worker("P1"); w.Status != state.WorkerWorking || w.Started.IsZero() || w.AgentID != a.id || !slices.Contains(x.tracked(), a.id) {
		t.Errorf("P1's worker %+v; baton's agents %v", w, x.tracked())
	}
	if ev := x.events(); !strings.Contains(ev, `"kind":"worker_started"`) || !strings.Contains(strings.Join(x.state().Run.Announce, "\n"), "the phase agent on P1 is at work") {
		t.Errorf("not announced: %s", ev)
	}
	if got := x.spawns(); !slices.Equal(got, []string{"P1", "P2"}) {
		t.Errorf("spawned %v", got)
	}
}

func TestWhenAWorkerStarts(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	pl := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0", After: []string{}}, {ID: "P1", After: []string{}}, {ID: "P2", After: []string{}}, {ID: "P3", After: []string{"P1"}}}}
	chain := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0"}, {ID: "P1"}}}
	on := func(st *state.State, phase, status string) {
		st.Phases[phase] = &state.PhaseState{Status: state.PhaseWorking, Worker: "r/" + phase}
		st.Workers = append(st.Workers, state.Worker{Phase: phase, Ref: "r/" + phase, Status: status, Generation: st.Generation})
	}
	const busyWhy, laneWhy = "the machine is busy (load 15.0 on 14 CPUs)", "a gate waits for a lane"
	for _, c := range []struct {
		name  string
		f     Fleet
		room  room
		pl    plan.Plan
		st    func(*state.State)
		phase string // the phase that waits for a worker, or gets one
		why   string // why it gets none
		held  string // what holds it back
	}{
		{"a free slot, and a phase that can start beside the lead's", Fleet{Max: 1}, room{}, pl, nil, "P1", "", ""},
		{"never the lead's phase", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Phases["P0"].Status, st.Current = state.PhaseDone, "P1"
			st.Phases["P1"].Status = state.PhaseActive
		}, "P2", "", ""},
		{"nor the phase the idle lead takes up next", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Phases["P0"].Status, st.Current = state.PhaseDone, ""
		}, "P2", "", ""},
		{"every slot taken", Fleet{Max: 1}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "", "1 of 1 workers run already", ""},
		{"a suspended worker keeps its slot", Fleet{Max: 1}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerSuspended) }, "", "1 of 1 workers run already", ""},
		{"a worker of a replaced plan keeps none", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Generation++
		}, "P2", "", ""},
		{"never two workers on one phase", Fleet{Max: 2}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "P2", "", ""},
		{"a failed worker frees its slot", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Workers = append(st.Workers, state.Worker{Phase: "P1", Ref: "r/P1", Status: state.WorkerFailed})
		}, "P1", "", ""},
		{"a phase waits for one a worker has not landed", Fleet{Max: 3}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			on(st, "P2", state.WorkerLanding)
		}, "", "no other phase can start", ""},
		{"a chain", Fleet{Max: 1}, room{}, chain, nil, "", "no other phase can start", ""},
		{"paused", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.Mode = state.ModePaused }, "", "the run is paused", ""},
		{"blocked by the lead", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.Blocked = &state.Block{Reason: "x"} }, "", "the run is blocked", ""},
		{"a review due", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.ReviewDue = "P0" }, "", "the run waits on a review of its decisions", ""},
		{"attached on a detached HEAD", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.RunBranch = "" }, "", "the plan was attached with no branch checked out", ""},
		{"usage over the max", Fleet{Max: 1, UsageMax: 80}, room{}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85, Resets: now.Add(time.Hour)}
		}, "P1", "85% of the five-hour usage is used (worker_usage_max 80%)", state.HoldUsage},
		{"usage over the max, past its reset", Fleet{Max: 1, UsageMax: 80}, room{}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85, Resets: now.Add(-time.Minute)}
		}, "P1", "", ""},
		{"usage under the max", Fleet{Max: 1, UsageMax: 80}, room{}, pl, func(st *state.State) { st.Usage = &state.Usage{UsedPct: 79} }, "P1", "", ""},
		{"no usage check, the default", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.Usage = &state.Usage{UsedPct: 99} }, "P1", "", ""},
		{"a session waits on a usage limit", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Usage = &state.Usage{Limited: now.Add(time.Minute)}
		}, "", "a session waits on a usage limit", ""},

		// The machine.
		{"no room on the machine", Fleet{Max: 6}, room{busy: busyWhy}, pl, nil, "P1", busyWhy, state.HoldBusy},
		{"no room on the machine, with a number", Fleet{Max: 3}, room{busy: "memory is low (9% free)"}, pl, nil, "P1", "memory is low (9% free)", state.HoldBusy},
		{"the machine before the usage", Fleet{Max: 1, UsageMax: 80}, room{busy: busyWhy}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85}
		}, "P1", busyWhy, state.HoldBusy},
		{"no room, and no phase waits", Fleet{Max: 1}, room{busy: busyWhy}, chain, nil, "", "no other phase can start", ""},
		{"no room, and every slot taken", Fleet{Max: 1}, room{busy: busyWhy}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "", "1 of 1 workers run already", ""},

		// The lanes, and the landings.
		{"a gate waits for a lane", Fleet{Max: 6}, room{lanes: laneWhy}, pl, nil, "P1", laneWhy, state.HoldLanes},
		{"the machine before the lanes", Fleet{Max: 6}, room{busy: busyWhy, lanes: laneWhy}, pl, nil, "P1", busyWhy, state.HoldBusy},
		{"the lanes before the usage", Fleet{Max: 6, UsageMax: 80}, room{lanes: laneWhy}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85}
		}, "P1", laneWhy, state.HoldLanes},
		{"two landings in a row conflicted", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 2
		}, "P2", "2 landings in a row conflicted", state.HoldConflicts},
		{"three", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 3
		}, "P2", "3 landings in a row conflicted", state.HoldConflicts},
		{"one conflicted", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 1
		}, "P2", "", ""},
		{"conflicts, and no worker left to land", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			st.Workers = append(st.Workers, state.Worker{Phase: "P1", Ref: "r/P1", Status: state.WorkerFailed})
			st.Conflicts = 2
		}, "P1", "", ""},
		{"conflicts, and only a replaced plan's worker left", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Generation++
			st.Conflicts = 2
		}, "P2", "", ""},
		{"the lanes before the conflicts", Fleet{Max: 6}, room{lanes: laneWhy}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 2
		}, "P2", laneWhy, state.HoldLanes},
		{"the conflicts before the usage", Fleet{Max: 6, UsageMax: 80}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts, st.Usage = 2, &state.Usage{UsedPct: 85}
		}, "P2", "2 landings in a row conflicted", state.HoldConflicts},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := state.Attach(c.pl, now, state.Origin{})
			st.RunBranch = "main"
			if c.st != nil {
				c.st(&st)
			}
			phase, why, held := c.f.next(st, c.pl, now, c.room)
			if phase != c.phase || why != c.why || held != c.held {
				t.Errorf("next = %q, %q, %q; want %q, %q, %q", phase, why, held, c.phase, c.why, c.held)
			}
		})
	}
}

// The machine has room for another session while its load is under its CPUs and its memory is not short:
// on macOS by the kernel's pressure and how fast swap grows, never by its free share, which macOS holds
// near 36% by swapping; on Linux by what is available. With no live readings, it has room.
func TestRoomOnTheMachine(t *testing.T) {
	mac := roomy
	linux := machine.Reading{CPUs: 8, MemTotal: 16 << 30, MemAvailablePct: 40, Load1: 2, Known: true}
	with := func(m machine.Reading, fn func(*machine.Reading)) machine.Reading { fn(&m); return m }
	for _, c := range []struct {
		name string
		m    machine.Reading
		grew uint64
		want string
	}{
		{"a roomy Mac", mac, 0, ""},
		{"a roomy Linux machine", linux, 0, ""},
		{"no live readings", machine.Reading{CPUs: 8, Load1: 99}, 0, ""},
		{"a load just under the CPUs", with(linux, func(m *machine.Reading) { m.Load1 = 7.9 }), 0, ""},
		{"a load at the CPUs", with(linux, func(m *machine.Reading) { m.Load1 = 8 }), 0, "the machine is busy (load 8.0 on 8 CPUs)"},
		{"a busy Mac", with(mac, func(m *machine.Reading) { m.Load1 = 15.04 }), 0, "the machine is busy (load 15.0 on 14 CPUs)"},
		{"macOS's free share is no guide", with(mac, func(m *machine.Reading) { m.MemAvailablePct = 5 }), 0, ""},
		{"memory pressure critical", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureCritical }), 0, "memory pressure is critical"},
		{"pressure at warn, swap steady", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureWarn }), 256 << 20, ""},
		{"pressure at warn, swapping", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureWarn }), 1288 << 20,
			"the machine is swapping (1.3 GiB in the last minute)"},
		{"pressure at warn, swapping a little", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureWarn }), 300 << 20,
			"the machine is swapping (300 MiB in the last minute)"},
		{"swapping at normal pressure", mac, 2 << 30, ""},
		{"Linux at the floor", with(linux, func(m *machine.Reading) { m.MemAvailablePct = 15 }), 0, ""},
		{"Linux under it", with(linux, func(m *machine.Reading) { m.MemAvailablePct = 14 }), 0, "memory is low (14% free)"},
	} {
		if got := busy(c.m, c.grew); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// No phase agent starts before the lead's session has.
func TestNoWorkerStartsBeforeTheLead(t *testing.T) {
	x := newCrew(t, 1)
	x.leadIdle = true
	x.lead.Update(func(st *state.State) error { st.Run.LastActivity = x.now.Add(-time.Hour); return nil })
	x.tick(time.Second)
	x.tick(time.Minute)
	if len(x.spawns()) != 0 {
		t.Fatal("a phase agent started before the lead's session")
	}
	x.leadIdle = false
	x.tick(time.Second)
	if len(x.spawns()) != 1 {
		t.Errorf("%d spawns once the lead started", len(x.spawns()))
	}
}

// A spawn that starts no agent in its phase's worktree is taken back, and its worker retired; its phase
// goes back to the scheduler, and an agent takes it up a while later. One the mod never runs is voided
// after Timing.AgentStart, so that its worktree would be refused; one that started somewhere else is
// stopped, and is none of baton's agents any more.
func TestASpawnThatStartsNoAgentIsTakenBack(t *testing.T) {
	never := "its agent did not start: the mod did not start it within " + DefaultTiming.AgentStart.String()
	for _, c := range []struct {
		name string
		set  func(x *crew)
		why  string
	}{
		{"the mod never runs it", func(x *crew) { x.modOff = true }, never},
		{"the Agent call fails", func(x *crew) { x.spawnErr = "the agent type is unknown" }, "its agent did not start: the agent type is unknown"},
		{"the agent starts somewhere else", func(x *crew) { x.unplaced = true }, "its agent started outside its phase's worktree"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			c.set(x)
			x.tick(time.Second)
			if c.why == never {
				x.tick(DefaultTiming.AgentStart)
				if x.worker("P1").Status != state.WorkerStarting {
					t.Fatal("taken back before its time")
				}
			}
			x.tick(time.Second)
			x.tick(time.Second)
			st := x.state()
			w := st.Workers[0]
			if w.Status != state.WorkerFailed || w.Why != c.why || st.Phases["P1"].Status != state.PhasePending || isDir(w.Worktree) ||
				gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") != "" {
				t.Fatalf("worker %+v; P1 %+v; worktree there %v", w, st.Phases["P1"], isDir(w.Worktree))
			}
			if cmds, _ := x.bridge().Commands(); len(cmds) != 0 || len(x.tracked()) != 0 {
				t.Errorf("commands %+v; baton's agents %v", cmds, x.tracked())
			}
			if x.unplaced && (len(x.stopped) != 1 || x.stopped[0] != x.agent("P1").id) {
				t.Errorf("stopped %v", x.stopped)
			}
			retry := x.now.Add(launchRetry - 2*time.Second).Local().Format("15:04")
			if ann := strings.Join(st.Run.Announce, "\n"); !strings.Contains(ann, "baton: no phase agent on P1 for now ("+c.why+") → baton tries again at "+retry) {
				t.Errorf("announced %q", ann)
			}
			if !strings.Contains(x.events(), `"kind":"worker_ended"`) {
				t.Errorf("events:\n%s", x.events())
			}
			// A new agent takes the phase up a while later.
			x.modOff, x.spawnErr, x.unplaced = false, "", false
			x.tick(time.Minute)
			if len(x.spawns()) != 1 {
				t.Fatal("a new agent started at once")
			}
			x.tick(launchRetry)
			x.tick(time.Second)
			if got := x.spawns(); len(got) != 2 || x.worker("P1").Status != state.WorkerWorking {
				t.Errorf("spawned %v; roster %+v", got, x.state().Workers)
			}
		})
	}
}

// A dropped run's phase agents are stopped, and their workers retired. Each one's uncommitted work is
// saved and its worktree removed, and a branch that holds commits that never landed is kept.
func TestDroppingTheRunEndsItsWorkers(t *testing.T) {
	x := newCrew(t, 2)
	x.atWork("P1", "P2")
	p1, p2 := x.worker("P1"), x.worker("P2")
	gittest.Write(t, p1.Worktree, "one.txt", "committed\n")
	gittest.Commit(t, p1.Worktree, "P1's work")
	gittest.Write(t, p2.Worktree, "two.txt", "uncommitted\n")
	x.lead.Update(func(st *state.State) error { return state.Detach(st) })
	x.tick(time.Second)
	if !slices.Equal(x.stopped, []string{p1.AgentID, p2.AgentID}) && !slices.Equal(x.stopped, []string{p2.AgentID, p1.AgentID}) {
		t.Fatalf("stopped %v", x.stopped)
	}
	x.tick(time.Second)
	st := x.state()
	for _, w := range st.Workers {
		if w.Status != state.WorkerFailed || w.Why != "the run was dropped" || isDir(w.Worktree) {
			t.Errorf("worker %+v; worktree there %v", w, isDir(w.Worktree))
		}
	}
	if len(x.tracked()) != 0 {
		t.Errorf("still baton's agents: %v", x.tracked())
	}
	// The run was dropped: P1's branch, with a commit that never landed, is kept under kept/, so that no
	// worker of a later plan carries on from it.
	kept := x.worker("P1").Branch
	if x.worker("P1").Unlanded != 1 || kept != "baton/sess-1/kept/P1-20261011-1200" || gitx.RevParse(x.repo, "refs/heads/"+kept) == "" ||
		gitx.RevParse(x.repo, "refs/heads/"+p1.Branch) != "" {
		t.Errorf("P1's branch, with a commit that never landed: %q", kept)
	}
	if ann := strings.Join(x.state().Run.Announce, "\n"); !strings.Contains(ann, "its branch "+kept+" keeps 1 commit that never landed") {
		t.Errorf("announced %q", ann)
	}
	if x.worker("P2").Unlanded != 0 || gitx.RevParse(x.repo, "refs/heads/"+p2.Branch) != "" {
		t.Error("P2's branch, with nothing on it, is still there")
	}
	b, _ := os.ReadFile(filepath.Join(x.sub("P2").Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"snapshot"`) || !strings.Contains(string(b), `"files":1`) {
		t.Errorf("P2's uncommitted work was not saved:\n%s", b)
	}
	if n := strings.Count(x.events(), `"kind":"worker_ended"`); n != 2 {
		t.Errorf("%d workers ended:\n%s", n, x.events())
	}
	x.tick(time.Minute)
	if len(x.spawns()) != 2 {
		t.Error("a phase agent started on a dropped run")
	}
}

// A phase the plan no longer has takes its agent with it.
func TestARemovedPhaseCancelsItsWorker(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	revised := x.pl
	revised.Phases = []plan.Phase{x.pl.Phases[0], x.pl.Phases[2]}
	x.lead.WritePlan(revised)
	x.lead.Update(func(st *state.State) error { state.Revise(st, revised, x.now); return nil })
	x.tick(time.Second)
	if !slices.Equal(x.stopped, []string{x.agent("P1").id}) {
		t.Fatalf("stopped %v", x.stopped)
	}
	x.tick(time.Second)
	if w := x.state().Workers[0]; w.Status != state.WorkerFailed || w.Why != "its phase is no longer in the plan" {
		t.Errorf("worker %+v", w)
	}
}

// An agent that does not ack its stop in time is let go all the same: its worker is retired.
func TestAnAgentThatDoesNotStopIsRetiredAnyway(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	x.modOff = true
	x.lead.Update(func(st *state.State) error { return state.Detach(st) })
	x.tick(time.Second)
	x.tick(stopWait)
	if w := x.worker("P1"); w.Status == state.WorkerFailed {
		t.Fatal("retired before its stop was due")
	}
	x.tick(time.Second)
	if w := x.worker("P1"); w.Status != state.WorkerFailed || w.Why != "the run was dropped" || len(x.tracked()) != 0 {
		t.Errorf("worker %+v; baton's agents %v", w, x.tracked())
	}
}

// Phase agents end with the lead's claude: their workers are suspended, and the next host for the run
// starts an agent for each again, in its worktree as it was left, at once. The agent before it is none of
// baton's any more, and the new one is told it takes the phase up. One whose spawn was never acked starts
// anew.
func TestTheNextHostResumesSuspendedWorkers(t *testing.T) {
	for _, c := range []struct {
		name    string
		started bool
	}{{"an agent at work", true}, {"a spawn not acked yet", false}} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			if c.started {
				x.atWork("P1")
				gittest.Write(t, x.worker("P1").Worktree, "w.txt", "the worker's\n")
				gittest.Commit(t, x.worker("P1").Worktree, "the worker's commit")
			} else {
				x.modOff = true
				x.tick(time.Second)
			}
			before := x.worker("P1")
			var stopped []string
			suspended := x.c.Ended("the baton session ended", func(sub *state.Store) { stopped = append(stopped, sub.Ref) })
			if len(suspended) != 1 || suspended[0].Phase != "P1" || !slices.Equal(stopped, []string{"sess-1/P1"}) {
				t.Fatalf("Ended returned %+v, ran on %v", suspended, stopped)
			}
			if w := x.worker("P1"); w.Status != state.WorkerSuspended || w.Why != "the baton session ended" || x.state().Phases["P1"].Status != state.PhaseWorking {
				t.Fatalf("worker %+v", w)
			}
			if sst, _ := x.sub("P1").Load(); sst.Owner != nil {
				t.Errorf("the sub-run is still held: %+v", sst.Owner)
			}
			x.proj.Unbind("inst")

			x.modOff = false
			x.c = x.coordinator("next", 1)
			x.tick(time.Second)
			if got := x.spawns(); len(got) != 2 || !strings.Contains(x.events(), `"why":"the baton session ended"`) {
				t.Fatalf("spawned %v; events:\n%s", got, x.events())
			}
			a := x.agent("P1")
			x.tick(time.Second)
			w := x.worker("P1")
			if a.tree != before.Worktree || w.Status != state.WorkerWorking || w.AgentID != a.id || !slices.Equal(x.tracked(), []string{a.id}) {
				t.Fatalf("agent %+v; worker %+v; baton's agents %v", a, w, x.tracked())
			}
			sst, _ := x.sub("P1").Load()
			if sst.Owner == nil || sst.Owner.Instance != state.WorkerInstance("next", "P1") || sst.Run.Briefed != a.id {
				t.Errorf("the sub-run: owner %+v, briefed %q", sst.Owner, sst.Run.Briefed)
			}
			if c.started && gitx.Head(a.tree) != gitx.Head(before.Worktree) || !c.started && gitx.Head(a.tree) != before.Base {
				t.Errorf("the worktree is at %s", gitx.Head(a.tree))
			}
			if !x.logged("agent_placed", map[string]any{"phase": "P1", "agent": a.id, "kept": c.started}) {
				t.Errorf("events:\n%s", x.events())
			}
		})
	}
}

// The work on the phase agents runs off the lead's tick, one step at a time, and the host waits for the
// step under way before it suspends them.
func TestTheWorkersAreLookedAfterOffTheLeadsTick(t *testing.T) {
	x := newCrew(t, 2)
	x.c.Fleet.Go = nil
	for i := 0; i < 100 && (x.status("P1") != state.WorkerWorking || x.status("P2") != state.WorkerWorking); i++ {
		x.advance(time.Second)
		x.lead.Update(func(st *state.State) error { st.Run.LastActivity = x.clock(); return nil })
		x.c.Tick(host.View{Now: x.clock(), Store: x.lead, Owner: true, LastOutput: x.clock().Add(-time.Hour), LastHumanKey: x.clock().Add(-time.Hour)}, &typed{})
		time.Sleep(20 * time.Millisecond)
		x.c.fleetWG.Wait()
		x.runMod()
	}
	if got := x.c.Ended("the baton session ended", nil); len(got) != 2 {
		t.Errorf("suspended %+v", got)
	}
	for _, w := range x.state().Workers {
		if w.Status != state.WorkerSuspended {
			t.Errorf("worker %+v", w)
		}
	}
}

// A suspended worker has no agent to stop: when its run is dropped, the next host saves its uncommitted
// work itself, then removes its worktree.
func TestASuspendedWorkerOfADroppedRunIsCleanedUp(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	w := x.worker("P1")
	gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
	x.c.Ended("the baton session ended", nil)
	x.proj.Unbind("inst")
	x.lead.Update(func(st *state.State) error { return state.Detach(st) })

	x.c = x.coordinator("next", 1)
	x.tick(time.Second)
	if len(x.spawns()) != 1 {
		t.Fatalf("a worker of a dropped run was started again")
	}
	if got := x.worker("P1"); got.Status != state.WorkerFailed || got.Why != "the run was dropped" || isDir(w.Worktree) {
		t.Errorf("worker %+v; worktree there %v", got, isDir(w.Worktree))
	}
	b, _ := os.ReadFile(filepath.Join(x.sub("P1").Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"snapshot"`) || !strings.Contains(string(b), `"why":"the run was dropped"`) {
		t.Errorf("its uncommitted work was not saved:\n%s", b)
	}
}
