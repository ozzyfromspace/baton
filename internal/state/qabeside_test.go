package state

import (
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// qaGraph is graph, with the phases named in qa marked `Type: qa`.
func qaGraph(qa []string, specs ...string) plan.Plan {
	p := graph(specs...)
	for i := range p.Phases {
		if slices.Contains(qa, p.Phases[i].ID) {
			p.Phases[i].Type = plan.TypeQA
		}
	}
	return p
}

// withAgents makes st a run whose host, inst, runs phase agents beside the lead, on the branch main.
func withAgents(st *State) {
	st.Owner, st.Run.Fleet, st.RunBranch, st.Generation = &Owner{Instance: "inst"}, "inst", "main", 2
}

// Where the run has phase agents, the lead passes over a QA phase that can start, and an agent takes it,
// even as the plan's last phase; with none, or once an agent of the plan failed on it, it is the lead's, as
// is one the lead started. A QA phase in the middle of the plan goes the same way, and the lead takes what
// else can start.
func TestNextWorkerTakesAQAPhase(t *testing.T) {
	final := qaGraph([]string{"QA"}, "P0:", "P1:", "QA:P0,P1")
	middle := qaGraph([]string{"Q1"}, "P0:", "Q1:P0", "P2:Q1", "P3:")
	const done, working, parked = PhaseDone, PhaseWorking, PhaseParked
	failed := func(gen int) func(*State) {
		return func(st *State) {
			st.Workers = append(st.Workers, Worker{Phase: "QA", Ref: "r1/QA", Status: WorkerFailed, Generation: gen})
		}
	}
	for _, c := range []struct {
		name       string
		p          plan.Plan
		statuses   map[string]string
		agents     bool
		edit       func(*State)
		next, want string // the lead's next phase (Next), and an agent's (NextWorker)
	}{
		{"no agents: the final QA phase is the lead's", final, map[string]string{"P0": done, "P1": done}, false, nil, "QA", ""},
		{"agents: an agent takes the final QA phase", final, map[string]string{"P0": done, "P1": done}, true, nil, "", "QA"},
		{"agents: a QA phase that cannot start yet is nobody's", final, map[string]string{"P0": done, "P1": working}, true, nil, "", ""},
		{"serial: the lead's", final, map[string]string{"P0": done, "P1": done}, true, func(st *State) {
			st.Run.Serial = &Serial{Instance: "inst", Why: "no mod"}
		}, "QA", ""},
		{"the host runs no agents: the lead's", final, map[string]string{"P0": done, "P1": done}, true, func(st *State) {
			st.Run.Fleet = ""
		}, "QA", ""},
		{"no branch for an agent's work: the lead's", final, map[string]string{"P0": done, "P1": done}, true, func(st *State) {
			st.RunBranch = ""
		}, "QA", ""},
		{"an agent of the plan failed on it: the lead's", final, map[string]string{"P0": done, "P1": done}, true, failed(2), "QA", ""},
		{"an agent of a replaced plan failed on it: an agent's", final, map[string]string{"P0": done, "P1": done}, true, failed(1), "", "QA"},
		{"the lead set it aside: the lead's", final, map[string]string{"P0": done, "P1": done, "QA": parked}, true, nil, "QA", ""},
		{"no agents: a QA phase in the middle is the lead's", middle, map[string]string{"P0": done}, false, nil, "Q1", "P3"},
		{"agents: the lead takes what else can start", middle, map[string]string{"P0": done}, true, nil, "P3", "Q1"},
		{"agents: and with nothing else, idles", middle, map[string]string{"P0": done, "P3": done}, true, nil, "", "Q1"},
	} {
		st := runState(c.p, c.statuses, map[string]Park{"QA": {Reason: ParkCheckpoint, Notes: "half way"}})
		if c.agents {
			withAgents(&st)
		}
		if c.edit != nil {
			c.edit(&st)
		}
		if got := Next(st, c.p); got != c.next {
			t.Errorf("%s: Next = %q, want %q", c.name, got, c.next)
		}
		if got := NextWorker(st, c.p); got != c.want {
			t.Errorf("%s: NextWorker = %q, want %q", c.name, got, c.want)
		}
	}
}

// The lead done with the last phase before a final QA phase takes it up after a compaction where the run
// has no agents; with agents, it holds no phase, and the plan is not complete. An agent that fails on the
// QA phase gives it back to the lead, which takes it up.
func TestTheLeadTakesAQAPhaseOnlyWithoutAgents(t *testing.T) {
	p := qaGraph([]string{"QA"}, "P0:", "QA:P0")
	for _, agents := range []bool{false, true} {
		st := runState(p, map[string]string{"P0": PhaseActive}, nil)
		st.Current = "P0"
		if agents {
			withAgents(&st)
		}
		next, err := Done(&st, p, "P0", t0, DoneOpts{})
		switch {
		case err != nil:
			t.Fatalf("agents %v: %v", agents, err)
		case !agents && (next != "QA" || st.Current != "QA" || !st.BoundaryOwed):
			t.Errorf("no agents: next %q, in hand %q, boundary owed %v", next, st.Current, st.BoundaryOwed)
		case agents && (next != "" || st.Current != "" || st.BoundaryOwed || st.Mode != ModeRunning):
			t.Errorf("agents: next %q, in hand %q, boundary owed %v, mode %s", next, st.Current, st.BoundaryOwed, st.Mode)
		}
		if !agents {
			continue
		}
		if got := NextWorker(st, p); got != "QA" {
			t.Fatalf("an agent's next phase: %q", got)
		}
		if err := Assign(&st, p, worker("QA"), t0); err != nil {
			t.Fatal(err)
		}
		took, err := Unassign(&st, p, "QA", "its agent did not start", t0.Add(time.Minute))
		if err != nil || took != "QA" || st.Current != "QA" || st.Run.Compaction.Reason != "boundary" {
			t.Errorf("an agent failed on QA: took %q (%v), in hand %q, compaction %+v", took, err, st.Current, st.Run.Compaction)
		}
	}
}

// The run has agents while its host runs them, it did not go serial, and it has a branch. The last host's
// word stands until the next has recorded its own.
func TestHasAgents(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*State)
		want bool
	}{
		{"the owner runs them", nil, true},
		{"no host runs them", func(st *State) { st.Run.Fleet = "" }, false},
		{"the host before ran them, and the next has not said yet", func(st *State) { st.Owner.Instance = "next" }, true},
		{"no owner", func(st *State) { st.Owner = nil }, true},
		{"the owner went serial", func(st *State) { st.Run.Serial = &Serial{Instance: "inst", Why: "no mod"} }, false},
		{"another host went serial", func(st *State) { st.Run.Serial = &Serial{Instance: "old", Why: "no mod"} }, true},
		{"no branch", func(st *State) { st.RunBranch = "" }, false},
	} {
		st := New()
		withAgents(&st)
		if c.edit != nil {
			c.edit(&st)
		}
		if got := st.HasAgents(); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// The plan is ready for the human's review once every phase but its QA phases is done, and a QA phase's
// session takes their words: the lead's, or an agent's at work on it. Once a plan; never for a plan with no
// QA phase, or with nothing but QA phases.
func TestReviewReady(t *testing.T) {
	final := qaGraph([]string{"QA"}, "P0:", "P1:", "QA:P0,P1")
	two := qaGraph([]string{"QA", "QB"}, "P0:", "QA:P0", "QB:P0")
	none := graph("P0:", "P1:")
	onlyQA := qaGraph([]string{"QA"}, "QA:")
	const done, working = PhaseDone, PhaseWorking
	agent := func(phase, status, id string) func(*State) {
		return func(st *State) {
			st.Phases[phase] = &PhaseState{Status: working, Worker: "r1/" + phase}
			st.Workers = append(st.Workers, Worker{Phase: phase, Ref: "r1/" + phase, Status: status, AgentID: id, Generation: st.Generation})
		}
	}
	for _, c := range []struct {
		name     string
		p        plan.Plan
		statuses map[string]string
		edit     func(*State)
		want     []string
	}{
		{"a phase before the QA phase is not done", final, map[string]string{"P0": done, "P1": working}, nil, nil},
		{"no agent takes the QA phase yet", final, map[string]string{"P0": done, "P1": done}, nil, nil},
		{"its agent is starting", final, map[string]string{"P0": done, "P1": done}, agent("QA", WorkerStarting, ""), nil},
		{"its agent is at work", final, map[string]string{"P0": done, "P1": done}, agent("QA", WorkerWorking, "a1"), []string{"QA"}},
		{"its agent is being let go", final, map[string]string{"P0": done, "P1": done}, func(st *State) {
			agent("QA", WorkerWorking, "a1")(st)
			st.Workers[0].Retiring = t0
		}, nil},
		{"the lead holds it", final, map[string]string{"P0": done, "P1": done}, func(st *State) { st.Current = "QA" }, []string{"QA"}},
		{"the lead set it aside", final, map[string]string{"P0": done, "P1": done, "QA": PhaseParked}, nil, []string{"QA"}},
		{"it is done", final, map[string]string{"P0": done, "P1": done, "QA": done}, nil, nil},
		{"told for this plan", final, map[string]string{"P0": done, "P1": done}, func(st *State) {
			st.Current = "QA"
			ToldReview(st, t0)
		}, nil},
		{"told for the plan before", final, map[string]string{"P0": done, "P1": done}, func(st *State) {
			st.Current = "QA"
			ToldReview(st, t0)
			st.Generation++
		}, []string{"QA"}},
		{"the run is paused", final, map[string]string{"P0": done, "P1": done}, func(st *State) {
			st.Current, st.Mode = "QA", ModePaused
		}, nil},
		{"two QA phases: the one whose agent is at work", two, map[string]string{"P0": done}, agent("QB", WorkerWorking, "a1"), []string{"QB"}},
		{"two QA phases, both at work", two, map[string]string{"P0": done}, func(st *State) {
			agent("QA", WorkerWorking, "a1")(st)
			agent("QB", WorkerWorking, "a2")(st)
		}, []string{"QA", "QB"}},
		{"no QA phase", none, map[string]string{"P0": done, "P1": PhaseActive}, func(st *State) { st.Current = "P1" }, nil},
		{"nothing but a QA phase", onlyQA, nil, func(st *State) { st.Current = "QA" }, nil},
	} {
		st := runState(c.p, c.statuses, nil)
		st.Generation = 1
		if c.edit != nil {
			c.edit(&st)
		}
		if got := ReviewReady(st, c.p); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
