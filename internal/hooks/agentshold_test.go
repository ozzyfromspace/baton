package hooks

import (
	"reflect"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The lead's stop with no phase in hand names the phase agents the run waits on, and says nothing waits on
// the human, when their work alone holds it: every phase left is with an agent, or a phase waits on an
// agent's work. Anything else that holds it (a job, a gate here) keeps the line it had.
func TestTheLeadsStopNamesTheAgentsItWaitsOn(t *testing.T) {
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "Zero", After: []string{}},
		{ID: "P1", Title: "One", After: []string{"P0"}}, {ID: "P2", Title: "Two", After: []string{"P0"}}, {ID: "P3", Title: "Three", After: []string{"P0"}},
		{ID: "P4", Title: "Four", After: []string{"P0"}}, {ID: "P5", Title: "Five", After: []string{"P3"}}}}
	at := tStop.Add(-time.Hour)
	agent := func(st *state.State, phase string) {
		st.Phases[phase] = &state.PhaseState{Status: state.PhaseWorking, Worker: "run/" + phase, StartedAt: at}
		st.Workers = append(st.Workers, state.Worker{Phase: phase, Ref: "run/" + phase, Status: state.WorkerWorking, Assigned: at, Generation: st.Generation})
	}
	job := state.Job{ID: "j1", Phase: "P3", Kind: state.JobVerify, Command: "make test", Status: state.JobRunning}
	gate := state.Job{ID: "j1", Phase: "P3", Kind: state.JobVerify, Command: "make e2e", Status: state.JobRunning, Here: true}
	for _, c := range []struct {
		name    string
		pending []string // phases left pending; every other phase not set below is done
		setup   func(*state.State)
		say     string
		logged  map[string]any // idle_on_agents, or nil for none
	}{
		{"the last phase with an agent", nil, func(st *state.State) { agent(st, "P5") },
			"baton: P5, the last phase, is with its phase agent — baton says here when it lands and the plan is complete. Nothing waits on you.",
			map[string]any{"agents": []string{"P5"}, "behind": []string(nil), "last": true}},
		{"two last phases", nil, func(st *state.State) { agent(st, "P4"); agent(st, "P5") },
			"baton: P4 and P5, the last phases, are with phase agents — baton says here when they land and the plan is complete. Nothing waits on you.",
			map[string]any{"agents": []string{"P4", "P5"}, "behind": []string(nil), "last": true}},
		{"a phase waiting on an agent's", []string{"P5"}, func(st *state.State) { agent(st, "P3") },
			"baton: no phase can start until P3's agent lands its work (P5 waits on it) — baton takes the next one up then. Nothing waits on you.",
			map[string]any{"agents": []string{"P3"}, "behind": []string{"P5"}, "last": false}},
		{"jobs only", []string{"P5"}, func(st *state.State) {
			st.Phases["P3"].Status, st.Jobs = state.PhaseVerifying, []state.Job{job}
		}, decide.NoPhaseYet("job j1 (`make test`, P3)", nil), nil},
		{"a job beside an agent's work", []string{"P5"}, func(st *state.State) {
			st.Phases["P3"].Status, st.Jobs = state.PhaseVerifying, []state.Job{job}
			agent(st, "P4")
		}, decide.NoPhaseYet("job j1 (`make test`, P3)", nil), nil},
		{"a gate here", []string{"P5"}, func(st *state.State) {
			st.Phases["P3"].Status, st.Jobs = state.PhaseVerifying, []state.Job{gate}
			agent(st, "P4")
		}, decide.NoPhaseYet("job j1 (`make e2e`, P3)", &gate), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := state.Attach(pl, at, state.Origin{})
			st.Current = ""
			for _, ph := range pl.Phases {
				st.Phases[ph.ID] = &state.PhaseState{Status: state.PhaseDone, StartedAt: at}
			}
			for _, id := range c.pending {
				st.Phases[id] = &state.PhaseState{Status: state.PhasePending}
			}
			c.setup(&st)
			d := decideStop(&st, pl, tStop, decide.Words{Git: true, Timeout: 10 * time.Minute}, nil)
			if d.output["decision"] == "block" {
				t.Fatalf("the stop was refused: %v", d.output)
			}
			if got := said(d.output); got != c.say {
				t.Errorf("said %q\nwant %q", got, c.say)
			}
			var got map[string]any
			for _, e := range d.events {
				if e.kind == "idle_on_agents" {
					got = e.fields
				}
			}
			if !reflect.DeepEqual(got, c.logged) {
				t.Errorf("logged %v, want %v", got, c.logged)
			}
		})
	}
}
