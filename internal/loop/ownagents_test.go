package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/board"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/statusline"
)

// study is a background subagent of the lead's own model, at work: a read-only study the human asked for.
func study(at time.Time) state.Task {
	return state.Task{ID: "t1", Type: "subagent", AgentType: "general-purpose", Description: "Study AU", Status: "running", Since: at}
}

// The lead's own background subagents hold nothing baton types (v0.8.20), as a phase agent does not: a
// compaction, a job's result or a line of mail leaves them running, and they report when they end. In a
// 74-phase campaign two read-only studies held the lead's boundary compaction for 20 minutes.
func TestTheModelsOwnAgentsHoldNothingBatonTypes(t *testing.T) {
	for _, c := range []struct {
		name  string
		rig   func(t *testing.T) *rig
		set   func(r *rig, st *state.State)
		typed func(text string) bool
	}{
		{"a boundary compaction", newRig, func(*rig, *state.State) {}, func(s string) bool { return s == "/compact" }},
		{"a checkpoint compaction", newIdleRig, func(r *rig, st *state.State) {
			st.CheckpointOwed = true
			st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactQueued, Reason: "checkpoint", Queued: r.now}
		}, func(s string) bool { return s == "/compact" }},
		{"a job's result", resultRig, func(r *rig, st *state.State) {
			st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Command: "make test", Status: state.JobFailed, Exit: 1}}
		}, func(s string) bool { return s == "[baton] Job j1 (P1) finished; its result is attached." }},
		{"mail", newIdleRig, func(r *rig, st *state.State) {
			state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: "[baton] something for you"}, r.now)
		}, func(s string) bool { return s != "" && s != "/compact" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.rig(t)
			r.set(func(st *state.State) {
				st.Run.Background = []state.Task{study(r.now)}
				c.set(r, st)
			})
			r.run(20 * time.Second)
			if len(r.in.text) != 1 || !c.typed(r.in.text[0]) {
				t.Fatalf("typed %q", r.in.text)
			}
			if st := r.state(); st.Run.Escalation != nil || st.Run.Gate != "" {
				t.Fatalf("escalation %+v, gate %q", st.Run.Escalation, st.Run.Gate)
			}
		})
	}
}

// An escalation that the model's background subagents held baton too long, recorded by a baton before
// v0.8.20, is cleared: they hold nothing now.
func TestAStuckEscalationFromBeforeClears(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.Background = []state.Task{study(r.now)}
		st.Run.Escalation = &state.Escalation{Kind: "stuck", Reason: "background subagents are still running", Since: r.now, Watchdog: true}
	})
	r.tick(time.Second)
	if e, ev := r.state().Run.Escalation, r.events(); e != nil || !strings.Contains(ev, `"kind":"escalation_cleared"`) || !strings.Contains(ev, `"type":"stuck"`) {
		t.Fatalf("escalation %+v; events:\n%s", e, r.events())
	}
}

// A restart on the next phase's tier would end the model's own subagent with the lead's claude, so it
// waits for it (restartable), and says so: Run.Gate names the wait, so the status line and the board say
// the compaction is held, never "compacting…". In a 74-phase campaign they read "compacting…" through the
// whole of a ten-minute tier wait. Once the subagent ends, the lead restarts.
func TestATierRestartWaitsForTheModelsOwnAgentsAndSaysSo(t *testing.T) {
	r := tierRig(t, plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"})
	r.set(func(st *state.State) { st.Run.Background = []state.Task{study(r.now)} })
	r.run(time.Minute)
	if _, ok := r.loop.Retier(); ok || r.in.quit != 0 || len(r.in.text) != 0 {
		t.Fatalf("with the model's subagent at work: quit %d, typed %q", r.in.quit, r.in.text)
	}
	const want = "restart on opus/max waits for 1 agent"
	st := r.state()
	if st.Run.Gate != want {
		t.Fatalf("gate %q, want %q", st.Run.Gate, want)
	}
	pl, err := r.loop.Store.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if seg := statusline.Segment(st, pl, true, r.now); !strings.HasSuffix(seg, "compact held: "+want) || strings.Contains(seg, "compacting") {
		t.Errorf("status line %q", seg)
	}
	b := board.Build(board.Input{State: st, Plan: pl, HavePlan: true, Now: r.now})
	if b.Lead.Kind != decide.LeadHeld || !strings.HasSuffix(b.Lead.Board(r.now), want) || b.Lead.Band() != "lead: compaction held" {
		t.Errorf("board %+v: %q", b.Lead, b.Lead.Board(r.now))
	}

	r.set(func(st *state.State) { st.Run.Background = nil })
	r.tick(time.Second)
	if got, ok := r.loop.Retier(); !ok || got != "opus/max" || r.in.quit != 1 {
		t.Fatalf("once the subagent ended: %q %v, quit %d", got, ok, r.in.quit)
	}
}

// What the hold names: phase agents and the model's own subagents as agents, its other background work as
// tasks, a gate's own reason when the gate is what holds the restart; and once the wait gives up
// (Timing.TierWait) the compaction is typed though the agents still work, with no second hold after it.
func TestATierWaitNamesWhatHoldsIt(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(r *rig, st *state.State)
		want string
	}{
		{"six phase agents", func(r *rig, st *state.State) {
			for _, id := range []string{"a1", "a2", "a3", "a4", "a5", "a6"} {
				st.Run.Background = append(st.Run.Background, state.Task{ID: id, Type: "subagent", AgentType: state.PhaseAgentAt("xhigh"), Status: "running"})
			}
		}, "restart on opus/max waits for 6 agents"},
		{"an agent and a shell", func(r *rig, st *state.State) {
			st.Run.Background = []state.Task{study(r.now), {ID: "b1", Type: "shell", Status: "running"}}
		}, "restart on opus/max waits for 1 agent and 1 background task"},
		{"a turn", func(r *rig, st *state.State) {
			st.Run.Background = []state.Task{study(r.now)}
			st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		}, gateTurn},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := tierRig(t, plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"})
			r.set(func(st *state.State) { c.set(r, st) })
			r.run(time.Minute)
			if g := r.state().Run.Gate; g != c.want {
				t.Fatalf("gate %q, want %q", g, c.want)
			}
		})
	}

	r := tierRig(t, plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"})
	r.set(func(st *state.State) { st.Run.Background = []state.Task{study(r.now)} })
	r.run(r.loop.Timing.TierWait + time.Minute)
	if len(r.in.text) != 1 || r.in.text[0] != "/compact" {
		t.Fatalf("after the tier wait: typed %q", r.in.text)
	}
	if st := r.state(); st.Run.Gate != "" || st.Run.Escalation != nil {
		t.Errorf("gate %q, escalation %+v", st.Run.Gate, st.Run.Escalation)
	}
}

// A fresh session of the lead would end the model's own subagent with its claude: it waits for it, as the
// gate no longer does, for at most BackgroundMax from when it was due, then goes on. Each is logged once.
func TestAFreshSessionWaitsForTheModelsOwnAgentsAWhile(t *testing.T) {
	r := newIdleRig(t)
	r.loop.StartsAfresh = true
	r.set(func(st *state.State) {
		st.Run.Background = []state.Task{study(r.now)}
		st.Run.LastError = tooLong(r.now)
	})
	r.run(DefaultTiming.BackgroundMax - time.Minute)
	if r.in.quit != 0 {
		t.Fatal("started afresh over the model's subagent")
	}
	if ev := r.events(); strings.Count(ev, `"kind":"afresh_waits"`) != 1 || strings.Contains(ev, "agents_overridden") {
		t.Fatalf("events:\n%s", ev)
	}
	r.run(2 * time.Minute)
	if r.in.quit != 1 {
		t.Fatal("never started afresh")
	}
	if ev := r.events(); strings.Count(ev, `"kind":"agents_overridden"`) != 1 || !strings.Contains(ev, `"for":"afresh"`) {
		t.Fatalf("events:\n%s", ev)
	}
}
