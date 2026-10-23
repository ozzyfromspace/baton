package loop

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A host that runs phase agents records so in its run, and the run's final QA phase goes to an agent: the
// lead done with the last phase before it holds none, and idles, while an agent takes the QA phase up in a
// worktree of its own. Once that agent is at work, the human hears that a review of theirs reaches it in
// time, once.
func TestAPhaseAgentTakesTheFinalQAPhase(t *testing.T) {
	pl := plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}},
		{ID: "QA", Title: "checks", After: []string{"P0", "P1"}, Type: plan.TypeQA}}}
	x := newCrewOn(t, 2, pl)
	x.lead.Update(func(st *state.State) error {
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseDone, DoneAt: x.now}
		return nil
	})
	x.tick(time.Second)
	if st := x.state(); st.Run.Fleet != "inst" || !st.HasAgents() || len(x.spawns()) != 0 {
		t.Fatalf("the host's phase agents not recorded (%q), or an agent started early: %v", st.Run.Fleet, x.spawns())
	}

	var next string
	x.lead.Update(func(st *state.State) error {
		var err error
		next, err = state.Done(st, pl, "P0", x.now, state.DoneOpts{})
		return err
	})
	if st := x.state(); next != "" || st.Current != "" || st.Mode != state.ModeRunning {
		t.Fatalf("the lead took up %q (in hand %q, %s)", next, st.Current, st.Mode)
	}
	x.atWork("QA")
	st := x.state()
	if st.Current != "" || st.Phases["QA"].Status != state.PhaseWorking || !slices.Equal(x.spawns(), []string{"QA"}) {
		t.Fatalf("in hand %q, QA %+v, spawned %v", st.Current, st.Phases["QA"], x.spawns())
	}

	x.tick(reviewEvery)
	x.tick(reviewEvery)
	st = x.state()
	if want := decide.ReviewReadyLine([]string{"QA"}); !slices.Contains(st.Run.Announce, want) {
		t.Errorf("not announced: %q", st.Run.Announce)
	}
	if n := x.count("review_ready", map[string]any{"phases": []string{"QA"}, "agents": true}); n != 1 {
		t.Errorf("review_ready logged %d times:\n%s", n, x.events())
	}
	if st.Run.Review == nil || st.Run.Review.Generation != st.Generation {
		t.Errorf("not recorded as told: %+v", st.Run.Review)
	}
}

// Each host records in its run whether it runs phase agents, over what the host before it recorded.
func TestTheHostRecordsItsAgents(t *testing.T) {
	for _, c := range []struct {
		name      string
		fleet     bool
		before    string
		want      string
		hasAgents bool
	}{
		{"a host with agents", true, "", "inst", true},
		{"after a host with none", true, "old", "inst", true},
		{"recorded already", true, "inst", "inst", true},
		{"a host with none, after one with agents", false, "old", "", false},
		{"a host with none", false, "", "", false},
	} {
		x := newReview(t, plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", After: []string{}}}}, nil, "P0")
		x.lead.Update(func(st *state.State) error { st.Run.Fleet, st.RunBranch = c.before, "main"; return nil })
		if c.fleet {
			x.c.Fleet = &Fleet{Max: 2}
		}
		before, _ := x.lead.Load()
		x.c.fleetOn(x.lead, before)
		st, _ := x.lead.Load()
		if st.Run.Fleet != c.want || st.HasAgents() != c.hasAgents {
			t.Errorf("%s: recorded %q, has agents %v", c.name, st.Run.Fleet, st.HasAgents())
		}
	}
}

// A hold on new phases for a full check waits for a QA phase, which is that check: an agent takes the QA
// phase through it, and a phase that builds waits.
func TestAnAgentTakesAQAPhaseThroughARiskHold(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", After: []string{}}, {ID: "P1", After: []string{}},
		{ID: "P2", After: []string{"P0"}}, {ID: "QA", After: []string{"P0", "P1"}, Type: plan.TypeQA}}}
	for _, c := range []struct {
		name             string
		lead             string // the phase in hand ("" for none)
		done             []string
		phase, why, held string
	}{
		{"the QA phase", "", []string{"P0", "P1"}, "QA", "", ""},
		{"a phase that builds", "P1", []string{"P0"}, "P2", decide.RiskHoldsAgents, state.HoldRisk},
	} {
		st := state.New()
		st.Mode, st.Current, st.RunBranch, st.Run.Fleet = state.ModeRunning, c.lead, "main", "inst"
		st.Risk = &state.Risk{Held: now.Add(-time.Minute)}
		for _, ph := range pl.Phases {
			st.Phases[ph.ID] = &state.PhaseState{Status: state.PhasePending}
		}
		for _, id := range c.done {
			st.Phases[id].Status = state.PhaseDone
		}
		if c.lead != "" {
			st.Phases[c.lead].Status = state.PhaseActive
		}
		if phase, why, held := (&Fleet{Max: 3}).next(st, pl, now, room{}); phase != c.phase || why != c.why || held != c.held {
			t.Errorf("%s: %q, %q, %q", c.name, phase, why, held)
		}
	}
}

// review is a run whose plan is ready for the human's review, or not, and its host's coordinator.
type review struct {
	t    *testing.T
	now  time.Time
	lead *state.Store
	c    *Coordinator
}

func newReview(t *testing.T, pl plan.Plan, statuses map[string]string, current string) *review {
	x := &review{t: t, now: time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)}
	p, err := state.OpenProject(filepath.Join(t.TempDir(), ".baton"), func() time.Time { return x.now })
	if err != nil {
		t.Fatal(err)
	}
	if x.lead, err = p.Bind(state.Binding{Session: "sess-1", Instance: "inst"}); err != nil {
		t.Fatal(err)
	}
	x.lead.WritePlan(pl)
	x.lead.Update(func(st *state.State) error {
		*st = state.Reattach(*st, pl, x.now, state.Origin{})
		for id, s := range statuses {
			st.Phases[id] = &state.PhaseState{Status: s}
		}
		st.Current = current
		return nil
	})
	x.c = &Coordinator{Lead: &Loop{Store: x.lead, Notify: &bomb{}, Project: "demo", Timing: DefaultTiming}, Project: p}
	return x
}

// look lets the coordinator look d after the last, and returns how many times the human was told so far.
func (x *review) look(d time.Duration) int {
	x.now = x.now.Add(d)
	st, err := x.lead.Load()
	if err != nil {
		x.t.Fatal(err)
	}
	x.c.reviewReady(x.now, x.lead, st)
	st, _ = x.lead.Load()
	n := 0
	for _, k := range st.Run.Notices {
		if k.Kind == "review_ready" {
			n++
		}
	}
	return n
}

// The review notice goes out once a plan, at most every reviewEvery, and only once a QA phase is all that
// is left: never for a plan with no QA phase.
func TestReviewReadyOncePerPlan(t *testing.T) {
	qa := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "zero", After: []string{}},
		{ID: "QA", Title: "checks", After: []string{"P0"}, Type: plan.TypeQA}}}
	x := newReview(t, qa, map[string]string{"P0": state.PhaseActive}, "P0")
	if n := x.look(time.Second); n != 0 {
		t.Fatalf("told with P0 under way: %d", n)
	}
	x.lead.Update(func(st *state.State) error {
		st.Phases["P0"].Status, st.Current = state.PhaseDone, "QA" // no agents: the lead holds the QA phase
		return nil
	})
	if n := x.look(time.Second); n != 0 {
		t.Errorf("told %d times within %s of the last look", n, reviewEvery)
	}
	if n := x.look(reviewEvery); n != 1 {
		t.Fatalf("told %d times", n)
	}
	if n := x.look(reviewEvery); n != 1 {
		t.Errorf("told again: %d", n)
	}
	st, _ := x.lead.Load()
	if want := decide.ReviewReadyLine([]string{"QA"}); !slices.Contains(st.Run.Announce, want) || st.Run.Notices[0].Text != decide.ReviewReady([]string{"QA"}) {
		t.Errorf("announced %q, pushed %+v", st.Run.Announce, st.Run.Notices)
	}
	x.lead.Update(func(st *state.State) error { st.Generation++; return nil }) // another plan of the run's
	if n := x.look(reviewEvery); n != 2 {
		t.Errorf("not told for the next plan: %d", n)
	}

	none := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "zero", After: []string{}},
		{ID: "P1", Title: "one", After: []string{"P0"}}}}
	y := newReview(t, none, map[string]string{"P0": state.PhaseDone, "P1": state.PhaseActive}, "P1")
	if n := y.look(reviewEvery); n != 0 {
		t.Errorf("told for a plan with no QA phase: %d", n)
	}
}
