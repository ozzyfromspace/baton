package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// tierRig is the rig (P0 done, the boundary into P1 queued) with a plan on disk whose P1 names tier, a
// lead launched on launch, a host that can restart in place, and a session id on record.
func tierRig(t *testing.T, tier, launch plan.Tier) *rig {
	t.Helper()
	r := newRig(t)
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "a"}, {ID: "P1", Title: "b", Model: tier.Model, Effort: tier.Effort}}}
	if err := r.loop.Store.SavePlan(pl); err != nil {
		t.Fatal(err)
	}
	r.loop.Version, r.loop.Launch, r.loop.Retiers = "v0.8.18", launch, true
	r.set(func(st *state.State) { st.Run.SessionID = "sess-1" })
	return r
}

// At the boundary into a phase whose tier the lead does not run on, the lead restarts in place on it
// instead of typing the compaction, which the restarted session types: up a tier, and down one.
func TestTheLeadRestartsOnTheNextPhasesTierAtItsBoundary(t *testing.T) {
	for _, c := range []struct {
		name         string
		tier, launch plan.Tier
		want         string
	}{
		{"up", plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"}, "opus/max"},
		{"down", plan.Tier{Model: "opus", Effort: "low"}, plan.Tier{Model: "opus", Effort: "max"}, "opus/low"},
		{"another model", plan.Tier{Model: "sonnet", Effort: "high"}, plan.Tier{Model: "opus", Effort: "high"}, "sonnet/high"},
		{"the effort alone: the model stays", plan.Tier{Effort: "low"}, plan.Tier{Model: "opus", Effort: "high"}, "opus/low"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := tierRig(t, c.tier, c.launch)
			r.tick(time.Second)
			got, ok := r.loop.Retier()
			if !ok || got != c.want || r.in.quit != 1 || len(r.in.text) != 0 {
				t.Fatalf("retier %q %v, quit %d, typed %q", got, ok, r.in.quit, r.in.text)
			}
			if _, session, ok := r.loop.Restart(); !ok || session != "sess-1" {
				t.Fatalf("the restart resumes %q, %v", session, ok)
			}
			st := r.state()
			if rs := st.Run.Restart; rs == nil || rs.From != rs.To || rs.Tier == nil || rs.Tier.String() != c.want || rs.Phase != "P1" {
				t.Fatalf("Runtime.Restart %+v", rs)
			}
			if st.Run.Compaction.Status != state.CompactQueued {
				t.Fatalf("the compaction must stay queued for the restarted session: %+v", st.Run.Compaction)
			}
			if ev := r.events(); !strings.Contains(ev, `"kind":"tier_restart"`) || !strings.Contains(ev, `"to":"`+c.want+`"`) {
				t.Errorf("events: %s", ev)
			}
			if len(st.Run.Announce) == 0 || !strings.Contains(st.Run.Announce[len(st.Run.Announce)-1], c.want+", P1's tier") {
				t.Errorf("announced %q", st.Run.Announce)
			}
		})
	}
}

// A lead already on the phase's tier, a phase that names none, or a host that cannot restart in place:
// the compaction is typed as always.
func TestTheBoundaryGoesOnWhenTheTierHolds(t *testing.T) {
	for _, c := range []struct {
		name         string
		tier, launch plan.Tier
		retiers      bool
	}{
		{"on it already", plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "high"}, true},
		{"a phase with no tier", plan.Tier{}, plan.Tier{Model: "opus", Effort: "high"}, true},
		{"no restart in place", plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"}, false},
		{"a lead nothing has said of", plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := tierRig(t, c.tier, c.launch)
			r.loop.Retiers = c.retiers
			r.tick(time.Second)
			if _, ok := r.loop.Retier(); ok || r.in.quit != 0 {
				t.Fatalf("restarted on a tier, quit %d", r.in.quit)
			}
			if len(r.in.text) != 1 || !strings.Contains(r.in.text[0], "/compact") {
				t.Fatalf("typed %q", r.in.text)
			}
		})
	}
}

// What the lead's hooks and status line said stands in for a flag the human did not give: a lead launched
// on no --effort that runs on xhigh restarts for a phase at low.
func TestTheLeadsServedTierStandsInForItsFlags(t *testing.T) {
	r := tierRig(t, plan.Tier{Effort: "low"}, plan.Tier{Model: "opus"})
	r.set(func(st *state.State) { st.Run.Model, st.Run.Effort = "claude-opus-5-5", "xhigh" })
	r.tick(time.Second)
	if got, ok := r.loop.Retier(); !ok || got != "opus/low" {
		t.Fatalf("retier %q %v", got, ok)
	}
}

// A phase agent at work would end with the lead's claude: the boundary waits, the compaction untyped so
// that the phase starts on its tier, new agents are held for it, and once no agent is at work the lead
// restarts.
func TestTheBoundaryWaitsForTheAgentsAtWork(t *testing.T) {
	r := tierRig(t, plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"})
	r.set(func(st *state.State) {
		st.Run.Background = []state.Task{{ID: "a1", Type: "subagent", AgentType: state.PhaseAgentAt("high"), Status: "running"}}
	})
	r.run(time.Minute)
	if _, ok := r.loop.Retier(); ok || r.in.quit != 0 || len(r.in.text) != 0 {
		t.Fatalf("with an agent at work: quit %d, typed %q", r.in.quit, r.in.text)
	}
	if held, due := r.loop.retierHolds(r.view, r.state()); !due || held.String() != "opus/max" {
		t.Errorf("new agents not held: %s %v", held, due)
	}
	if ev := r.events(); strings.Count(ev, `"kind":"tier_waits"`) != 1 {
		t.Errorf("tier_waits logged %d times: %s", strings.Count(ev, `"kind":"tier_waits"`), ev)
	}
	r.set(func(st *state.State) { st.Run.Background = nil })
	r.tick(time.Second)
	if got, ok := r.loop.Retier(); !ok || got != "opus/max" || r.in.quit != 1 {
		t.Fatalf("once no agent was at work: %q %v, quit %d", got, ok, r.in.quit)
	}
}

// Mid-phase, a lead whose phase was raised restarts on the raised tier once it is idle, and is told to take
// the phase up again; while its turn runs, it does not.
func TestARaisedLeadPhaseRestartsOnceTheLeadIsIdle(t *testing.T) {
	r := tierRig(t, plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "high"})
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Reason: "boundary", Finished: r.now}
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: r.now, Raises: []state.TierRaise{
			{From: plan.Tier{Model: "opus", Effort: "high"}, To: plan.Tier{Model: "opus", Effort: "xhigh"}, Why: "3 red runs", At: r.now},
		}}
		st.Run.TurnOpen, st.Run.TurnStarted = true, r.now
	})
	r.run(time.Minute)
	if _, ok := r.loop.Retier(); ok {
		t.Fatal("restarted while the lead's turn ran")
	}
	r.set(func(st *state.State) { st.Run.TurnOpen, st.Run.LastStop = false, r.now })
	r.tick(time.Second)
	got, ok := r.loop.Retier()
	if !ok || got != "opus/xhigh" || r.in.quit != 1 {
		t.Fatalf("idle: %q %v, quit %d", got, ok, r.in.quit)
	}
	st := r.state()
	if len(st.Inbox) != 1 || st.Inbox[0].Kind != state.MessageTier || st.Inbox[0].Phase != "P1" || !strings.Contains(st.Inbox[0].Text, "Take P1 up again") {
		t.Errorf("the lead is not told to take P1 up again: %+v", st.Inbox)
	}
}

// Once no plan runs, a lead baton put on another tier goes back on the human's own, as they ran before,
// once idle; one on the human's own flags never restarts.
func TestTheLeadGoesBackOnTheHumansTierOnceNoPlanRuns(t *testing.T) {
	r := tierRig(t, plan.Tier{}, plan.Tier{Model: "opus", Effort: "low"})
	r.loop.Overridden = true
	r.set(func(st *state.State) {
		st.Mode, st.Run.Compaction = state.ModeComplete, state.Compaction{}
		st.Run.HumanModel, st.Run.HumanEffort = "claude-opus-5-5", "xhigh"
		st.Run.LastStop = r.now
	})
	r.run(r.loop.Timing.RestartIdle - 10*time.Second)
	if _, ok := r.loop.Retier(); ok {
		t.Fatal("restarted before the session was idle")
	}
	r.run(20 * time.Second)
	got, ok := r.loop.Retier()
	if !ok || got != "claude-opus-5-5/xhigh" || r.in.quit != 1 {
		t.Fatalf("back on the human's tier: %q %v, quit %d", got, ok, r.in.quit)
	}
	if rs := r.state().Run.Restart; rs == nil || rs.Phase != "" || rs.Tier == nil || rs.Tier.String() != "opus/xhigh" {
		t.Errorf("Runtime.Restart %+v", rs)
	}

	r = tierRig(t, plan.Tier{}, plan.Tier{Model: "opus", Effort: "low"})
	r.set(func(st *state.State) {
		st.Mode, st.Run.Compaction = state.ModeComplete, state.Compaction{}
		st.Run.HumanModel, st.Run.HumanEffort = "claude-opus-5-5", "xhigh"
	})
	r.run(2 * r.loop.Timing.RestartIdle)
	if _, ok := r.loop.Retier(); ok {
		t.Error("a lead on the human's own flags restarted")
	}
}

// The host's launch tier and allowed families go on the run, and so does the human's own tier while the
// host runs on the human's flags; never once a tier restart launched it.
func TestTheLeadsTierIsRecorded(t *testing.T) {
	r := tierRig(t, plan.Tier{}, plan.Tier{Model: "opus", Effort: "high"})
	r.loop.Tiers.Models = []string{"sonnet", "opus"}
	r.set(func(st *state.State) { st.Run.Model, st.Run.Effort = "claude-opus-5-5", "high" })
	r.tick(time.Second)
	st := r.state()
	if st.Run.Launched != (plan.Tier{Model: "opus", Effort: "high"}) || len(st.Run.Models) != 2 || st.Run.HumanModel != "claude-opus-5-5" || st.Run.HumanEffort != "high" {
		t.Fatalf("recorded %+v %v %q %q", st.Run.Launched, st.Run.Models, st.Run.HumanModel, st.Run.HumanEffort)
	}
	r.loop.Overridden = true
	r.set(func(st *state.State) { st.Run.Effort = "low" })
	r.tick(time.Second)
	if st := r.state(); st.Run.HumanEffort != "high" {
		t.Errorf("a lead on baton's tier was taken for the human's: %q", st.Run.HumanEffort)
	}
}

// TierEnv round-trips.
func TestTierEnv(t *testing.T) {
	for _, c := range []struct{ model, effort string }{{"opus", "max"}, {"", "low"}, {"claude-opus-5-5", ""}} {
		if m, e := DecodeTier(EncodeTier(c.model, c.effort)); m != c.model || e != c.effort {
			t.Errorf("%q/%q came back %q/%q", c.model, c.effort, m, e)
		}
	}
}

// A restart that cannot come for TierWait (a phase agent at work all that time, one at a permission prompt
// nobody answers, say) holds nothing longer: the boundary's compaction is typed, the phase starts on the
// lead's tier, new agents start again, and the human is told once. Nothing baton decides holds a run.
func TestTheLeadsTierWaitHasADeadline(t *testing.T) {
	r := tierRig(t, plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"})
	r.set(func(st *state.State) {
		st.Run.Background = []state.Task{{ID: "a1", Type: "subagent", AgentType: state.PhaseAgentAt("high"), Status: "running"}}
	})
	r.run(r.loop.Timing.TierWait - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("typed before the deadline: %q", r.in.text)
	}
	if _, due := r.loop.retierHolds(r.view, r.state()); !due {
		t.Fatal("new agents not held before the deadline")
	}
	r.run(2 * time.Minute)
	if len(r.in.text) != 1 || !strings.Contains(r.in.text[0], "/compact") {
		t.Fatalf("after the deadline the compaction must go ahead: typed %q", r.in.text)
	}
	if _, due := r.loop.retierHolds(r.view, r.state()); due {
		t.Error("new agents still held after the deadline")
	}
	if ev := r.events(); strings.Count(ev, `"kind":"tier_given_up"`) != 1 {
		t.Errorf("tier_given_up %d times: %s", strings.Count(ev, `"kind":"tier_given_up"`), ev)
	}
	a := r.state().Run.Announce
	if len(a) == 0 || !strings.Contains(strings.Join(a, "\n"), "P1 goes on on opus/high, not its tier opus/max") {
		t.Errorf("announced %q", a)
	}
	if _, ok := r.loop.Retier(); ok {
		t.Error("restarted with an agent at work")
	}
}

// A lead restarted on its phase's tier is checked against what it is served, once: a tier Claude Code
// capped is told to the human. One not launched for the phase's tier is not judged.
func TestTheLeadIsCheckedAgainstWhatItIsServed(t *testing.T) {
	r := tierRig(t, plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "max"})
	r.loop.Overridden = true
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, Finished: r.now}
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: r.now}
		st.Run.TurnOpen = true
	})
	r.tick(time.Second)
	if strings.Contains(r.events(), `"kind":"tier_served"`) {
		t.Fatal("judged before anything said what the lead runs on")
	}
	r.set(func(st *state.State) { st.Run.Model, st.Run.Effort = "claude-opus-5-5", "high" })
	r.tick(time.Second)
	r.tick(time.Second)
	if ev := r.events(); strings.Count(ev, `"kind":"tier_served"`) != 1 || !strings.Contains(ev, `"fits":false`) {
		t.Fatalf("events: %s", ev)
	}
	if a := r.state().Run.Announce; len(a) == 0 || !strings.Contains(a[len(a)-1], "P1 asked for opus/max and was served opus/high") {
		t.Errorf("announced %q", a)
	}
	r = tierRig(t, plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "high"})
	r.loop.Overridden = true
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, Finished: r.now}
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: r.now}
		st.Run.TurnOpen = true
		st.Run.Model, st.Run.Effort = "claude-opus-5-5", "high"
	})
	r.tick(time.Second)
	if strings.Contains(r.events(), `"kind":"tier_served"`) {
		t.Error("a lead launched on another tier was judged")
	}
}
