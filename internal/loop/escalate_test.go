package loop

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

var stdTiers = config.Tiers{Models: []string{"haiku", "sonnet", "opus"}, Steps: 2, After: 3}

// redRuns adds n red runs of phase's gate to st, ended a moment after at (after a raise at at), with their
// environment read.
func redRuns(st *state.State, phase string, n int, at time.Time) {
	at = at.Add(time.Millisecond)
	for range n {
		id := fmt.Sprintf("%s.j%d", phase, len(st.Jobs)+1)
		st.Jobs = append(st.Jobs, state.Job{ID: id, Phase: phase, Kind: state.JobVerify, Command: "go test ./...", Status: state.JobFailed, Ended: at, EnvRead: at})
	}
}

// The lead's phase, its gate red three times, is raised a step, then once more three reds later, and no
// further: its steps are used, which is logged once. Each raise is announced and logged.
func TestTheLeadsRedPhaseIsRaisedAStepAtATime(t *testing.T) {
	r := tierRig(t, plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "high"})
	r.loop.Tiers = stdTiers
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Reason: "boundary", Finished: r.now}
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: r.now}
		st.Run.TurnOpen, st.Run.TurnStarted = true, r.now // at work: no restart yet
		redRuns(st, "P1", 2, r.now)
	})
	r.tick(time.Second)
	if st := r.state(); st.Raised("P1") != nil {
		t.Fatalf("raised after two red runs: %+v", st.Phases["P1"].Raises)
	}
	r.set(func(st *state.State) { redRuns(st, "P1", 1, r.now) })
	r.tick(time.Second)
	st := r.state()
	if raised := st.Raised("P1"); raised == nil || raised.String() != "opus/xhigh" {
		t.Fatalf("after three: %+v", st.Phases["P1"].Raises)
	}
	if raise := st.Phases["P1"].Raises[0]; raise.From.String() != "opus/high" || raise.Reds != 3 || !strings.Contains(raise.Why, "3 red runs of its gate (`go test ./...`)") {
		t.Errorf("the raise %+v", raise)
	}
	if a := st.Run.Announce; len(a) == 0 || !strings.Contains(a[len(a)-1], "raised P1 from opus/high to opus/xhigh after 3 red runs") || !strings.Contains(a[len(a)-1], "your session restarts on that tier") {
		t.Errorf("announced %q", a)
	}
	r.tick(time.Second)
	if n := len(r.state().Phases["P1"].Raises); n != 1 {
		t.Fatalf("the same reds raised it %d times", n)
	}
	r.set(func(st *state.State) { redRuns(st, "P1", 3, r.now) })
	r.tick(time.Second)
	if raised := r.state().Raised("P1"); raised == nil || raised.String() != "opus/max" {
		t.Fatalf("after three more: %v", raised)
	}
	r.set(func(st *state.State) { redRuns(st, "P1", 3, r.now) })
	r.tick(time.Second)
	r.tick(time.Second)
	if n := len(r.state().Phases["P1"].Raises); n != 2 {
		t.Errorf("raised past its steps: %d", n)
	}
	if ev := r.events(); strings.Count(ev, `"kind":"tier_raised"`) != 2 || strings.Count(ev, `"kind":"tier_ceiling"`) != 1 || !strings.Contains(ev, `"why":"its steps are used"`) {
		t.Errorf("events: %s", ev)
	}
	// Once idle, the lead restarts on the raised tier and is told why.
	r.set(func(st *state.State) { st.Run.TurnOpen, st.Run.LastStop = false, r.now })
	r.tick(time.Second)
	if got, ok := r.loop.Retier(); !ok || got != "opus/max" {
		t.Fatalf("the lead did not restart on its raised tier: %q %v", got, ok)
	}
	if in := r.state().Inbox; len(in) != 1 || !strings.Contains(in[0].Text, "baton raised P1 to opus/max after 3 red runs") {
		t.Errorf("told %+v", in)
	}
}

// With escalate off (0 steps), nothing is raised however often the gate fails.
func TestNoRaiseWithEscalateOff(t *testing.T) {
	r := tierRig(t, plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "high"})
	r.loop.Tiers = config.Tiers{Models: stdTiers.Models, Steps: 0, After: 3}
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, Finished: r.now}
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: r.now}
		redRuns(st, "P1", 9, r.now)
	})
	r.tick(time.Second)
	if st := r.state(); st.Raised("P1") != nil {
		t.Fatalf("raised: %+v", st.Phases["P1"].Raises)
	}
}

// A phase agent is spawned on its phase's tier: the type of its effort, on its model, which its worker
// keeps; a phase with no tier is spawned as before.
func TestAPhaseAgentIsSpawnedOnItsTier(t *testing.T) {
	x := newCrewOn(t, 2, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}},
		{ID: "P1", Title: "one", After: []string{}, Model: "opus", Effort: "max"},
		{ID: "P2", Title: "two", After: []string{}},
	}})
	x.c.Lead.Tiers = stdTiers
	x.atWork("P1", "P2")
	got := map[string]bridge.Command{}
	for _, c := range x.spawnCmds {
		got[c.Phase] = c
	}
	if c := got["P1"]; c.Effort != "max" || c.Model != "opus" {
		t.Errorf("P1's spawn: effort %q, model %q", c.Effort, c.Model)
	}
	if c := got["P2"]; c.Effort != "" || c.Model != "" {
		t.Errorf("P2's spawn: effort %q, model %q", c.Effort, c.Model)
	}
	if w := x.worker("P1"); w.Tier != (plan.Tier{Model: "opus", Effort: "max"}) {
		t.Errorf("P1's worker keeps %+v", w.Tier)
	}
	if !x.logged("worker_spawned", map[string]any{"phase": "P1", "tier": "opus/max"}) {
		t.Errorf("events: %s", x.events())
	}
	if b := x.briefs[x.agent("P1").id]; !strings.Contains(b, "You run on opus/max, P1's tier from its plan.") {
		t.Errorf("P1's brief does not say its tier:\n%s", b)
	}
}

// A phase agent's gate red three times raises its phase a step, and once its agent is idle a fresh one
// takes the phase up on the new tier, in its worktree, told why.
func TestARedPhaseAgentIsRaisedAndReplacedOnTheNewTier(t *testing.T) {
	x := newCrewOn(t, 1, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}},
		{ID: "P1", Title: "one", After: []string{}, Model: "opus", Effort: "high"},
	}})
	x.c.Lead.Tiers = stdTiers
	x.atWork("P1")
	first := x.agent("P1")
	x.sub("P1").Update(func(st *state.State) error {
		redRuns(st, "P1", 3, x.now)
		st.Run.TurnOpen, st.Run.TurnStarted = true, x.now // its turn runs: it is replaced once idle
		return nil
	})
	x.tick(time.Second)
	if raised := x.state().Raised("P1"); raised == nil || raised.String() != "opus/xhigh" {
		t.Fatalf("not raised: %+v", x.state().Phases["P1"])
	}
	x.tick(time.Second)
	if x.status("P1") != state.WorkerWorking {
		t.Fatalf("an agent at work was replaced mid-turn: %s", x.status("P1"))
	}
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = false; return nil })
	for range 6 {
		x.tick(time.Second)
	}
	next := x.agent("P1")
	if next.id == first.id {
		t.Fatalf("no fresh agent: %+v\n%s", x.state().Workers, x.events())
	}
	if w := x.worker("P1"); w.Tier != (plan.Tier{Model: "opus", Effort: "xhigh"}) || w.Status != state.WorkerWorking {
		t.Errorf("the fresh agent's worker: %+v", w)
	}
	if b := x.briefs[next.id]; !strings.Contains(b, "baton raised P1 from opus/high to opus/xhigh after 3 red runs of its gate") {
		t.Errorf("the fresh agent's brief does not say why:\n%s", b)
	}
	if got := x.spawnCmds[len(x.spawnCmds)-1]; got.Phase != "P1" || got.Effort != "xhigh" || got.Model != "opus" {
		t.Errorf("the fresh agent's spawn: %+v", got)
	}
}

// A phase whose plan names no tier, as every plan from before tiers, is never raised, however often its
// gate goes red: it runs as phases always have.
func TestAPhaseWithNoTierIsNeverRaised(t *testing.T) {
	r := tierRig(t, plan.Tier{}, plan.Tier{Model: "opus", Effort: "xhigh"})
	r.loop.Tiers = stdTiers
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, Finished: r.now}
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: r.now}
		redRuns(st, "P1", 9, r.now)
	})
	r.run(time.Minute)
	if st := r.state(); st.Raised("P1") != nil {
		t.Fatalf("raised: %+v", st.Phases["P1"].Raises)
	}
	if _, ok := r.loop.Retier(); ok || strings.Contains(r.events(), "tier_") {
		t.Errorf("a phase with no tier moved a tier: %s", r.events())
	}
	x := newCrewOn(t, 1, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}}}})
	x.c.Lead.Tiers = stdTiers
	x.atWork("P1")
	x.sub("P1").Update(func(st *state.State) error { redRuns(st, "P1", 9, x.now); return nil })
	x.tick(time.Second)
	x.tick(time.Second)
	if raised := x.state().Raised("P1"); raised != nil {
		t.Errorf("a phase agent's phase with no tier was raised: %v", raised)
	}
}
