package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The human's words for an agent are queued once, however often the same prompt is sent while they wait,
// and taken off by when they were written.
func TestTheHumansWordsAreQueuedOnce(t *testing.T) {
	st := New()
	a := HumanWords{Phase: "P2", Agent: "a2", Text: "write notes.md", Prompt: "@P2 write notes.md", At: t0}
	if !QueueWords(&st, a) {
		t.Fatal("not queued")
	}
	again := a
	again.At = t0.Add(time.Second)
	if QueueWords(&st, again) {
		t.Error("the same words queued twice")
	}
	other := HumanWords{Phase: "P3", Agent: "a3", Text: "write notes.md", At: t0.Add(2 * time.Second)}
	if !QueueWords(&st, other) || len(st.ToAgents) != 2 {
		t.Fatalf("queued %+v", st.ToAgents)
	}
	if got, ok := TakeWords(&st, t0); !ok || got.Phase != "P2" || len(st.ToAgents) != 1 {
		t.Errorf("took %+v %v, left %+v", got, ok, st.ToAgents)
	}
	if _, ok := TakeWords(&st, t0); ok {
		t.Error("taken twice")
	}
}

// Where the human's words for a phase go follows where it stands: to its agent only while one works on it
// that baton's mod has and nobody is retiring; otherwise the lead takes them, for why.
func TestWhereTheHumansWordsGo(t *testing.T) {
	known := func(id string) bool { return id == "a2" }
	base := func() State {
		st := New()
		st.Mode, st.Current, st.Generation = ModeRunning, "P1", 1
		st.Phases = map[string]*PhaseState{
			"P0": {Status: PhaseDone}, "P1": {Status: PhaseActive}, "P2": {Status: PhaseWorking}, "P3": {Status: PhasePending},
			"P4": {Status: PhaseVerifying}, "P5": {Status: PhasePending}, "P6": {Status: PhaseWorking},
		}
		st.Workers = []Worker{{Phase: "P2", Ref: "r/P2", AgentID: "a2", Status: WorkerWorking, Generation: 1}}
		return st
	}
	for _, c := range []struct {
		name  string
		set   func(st *State)
		phase string
		to    string
		agent string
	}{
		{"its agent at work", nil, "P2", ToAgent, "a2"},
		{"its agent the mod does not have", func(st *State) { st.Workers[0].AgentID = "a9" }, "P2", ToUnknown, ""},
		{"its agent stopped by the human", func(st *State) { st.Workers[0].Status = WorkerHeld }, "P2", ToStopped, ""},
		{"its agent starting", func(st *State) { st.Workers[0].Status, st.Workers[0].AgentID = WorkerStarting, "" }, "P2", ToStarting, ""},
		{"its agent replaced", func(st *State) { st.Workers[0].Status = WorkerSuspended }, "P2", ToReplaced, ""},
		{"its work waiting to land", func(st *State) { st.Workers[0].Status = WorkerLanding }, "P2", ToLanding, ""},
		{"its worker retiring", func(st *State) { st.Workers[0].Retiring = t0 }, "P2", ToRetiring, ""},
		{"its worker of an earlier plan", func(st *State) { st.Workers[0].Generation = 0 }, "P2", ToNone, ""},
		{"done", nil, "P0", ToDone, ""},
		{"done, an agent's work landed", func(st *State) { st.Landed = []Landing{{Phase: "P0"}} }, "P0", ToLanded, ""},
		{"done, recorded landed by the lead", func(st *State) { st.Landed = []Landing{{Phase: "P0", By: LandedByLead}} }, "P0", ToDone, ""},
		{"the lead's in hand", nil, "P1", ToLead, ""},
		{"the lead's next, at its boundary", func(st *State) { st.Current, st.BoundaryOwed = "P3", true }, "P3", ToLead, ""},
		{"the lead's, verifying", nil, "P4", ToLead, ""},
		{"not started", nil, "P5", ToPending, ""},
		{"working, with no worker", nil, "P6", ToNone, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := base()
			if c.set != nil {
				c.set(&st)
			}
			if to, agent := Addressee(st, c.phase, known); to != c.to || agent != c.agent {
				t.Errorf("%s: to %q %q, want %q %q", c.phase, to, agent, c.to, c.agent)
			}
		})
	}
}

// An agent baton let go of is recorded by its id, as a link to its sub-run: found while that sub-run is
// there, its waking recorded once, and swept with it.
func TestAnAgentLetGoIsRecorded(t *testing.T) {
	p, _ := newProject(t)
	lead := bind(t, p, Binding{Session: "lead", Instance: "host"})
	withPlan(t, lead, "Plan")
	w := newWorker(t, p, lead, "P1")
	for _, c := range []struct{ ref, agent string }{{"lead/P1", ""}, {"lead/P1", "../a1"}, {"lead", "a1"}} {
		if err := p.LetGo(c.ref, c.agent); err == nil {
			t.Errorf("recorded %+v", c)
		}
	}
	if p.LetGoOf("a1") != "" || p.Woke("a1") {
		t.Fatal("an agent never let go is found")
	}
	if err := p.LetGo("lead/P1", "a1"); err != nil {
		t.Fatal(err)
	}
	if p.LetGoOf("a1") != "lead/P1" || p.LetGoOf("a2") != "" {
		t.Errorf("found %q %q", p.LetGoOf("a1"), p.LetGoOf("a2"))
	}
	if !p.Woke("a1") || p.Woke("a1") {
		t.Error("its waking is not recorded once")
	}
	os.Remove(filepath.Join(w.Dir, treeFile)) // P1's sub-run is gone
	if p.LetGoOf("a1") != "" {
		t.Error("found with its sub-run gone")
	}
	p.Sweep()
	if exists(p.path(retiredDir, "a1")) || exists(p.path(retiredDir, "a1"+wokeSuffix)) {
		t.Error("not swept with its sub-run")
	}
}
