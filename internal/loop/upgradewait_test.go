package loop

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// eventsOf lists the events of kind in a run's log, oldest first.
func eventsOf(log, kind string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(log, "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

// told lists what the run announced to the human that contains part.
func told(st state.State, part string) []string {
	var out []string
	for _, a := range st.Run.Announce {
		if strings.Contains(a, part) {
			out = append(out, a)
		}
	}
	return out
}

// The campaign that stalled on v0.8.20: the lead holds a phase, a phase agent is at work, and a background
// poll loop the lead's model started 22 hours before is still running when a newer baton is installed.
// Holding new agents cannot clear the loop, so none is held; ten minutes on, the human is told what keeps
// the restart away, the loop first, once, and upgrade_waits logs each kind of it once.
func TestAStaleBackgroundShellHoldsNoAgentAndIsNamed(t *testing.T) {
	r := idleRig(t, upgradeAt{inHand: true, agents: true})
	r.set(func(st *state.State) {
		st.Run.Background = append(st.Run.Background, state.Task{ID: "b8196fcr0", Type: "shell", Description: "Wait for the P23 tests", Since: r.now.Add(-22 * time.Hour)})
		st.Run.BackgroundToldAt = map[string]time.Time{"b8196fcr0": r.now.Add(-22*time.Hour - 5*time.Minute)}
	})
	r.view.Now = r.now
	if r.loop.upgradeHolds(r.view, r.state()) {
		t.Fatal("new agents held for a restart the model's own background shell keeps away")
	}
	r.run(9 * time.Minute)
	if e := eventsOf(r.events(), "upgrade_waits"); len(e) != 0 || len(told(r.state(), "could not restart")) != 0 {
		t.Fatalf("said before UpgradeWait: %v", e)
	}
	r.run(2 * time.Minute)
	waits := eventsOf(r.events(), "upgrade_waits")
	causes := []string{}
	for _, e := range waits {
		causes = append(causes, fmt.Sprint(e["cause"]))
	}
	if slices.Sort(causes); !slices.Equal(causes, []string{upAgents, upTasks}) {
		t.Fatalf("upgrade_waits causes %v:\n%s", causes, r.events())
	}
	for _, e := range waits {
		if e["version"] != "v0.8.16" {
			t.Errorf("upgrade_waits %v", e)
		}
		if e["cause"] == upTasks {
			tasks, _ := e["tasks"].([]any)
			if len(tasks) != 1 || !strings.Contains(fmt.Sprint(tasks[0]), "b8196fcr0") || !strings.Contains(fmt.Sprint(tasks[0]), "Wait for the P23 tests") {
				t.Errorf("tasks %v", e["tasks"])
			}
		}
	}
	said := told(r.state(), "could not restart")
	if len(said) != 1 {
		t.Fatalf("told %q", r.state().Run.Announce)
	}
	for _, part := range []string{"baton v0.8.16", `the background shell "Wait for the P23 tests" (running 22h1`, "the phase agent on P2", "can be stopped"} {
		if !strings.Contains(said[0], part) {
			t.Errorf("told %q, want %q in it", said[0], part)
		}
	}
	r.run(20 * time.Minute)
	if n := len(eventsOf(r.events(), "upgrade_waits")); n != 2 || len(told(r.state(), "could not restart")) != 1 || r.restarted() {
		t.Errorf("upgrade_waits %d times, told %d times, restarted %v: once per version and kind", n, len(told(r.state(), "could not restart")), r.restarted())
	}
}

// With phase agents all that keeps the restart away, new agents are held for it, for Timing.UpgradeWait at
// most: then they start again, the human is told once (upgrade_given_up), and the restart comes at the
// next compaction with nothing in the way. A version installed after it starts the wait over.
func TestANewerBatonHoldsAgentsForUpgradeWaitAtMost(t *testing.T) {
	r := idleRig(t, upgradeAt{inHand: true, agents: true})
	holds := func(after time.Duration) bool {
		r.now = r.now.Add(after)
		r.view.Now = r.now
		return r.loop.upgradeHolds(r.view, r.state())
	}
	if !holds(0) || !holds(DefaultTiming.UpgradeWait-time.Second) {
		t.Fatal("new agents not held while phase agents alone keep the restart away")
	}
	if holds(time.Second) {
		t.Fatal("new agents still held at UpgradeWait")
	}
	given := eventsOf(r.events(), "upgrade_given_up")
	if len(given) != 1 || given[0]["version"] != "v0.8.16" || given[0]["waited"] != "10m0s" || fmt.Sprint(given[0]["agents"]) != "[P2]" {
		t.Fatalf("upgrade_given_up %v", given)
	}
	if want := decide.UpgradeGivenUp("v0.8.16", 10*time.Minute, []string{"P2"}); len(told(r.state(), want)) != 1 {
		t.Errorf("told %q, want %q", r.state().Run.Announce, want)
	}
	if holds(time.Minute) || len(eventsOf(r.events(), "upgrade_given_up")) != 1 {
		t.Fatal("held again, or given up twice, for the same version")
	}

	// P2's agent checkpoints, and the lead's checkpoint is queued with nothing else in the way: the restart.
	r.set(func(st *state.State) {
		st.Workers[0].Status, st.Workers[0].Why = state.WorkerSuspended, decide.AgentCheckpointed
		st.Run.Background = nil
		st.OweCheckpoint(r.now)
		st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactQueued, Reason: "checkpoint", Queued: r.now}
		st.Run.LastStop = r.now
	})
	r.tick(time.Second)
	if e := r.restarting(); !r.restarted() || e == nil || e["to"] != "v0.8.16" || r.state().Run.Compaction.Status != state.CompactQueued {
		t.Fatalf("restarting %v, restarted %v, %+v:\n%s", e, r.restarted(), r.state().Run.Compaction, r.events())
	}

	r = idleRig(t, upgradeAt{inHand: true, agents: true})
	holds(0)
	holds(DefaultTiming.UpgradeWait)
	r.loop.Newer = func() string { return "v0.8.17" }
	if !holds(time.Second) {
		t.Error("a newer version installed after a hold gave up is not held for")
	}
}

// What holds new agents for a restart and what does not, with a phase in hand and an agent at work: the
// model's own work (a shell, a subagent of its own, a wakeup, a monitor) is never waited on by holding
// agents, and the moment's gates hold nothing back.
func TestOnlyPhaseAgentsHoldNewAgentsForAnUpgrade(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   func(r *rig, st *state.State)
		holds bool
	}{
		{"phase agents alone", func(*rig, *state.State) {}, true},
		{"the lead's turn", func(r *rig, st *state.State) { st.Run.TurnOpen, st.Run.TurnStarted = true, r.now }, true},
		{"a background shell", func(_ *rig, st *state.State) {
			st.Run.Background = append(st.Run.Background, state.Task{ID: "b1", Type: "shell"})
		}, false},
		{"a subagent of the model's own", func(_ *rig, st *state.State) {
			st.Run.Background = append(st.Run.Background, state.Task{ID: "a1", Type: "subagent", AgentType: "general-purpose"})
		}, false},
		{"a wakeup", func(_ *rig, st *state.State) { st.Run.Crons = 1 }, false},
		{"a monitor", func(_ *rig, st *state.State) {
			st.Run.Background = append(st.Run.Background, state.Task{ID: "m1", Type: "shell"})
			st.Run.Monitors = []string{"m1"}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := idleRig(t, upgradeAt{inHand: true, agents: true})
			r.set(func(st *state.State) { c.set(r, st) })
			r.view.Now = r.now
			if got := r.loop.upgradeHolds(r.view, r.state()); got != c.holds {
				t.Errorf("held %v, want %v", got, c.holds)
			}
		})
	}
}

// The coordinator starts new agents beside a newer baton while the model's own background shell keeps the
// restart away, and holds them, for UpgradeWait at most, while phase agents alone would.
func TestNewAgentsStartWhileAShellKeepsTheRestartAway(t *testing.T) {
	pl := plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}}, {ID: "P2", Title: "two", After: []string{}}}}
	for _, shell := range []bool{true, false} {
		t.Run(map[bool]string{true: "a stale shell", false: "agents alone"}[shell], func(t *testing.T) {
			x := newCrewOn(t, 2, pl)
			x.atWork("P1")
			x.lead.Update(func(st *state.State) error {
				st.Run.SessionID = "sess-1"
				if shell {
					st.Run.Background = append(st.Run.Background, state.Task{ID: "b1", Type: "shell", Description: "poll", Since: x.now.Add(-22 * time.Hour)})
				}
				return nil
			})
			x.c.Lead.Version, x.c.Lead.Newer = "v0.8.15", func() string { return "v0.8.16" }
			for range 3 {
				x.tick(AgentRamp)
			}
			if shell {
				if x.c.pending.Load() || !slices.Equal(x.spawns(), []string{"P1", "P2"}) {
					t.Fatalf("held %v, spawns %v:\n%s", x.c.pending.Load(), x.spawns(), x.events())
				}
				return
			}
			if !x.c.pending.Load() || !slices.Equal(x.spawns(), []string{"P1"}) {
				t.Fatalf("held %v, spawns %v", x.c.pending.Load(), x.spawns())
			}
			for range 3 {
				x.tick(DefaultTiming.UpgradeWait / 2)
			}
			if x.c.pending.Load() || !slices.Equal(x.spawns(), []string{"P1", "P2"}) || !x.logged("upgrade_given_up", map[string]any{"version": "v0.8.16"}) {
				t.Fatalf("held %v, spawns %v:\n%s", x.c.pending.Load(), x.spawns(), x.events())
			}
		})
	}
}
