package loop

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// upgradeAt is where a running plan stands as its host finds a newer baton.
type upgradeAt struct {
	inHand bool // the lead holds P1; otherwise it holds none, or takes P1 up after the compaction queued
	agents bool // P2's phase agent is at work; otherwise P2's worker waits for a fresh agent: its agent checkpointed
	queued bool // a compaction is queued: a checkpoint's, with P1 in hand, else the boundary before P1
}

// idleRig is the lead's loop, on a newer baton's arrival, over the plan P0 (done), P1 (the lead's) and P2 (a
// phase agent's), standing at u. The lead's turn is over: a queued compaction's turn ended just now, and its
// Stop listed only the agent at work; otherwise its last Stop was an hour ago, with P2's first agent at work.
func idleRig(t *testing.T, u upgradeAt) *rig {
	r := &rig{t: t, now: time.Date(2026, 10, 23, 10, 29, 0, 0, time.UTC), in: &typed{}, push: &pushes{}}
	store, err := state.Open(t.TempDir(), "inst", func() time.Time { return r.now })
	if err != nil {
		t.Fatal(err)
	}
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "a"}, {ID: "P1", Title: "b"}, {ID: "P2", Title: "c"}}}
	if err := store.WritePlan(pl); err != nil {
		t.Fatal(err)
	}
	hour := r.now.Add(-time.Hour)
	if _, err := store.Update(func(st *state.State) error {
		*st = state.Attach(pl, hour.Add(-time.Hour), state.Origin{})
		st.Run.SessionID = "sess-1"
		st.Phases["P0"].Status, st.Phases["P0"].DoneAt = state.PhaseDone, hour
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, Started: hour, Finished: hour}
		st.BoundaryOwed, st.Run.LastStop = false, hour
		switch {
		case u.inHand:
			st.Current = "P1"
			st.Phases["P1"].Status, st.Phases["P1"].StartedAt = state.PhaseActive, hour
			if u.queued {
				st.OweCheckpoint(r.now)
				st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactQueued, Reason: "checkpoint", Queued: r.now}
			}
		case u.queued:
			st.Current = "P1"
			st.OweBoundary(r.now)
			st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactQueued, Reason: "boundary", Queued: r.now}
		default:
			st.Current = ""
			st.Phases["P1"].Status, st.Phases["P1"].DoneAt = state.PhaseDone, hour
		}
		w := state.Worker{Phase: "P2", Ref: state.WorkerRef("sess-1", "P2"), AgentID: "agent-p2", Assigned: hour, Since: r.now.Add(-time.Minute),
			Status: state.WorkerSuspended, Why: decide.AgentCheckpointed}
		if u.agents {
			w.Status, w.Why = state.WorkerWorking, ""
		}
		st.Phases["P2"] = &state.PhaseState{Status: state.PhaseWorking, StartedAt: hour, Worker: w.Ref}
		st.Workers = []state.Worker{w}
		if u.queued {
			st.Run.LastStop = r.now
		}
		if u.agents || !u.queued {
			st.Run.Background = []state.Task{{ID: "agent-p2", Type: "subagent", AgentType: state.PhaseAgent}}
		}
		return state.Claim(st, "inst", 1, r.now)
	}); err != nil {
		t.Fatal(err)
	}
	r.loop = &Loop{Store: store, Notify: r.push, Project: "demo", Timing: DefaultTiming, Workers: true,
		Version: "v0.8.15", Newer: func() string { return "v0.8.16" }}
	r.coord = &Coordinator{Lead: r.loop}
	r.view = host.View{Owner: true, LastOutput: r.now.Add(-time.Hour), LastHumanKey: r.now.Add(-time.Hour)}
	return r
}

// restarting is what the rig's restarting event said, or nil for none.
func (r *rig) restarting() map[string]any {
	for _, line := range strings.Split(r.events(), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["kind"] == "restarting" {
			return e
		}
	}
	return nil
}

// A newer baton never holds a run whose lead cannot reach a point to restart at. A lead that holds a
// phase, or has a compaction queued, restarts at the compaction once no phase agent is at work, and holds
// new agents until then; one that holds no phase has no compaction coming, so it restarts at once with no
// agent at work, and lets agents start otherwise. Either way a restart never ends an agent at work.
func TestAnUpgradeRestartsWhereTheLeadCanReachIt(t *testing.T) {
	const now, atCompaction, not = "now", "at the compaction", "not"
	for _, c := range []struct {
		at      upgradeAt
		restart string
		holds   bool // Coordinator.pending holds new agents
	}{
		{upgradeAt{inHand: true}, not, true},
		{upgradeAt{inHand: true, queued: true}, atCompaction, true},
		{upgradeAt{inHand: true, agents: true}, not, true},
		{upgradeAt{inHand: true, agents: true, queued: true}, not, true},
		{upgradeAt{}, now, false},
		{upgradeAt{queued: true}, atCompaction, true},
		{upgradeAt{agents: true}, not, false},
		{upgradeAt{agents: true, queued: true}, not, true},
	} {
		name := map[bool]string{true: "a phase in hand", false: "no phase"}[c.at.inHand] +
			map[bool]string{true: ", an agent at work", false: ", no agent"}[c.at.agents] +
			map[bool]string{true: ", a compaction queued", false: ""}[c.at.queued]
		t.Run(name, func(t *testing.T) {
			r := idleRig(t, c.at)
			r.view.Now = r.now
			if holds := r.loop.upgradeHolds(r.view, r.state()); holds != c.holds {
				t.Errorf("new agents held %v, want %v", holds, c.holds)
			}
			r.tick(time.Second)
			if c.restart == not {
				r.run(time.Minute)
			}
			st, e := r.state(), r.restarting()
			if r.restarted() != (c.restart != not) {
				t.Fatalf("restarted %v, want %s:\n%s", r.restarted(), c.restart, r.events())
			}
			switch c.restart {
			case now:
				if e["why"] != idleWhy || r.in.quit != 1 || len(r.in.text) != 0 || st.Run.Compaction.Status != state.CompactDone || st.Run.Restart == nil {
					t.Errorf("restarting %v, quit %d, typed %q, %+v", e, r.in.quit, r.in.text, st.Run.Compaction)
				}
			case atCompaction:
				if _, idle := e["why"]; idle || len(r.in.text) != 0 || st.Run.Compaction.Status != state.CompactQueued {
					t.Errorf("restarting %v, typed %q: the compaction must stay queued for the restarted session: %+v", e, r.in.text, st.Run.Compaction)
				}
			}
		})
	}
}

// What holds a restart holds the idle one too: the lead's turn, the human at the keyboard, the model's own
// background work, a phase agent the roster has at work (one starting, or idle with its work in the merge
// queue, too), and a compaction owed, which is the restart point of its own. A failed compaction nothing
// owes any more holds nothing, nor does a worker held for the human, whose agent they stopped.
func TestAnIdleLeadRestartsOnlyWithNothingUnderWay(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(r *rig, st *state.State)
		restart bool
	}{
		{"nothing", func(*rig, *state.State) {}, true},
		{"the lead's turn", func(r *rig, st *state.State) {
			st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		}, false},
		{"the human typing", func(r *rig, _ *state.State) { r.view.LastHumanKey = r.now }, false},
		{"a background shell", func(_ *rig, st *state.State) {
			st.Run.Background = append(st.Run.Background, state.Task{ID: "b1", Type: "shell"})
		}, false},
		{"an agent starting", func(_ *rig, st *state.State) { st.Workers[0].Status, st.Workers[0].AgentID = state.WorkerStarting, "" }, false},
		{"an agent's work landing", func(_ *rig, st *state.State) { st.Workers[0].Status = state.WorkerLanding }, false},
		{"a worker held for the human", func(_ *rig, st *state.State) { st.Workers[0].Status = state.WorkerHeld }, true},
		{"a checkpoint owed", func(r *rig, st *state.State) { st.OweCheckpoint(r.now) }, false},
		{"a failed compaction owed nothing", func(r *rig, st *state.State) {
			st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactFailed, FailedAt: r.now.Add(-time.Hour)}
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := idleRig(t, upgradeAt{})
			r.set(func(st *state.State) { c.set(r, st) })
			r.tick(time.Second)
			if r.restarted() != c.restart {
				t.Fatalf("restarted %v, want %v:\n%s", r.restarted(), c.restart, r.events())
			}
		})
	}
}

// The run that stalled (v0.8.14): the lead holds no phase and waits on a phase agent, which checkpoints, so
// a fresh agent is due, as a newer baton is installed. The lead restarts at once, with no agent at work,
// though its last Stop still lists the agent that checkpointed; the restarted host starts the fresh agent.
func TestAnIdleLeadRestartsAndTheNextHostStartsTheFreshAgent(t *testing.T) {
	x := newCrewOn(t, 1, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}}}})
	x.atWork("P1")
	first := x.agent("P1")
	x.leadDone("P0") // nothing is left for the lead: it waits on P1
	x.leadIdle = true
	x.lead.Update(func(st *state.State) error {
		st.Run.SessionID, st.Run.TurnOpen, st.Run.LastStop = "sess-1", false, x.now
		st.Run.Background = []state.Task{{ID: first.id, Type: "subagent", AgentType: state.PhaseAgent}}
		return nil
	})
	x.c.Lead.Version, x.c.Lead.Newer = "v0.8.15", func() string { return "v0.8.16" }
	x.tick(time.Second)
	if _, _, ok := x.c.Lead.Restart(); ok || x.c.pending.Load() {
		t.Fatalf("restarted %v, new agents held %v, with P1's agent at work", ok, x.c.pending.Load())
	}
	x.sub("P1").Update(func(st *state.State) error { return state.SetCheckpoint(st, x.now) })
	x.stops("P1")
	for range 4 {
		if x.tick(time.Second); x.c.Lead.quit {
			break
		}
	}
	if to, _, ok := x.c.Lead.Restart(); !ok || to != "v0.8.16" || !x.logged("restarting", map[string]any{"why": idleWhy}) {
		t.Fatalf("restart %q %v:\n%s", to, ok, x.events())
	}
	if !slices.Equal(x.spawns(), []string{"P1"}) {
		t.Fatalf("an agent started as the session restarts: %v", x.spawns())
	}
	x.c.Ended("baton restarted on baton v0.8.16", nil)
	x.proj.Unbind("inst")
	x.c = x.coordinator("next", 1)
	x.leadIdle = false // the restarted session starts: its hooks run
	// The restarted host is a new instance: the sub-run the agent left is its own once the old host's
	// claim on it is stale.
	x.tick(state.OwnerTTL)
	for range 4 {
		x.tick(time.Second)
	}
	if fresh := x.agent("P1"); fresh.id == first.id || !slices.Equal(x.spawns(), []string{"P1", "P1"}) || x.status("P1") != state.WorkerWorking {
		t.Errorf("the next host: agents %+v, spawns %v, P1 %s:\n%s", x.agents, x.spawns(), x.status("P1"), x.events())
	}
}

// While the lead holds no phase, a newer baton holds no agent: one at work holds the restart, and another
// starts meanwhile, here the fresh agent of a phase whose agent checkpointed.
func TestAnIdleLeadLetsAgentsStartWhileOneWorks(t *testing.T) {
	x := newCrewOn(t, 2, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}}, {ID: "P2", Title: "two", After: []string{}}}})
	x.atWork("P1", "P2")
	x.leadDone("P0")
	x.leadIdle = true
	x.lead.Update(func(st *state.State) error {
		st.Run.SessionID, st.Run.TurnOpen = "sess-1", false
		return nil
	})
	x.c.Lead.Version, x.c.Lead.Newer = "v0.8.15", func() string { return "v0.8.16" }
	x.sub("P1").Update(func(st *state.State) error { return state.SetCheckpoint(st, x.now) })
	x.stops("P1")
	for range 4 {
		x.tick(time.Second)
	}
	if _, _, ok := x.c.Lead.Restart(); ok {
		t.Fatalf("restarted with P2's agent at work:\n%s", x.events())
	}
	if !slices.Equal(x.spawns(), []string{"P1", "P2", "P1"}) || x.status("P1") != state.WorkerWorking {
		t.Errorf("spawns %v, P1 %s:\n%s", x.spawns(), x.status("P1"), x.events())
	}
}
