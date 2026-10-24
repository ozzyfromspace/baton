package state

import (
	"reflect"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A run whose lead holds no phase is held by its phase agents' work alone when every phase left is with an
// agent, or a phase not started waits on nothing but agents' work; anything else that holds it (a job, a
// gate here, a phase that can start, an agent not at work) leaves HeldByAgents zero.
func TestHeldByAgents(t *testing.T) {
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{
		{ID: "P0", Title: "Zero", After: []string{}}, {ID: "P1", Title: "One", After: []string{"P0"}}, {ID: "P2", Title: "Two", After: []string{"P0"}},
		{ID: "P3", Title: "Three", After: []string{"P0"}}, {ID: "P4", Title: "Four", After: []string{"P3", "P2"}}, {ID: "P5", Title: "Five", After: []string{"P3"}}}}
	// run is the lead's run with no phase in hand: done phases done, each of agents with a worker of that
	// status, and every other phase pending.
	run := func(done []string, agents map[string]string) State {
		st := New()
		st.Mode, st.Generation = ModeRunning, 1
		for _, ph := range pl.Phases {
			st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
		}
		for _, id := range done {
			st.Phases[id].Status = PhaseDone
		}
		for _, ph := range pl.Phases {
			if status, ok := agents[ph.ID]; ok {
				st.Phases[ph.ID] = &PhaseState{Status: PhaseWorking, Worker: "run/" + ph.ID, StartedAt: t0}
				st.Workers = append(st.Workers, Worker{Phase: ph.ID, Ref: "run/" + ph.ID, Status: status, Assigned: t0, Generation: 1})
			}
		}
		return st
	}
	for _, c := range []struct {
		name  string
		st    State
		setup func(*State)
		want  AgentsHold
	}{
		{"the last phase with an agent", run([]string{"P0", "P1", "P2", "P3", "P4"}, map[string]string{"P5": WorkerWorking}), nil,
			AgentsHold{AtWork: []string{"P5"}, Last: true}},
		{"two last phases, one landing and one starting", run([]string{"P0", "P1", "P2", "P3"}, map[string]string{"P4": WorkerLanding, "P5": WorkerStarting}), nil,
			AgentsHold{AtWork: []string{"P4", "P5"}, Last: true}},
		{"phases wait on an agent's", run([]string{"P0", "P1", "P2"}, map[string]string{"P3": WorkerWorking}), nil,
			AgentsHold{AtWork: []string{"P3"}, Behind: []Behind{{Phase: "P4", On: []string{"P3"}}, {Phase: "P5", On: []string{"P3"}}}}},
		{"a phase waits on two agents', in plan order", run([]string{"P0", "P1"}, map[string]string{"P2": WorkerWorking, "P3": WorkerWorking}), nil,
			AgentsHold{AtWork: []string{"P2", "P3"}, Behind: []Behind{{Phase: "P4", On: []string{"P2", "P3"}}, {Phase: "P5", On: []string{"P3"}}}}},
		{"a phase waits on a held agent's too", run([]string{"P0", "P1"}, map[string]string{"P2": WorkerHeld, "P3": WorkerWorking}), nil,
			AgentsHold{AtWork: []string{"P3"}, Behind: []Behind{{Phase: "P5", On: []string{"P3"}}}}},
		{"a phase can start", run([]string{"P0", "P1"}, map[string]string{"P3": WorkerWorking}), nil, AgentsHold{}},
		{"the last phase's agent is suspended", run([]string{"P0", "P1", "P2", "P3", "P4"}, map[string]string{"P5": WorkerSuspended}), nil, AgentsHold{}},
		{"an earlier plan's agent", run([]string{"P0", "P1", "P2", "P3", "P4"}, map[string]string{"P5": WorkerWorking}),
			func(st *State) { st.Workers[0].Generation = 0 }, AgentsHold{}},
		{"a job runs", run([]string{"P0", "P1", "P2", "P3", "P4"}, map[string]string{"P5": WorkerWorking}),
			func(st *State) {
				st.Phases["P4"].Status = PhaseVerifying
				st.Jobs = []Job{{ID: "j1", Phase: "P4", Kind: JobVerify, Command: "make test", Status: JobRunning}}
			}, AgentsHold{}},
		{"a gate runs here", run([]string{"P0", "P1", "P2", "P3", "P4"}, map[string]string{"P5": WorkerWorking}),
			func(st *State) {
				st.Jobs = []Job{{ID: "j1", Phase: "P4", Kind: JobVerify, Command: "make e2e", Status: JobRunning, Here: true}}
			}, AgentsHold{}},
		{"the lead holds a phase", run([]string{"P0", "P1", "P2"}, map[string]string{"P3": WorkerWorking}),
			func(st *State) { st.Current, st.Phases["P4"].Status = "P4", PhaseActive }, AgentsHold{}},
		{"the run is paused", run([]string{"P0", "P1", "P2", "P3", "P4"}, map[string]string{"P5": WorkerWorking}),
			func(st *State) { st.Mode = ModePaused }, AgentsHold{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.setup != nil {
				c.setup(&c.st)
			}
			got := HeldByAgents(c.st, pl)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("held %+v\nwant %+v", got, c.want)
			}
			if got.Holds() != (c.want.Last || len(c.want.Behind) > 0) {
				t.Errorf("holds %v", got.Holds())
			}
		})
	}
}
