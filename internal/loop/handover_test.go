package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// handCrew is a crew whose run has the phases of pl, each on opus/high, which the lead runs on: it can
// restart in place, raises a phase after 3 red runs, and holds lead, which started on the commit there is
// with nothing uncommitted; the agents on agents are at work, as its last Stop reported.
func handCrew(t *testing.T, pl plan.Plan, lead string, done []string, agents ...string) *crew {
	t.Helper()
	x := newCrewOn(t, 2, pl)
	x.now = time.Now().UTC() // git dates the lead's commits by the wall clock, which the brief reads them against
	l := x.c.Lead
	l.Tiers, l.Launch, l.Retiers, l.Version = stdTiers, plan.Tier{Model: "opus", Effort: "high"}, true, "v0.8.20"
	head := gitx.Head(x.repo)
	x.lead.Update(func(st *state.State) error {
		for _, id := range done {
			st.Phases[id] = &state.PhaseState{Status: state.PhaseDone, DoneAt: x.now}
		}
		if st.Current != lead {
			st.Phases[st.Current] = &state.PhaseState{Status: state.PhasePending}
		}
		st.Current = lead
		st.Phases[lead] = &state.PhaseState{Status: state.PhaseActive, StartedAt: x.now, StartHead: head, StartDirty: &state.Dirt{}, Edited: []string{"zero.go"}}
		st.Run.SessionID = "sess-1"
		return nil
	})
	x.atWork(agents...)
	x.lead.Update(func(st *state.State) error {
		for _, phase := range agents {
			st.Run.Background = append(st.Run.Background, state.Task{ID: x.agent(phase).id, Type: "subagent", AgentType: state.PhaseAgentAt("high"), Status: "running"})
		}
		return nil
	})
	return x
}

// threePlan is P0, the lead's, P1 beside it, and P2 after P0, each on opus/high.
func threePlan() plan.Plan {
	return plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}, Model: "opus", Effort: "high"},
		{ID: "P1", Title: "one", After: []string{}, Model: "opus", Effort: "high"},
		{ID: "P2", Title: "two", After: []string{"P0"}, Model: "opus", Effort: "high"},
	}}
}

// redGates adds 3 red runs of phase's gate to the lead's run, the last with notes and a log that names
// the test that failed: the lead raises the phase a step.
func (x *crew) redGates(phase string) {
	x.t.Helper()
	x.lead.Update(func(st *state.State) error {
		for i := range 3 {
			id := fmt.Sprintf("j%d", len(st.Jobs)+1)
			j := state.Job{ID: id, Phase: phase, Kind: state.JobVerify, Command: "go test ./...", Status: state.JobFailed, Exit: 1,
				Queued: x.now, Started: x.now, Ended: x.now.Add(time.Millisecond), EnvRead: x.now.Add(time.Millisecond)}
			if i == 2 {
				j.Notes = "the parser is half done"
				path := x.lead.JobLog(id)
				os.MkdirAll(filepath.Dir(path), 0o755)
				os.WriteFile(path, []byte("=== RUN   TestParse\n--- FAIL: TestParse (0.01s)\n    parse_test.go:12: got 1, want 2\nFAIL\n"), 0o644)
			}
			st.Jobs = append(st.Jobs, j)
		}
		return nil
	})
}

// commit commits name, written with text, in the lead's checkout, as the lead's work.
func (x *crew) commit(name, text, subject string) {
	x.t.Helper()
	gittest.Write(x.t, x.repo, name, text)
	gittest.Commit(x.t, x.repo, subject)
}

// The phase in hand, raised while a phase agent is at work, goes to an agent on its raised tier rather
// than waiting for a restart the agent at work keeps from coming: at the lead's idle moment, its work
// committed, it is set aside (phase_handed), and an agent is spawned on opus/xhigh from the run's branch,
// whose brief says the lead started it, why it is the agent's, the lead's commit and notes, and the gate's
// failure. Its history stays on the lead's run, and the lead's red gates no longer stand for it.
func TestARaisedPhaseInHandGoesToAnAgentOnItsTier(t *testing.T) {
	x := handCrew(t, threePlan(), "P0", nil, "P1")
	x.commit("zero.go", "package zero\n", "feat: the parser's first half")
	x.redGates("P0")
	x.tick(time.Second)
	st := x.state()
	if ps := st.Phases["P0"]; ps.Status != state.PhaseParked || ps.Park == nil || ps.Park.Reason != state.ParkHanded || ps.Park.Notes != "the parser is half done" {
		t.Fatalf("P0 was not set aside for an agent: %+v\n%s", ps, x.events())
	}
	if st.Current != "" || st.BoundaryOwed {
		t.Errorf("the lead holds %q (boundary owed %v), with nothing else to take up", st.Current, st.BoundaryOwed)
	}
	if !x.logged("phase_handed", map[string]any{"phase": "P0", "tier": "opus/xhigh", "lead": "opus/high", "from": "in hand", "job": "j3"}) {
		t.Errorf("no phase_handed: %s", x.events())
	}
	if a := strings.Join(st.Run.Announce, "\n"); !strings.Contains(a, "baton set P0 aside for a phase agent on opus/xhigh") {
		t.Errorf("announced %q", a)
	}
	x.tick(x.c.Fleet.Ramp)
	x.tick(time.Second)
	if x.status("P0") != state.WorkerWorking {
		t.Fatalf("no agent took P0: %+v\n%s", x.state().Workers, x.events())
	}
	if w := x.worker("P0"); w.Tier != (plan.Tier{Model: "opus", Effort: "xhigh"}) {
		t.Errorf("P0's agent is on %s", w.Tier)
	}
	if got := x.spawnCmds[len(x.spawnCmds)-1]; got.Phase != "P0" || got.Effort != "xhigh" || got.Model != "opus" {
		t.Errorf("P0's spawn: %+v", got)
	}
	if !x.logged("worker_spawned", map[string]any{"phase": "P0", "tier": "opus/xhigh", "handed": true}) || x.count("phase_handed", nil) != 1 {
		t.Errorf("events: %s", x.events())
	}
	brief := x.briefs[x.agent("P0").id]
	for _, want := range []string{
		"The lead's session started P0, and baton gave it to you, on opus/xhigh: baton raised it to opus/xhigh after 3 red runs",
		"the lead runs on opus/high",
		"feat: the parser's first half",
		"the parser is half done",
		"--- FAIL: TestParse",
		"Now: carry P0 — zero on from where the lead's session left it",
		"You run on opus/xhigh: baton raised P0 to it.",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the agent's brief lacks %q:\n%s", want, brief)
		}
	}
	st = x.state()
	if ps := st.Phases["P0"]; ps.Status != state.PhaseWorking || len(ps.Raises) != 1 || ps.Handed == nil || len(ps.Edited) != 1 {
		t.Errorf("P0's history on the lead's run: %+v", ps)
	}
	if red := st.Red(); len(red) != 0 {
		t.Errorf("the lead's red gates still stand for P0: %+v", red)
	}
	if strings.Contains(x.events(), `"kind":"tier_restart"`) || strings.Contains(x.events(), `"kind":"tier_given_up"`) {
		t.Errorf("the lead restarted or gave up: %s", x.events())
	}
}

// With its work uncommitted, the phase in hand waits as before: the human is told once what holds it, the
// wait for a restart gives up after TierWait, and once its work is committed it goes to an agent.
func TestAHandOverWaitsForUncommittedWork(t *testing.T) {
	x := handCrew(t, threePlan(), "P0", nil, "P1")
	gittest.Write(t, x.repo, "a.txt", "changed\n")
	x.redGates("P0")
	for range 3 {
		x.tick(time.Second)
	}
	st := x.state()
	if st.Current != "P0" || st.Phases["P0"].Status != state.PhaseActive || x.logged("phase_handed", nil) {
		t.Fatalf("set aside with its work uncommitted: %+v", st.Phases["P0"])
	}
	if !x.logged("handover_held", map[string]any{"phase": "P0", "why": "uncommitted work", "files": 1}) || x.count("handover_held", nil) != 1 {
		t.Errorf("handover_held: %s", x.events())
	}
	if a := strings.Join(st.Run.Announce, "\n"); strings.Count(a, "once your checkout has no uncommitted change") != 1 {
		t.Errorf("announced %q", a)
	}
	x.tick(x.c.Lead.Timing.TierWait)
	if !x.logged("tier_given_up", map[string]any{"phase": "P0"}) {
		t.Errorf("the wait did not give up: %s", x.events())
	}
	x.commit("a.txt", "changed\n", "feat: committed at last")
	x.tick(handRecheck)
	if !x.logged("phase_handed", map[string]any{"phase": "P0", "tier": "opus/xhigh"}) {
		t.Errorf("once committed, P0 was not set aside: %+v", x.state().Phases["P0"])
	}
}

// A phase that waits for every phase before it stays the lead's whatever its tier, with agents at work: it
// waits for a restart as before, and the wait gives up after TierWait.
func TestAPhaseAfterAllStaysTheLeadsWhenRaised(t *testing.T) {
	pl := plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}, Model: "opus", Effort: "high"},
		{ID: "P1", Title: "one", After: []string{}, Model: "opus", Effort: "high"},
		{ID: "P2", Title: "release", After: []string{"P0", "P1"}, Model: "opus", Effort: "high"},
		{ID: "P3", Title: "three", After: []string{}, Model: "opus", Effort: "high"},
	}}
	x := handCrew(t, pl, "P2", []string{"P0", "P1"}, "P3")
	x.redGates("P2")
	x.tick(time.Second)
	if st := x.state(); st.Current != "P2" || st.Phases["P2"].Status != state.PhaseActive || x.logged("phase_handed", nil) {
		t.Fatalf("a phase after all was set aside: %+v", st.Phases["P2"])
	}
	if !x.logged("handover_held", map[string]any{"phase": "P2", "why": "it waits for every phase before it, so it is the lead's"}) {
		t.Errorf("events: %s", x.events())
	}
	x.tick(x.c.Lead.Timing.TierWait)
	if !x.logged("tier_given_up", map[string]any{"phase": "P2"}) {
		t.Errorf("the wait did not give up: %s", x.events())
	}
}

// A boundary owed into a parked phase an agent takes up (raised, the lead's tier not its own) is turned
// before its compaction is typed: the lead holds none, and an agent takes the phase up on its tier, from
// the lead's park.
func TestABoundaryIntoAPhaseForAnAgentIsTurned(t *testing.T) {
	x := handCrew(t, threePlan(), "P0", nil, "P1")
	x.redGates("P0")
	x.lead.Update(func(st *state.State) error {
		ps := st.Phases["P0"]
		ps.Status, ps.Park = state.PhaseParked, &state.Park{Reason: state.ParkRed, Since: x.now, Notes: "the parser is half done", Job: "j3"}
		ps.Raises = []state.TierRaise{{From: plan.Tier{Model: "opus", Effort: "high"}, To: plan.Tier{Model: "opus", Effort: "xhigh"},
			Plan: plan.Tier{Model: "opus", Effort: "high"}, Why: "3 red runs of its gate", Reds: 3, At: x.now}}
		st.OweBoundary(x.now)
		st.Run.Compaction.Queue("boundary", x.now)
		return nil
	})
	x.tick(time.Second)
	st := x.state()
	if st.Current != "" || st.BoundaryOwed || st.Run.Compaction.Status != state.CompactCancelled {
		t.Fatalf("the boundary into P0 stands: in hand %q, compaction %+v", st.Current, st.Run.Compaction)
	}
	if !x.logged("boundary_redirected", map[string]any{"phase": "P0", "tier": "opus/xhigh", "next": ""}) {
		t.Errorf("events: %s", x.events())
	}
	x.tick(x.c.Fleet.Ramp)
	x.tick(time.Second)
	if w := x.worker("P0"); w.Status != state.WorkerWorking || w.Tier != (plan.Tier{Model: "opus", Effort: "xhigh"}) {
		t.Fatalf("P0's agent: %+v\n%s", w, x.events())
	}
	if !x.logged("phase_handed", map[string]any{"phase": "P0", "tier": "opus/xhigh", "from": "parked", "park": state.ParkRed, "job": "j3"}) {
		t.Errorf("events: %s", x.events())
	}
	if b := x.briefs[x.agent("P0").id]; !strings.Contains(b, "The lead's session started P0") || !strings.Contains(b, "--- FAIL: TestParse") {
		t.Errorf("the agent's brief:\n%s", b)
	}
	if strings.Contains(x.events(), `"kind":"tier_waits"`) {
		t.Errorf("the boundary waited for a restart: %s", x.events())
	}
}
