package state

import (
	"testing"
	"time"
)

// Whether a phase agent's work needs it any more is read from the record alone: landed, or done in its
// sub-run, or verifying with no red verdict.
func TestSatisfied(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	w := Worker{Phase: "P3", Status: WorkerHeld}
	for _, c := range []struct {
		name string
		lead PhaseState
		sub  *PhaseState
		job  string // the status of its latest job ("" for none)
		w    string // the worker's status ("" for held)
		want bool
	}{
		{"active", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseActive}, "", "", false},
		{"active, its gate passed", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseActive}, JobPassed, "", false},
		{"verifying, its gate running", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseVerifying}, JobRunning, "", true},
		{"verifying, no gate", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseVerifying}, "", "", true},
		{"verifying, red", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseVerifying}, JobFailed, "", false},
		{"verifying, timed out", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseVerifying}, JobTimeout, "", false},
		{"done in its sub-run", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseDone}, JobPassed, "", true},
		{"parked after red", PhaseState{Status: PhaseWorking}, &PhaseState{Status: PhaseParked}, JobFailed, "", false},
		{"no sub-run", PhaseState{Status: PhaseWorking}, nil, "", "", false},
		{"landed", PhaseState{Status: PhaseDone}, nil, "", WorkerLanded, true},
		{"done in the run", PhaseState{Status: PhaseDone}, &PhaseState{Status: PhaseActive}, "", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, sub := New(), New()
			lead := c.lead
			st.Phases["P3"] = &lead
			if c.sub != nil {
				ps := *c.sub
				sub.Phases["P3"] = &ps
			}
			if c.job != "" {
				sub.Jobs = []Job{{ID: "P3.j1", Phase: "P3", Status: c.job, Queued: now}}
			}
			e := w
			if c.w != "" {
				e.Status = c.w
			}
			if got := Satisfied(st, sub, e); got != c.want {
				t.Errorf("Satisfied = %v, want %v", got, c.want)
			}
		})
	}
}

// Each stop the human makes of a phase agent holds its worker and counts, and queues a question about it;
// the same agent's refusal seen again is no new stop.
func TestStopAgent(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	st := New()
	st.Mode = ModeRunning
	e := Worker{Phase: "P3", Ref: "run/P3", Status: WorkerWorking, Assigned: t0, AgentID: "a1"}
	st.Workers = []Worker{e}
	st.Phases["P3"] = &PhaseState{Status: PhaseWorking, Worker: e.Ref}

	n, fresh := StopAgent(&st, e, "a1", "three", t0)
	w := st.WorkerOn("P3")
	if n != 1 || !fresh || w.Status != WorkerHeld || w.Stops != 1 || w.Stopped != "a1" || !w.StoppedAt.Equal(t0) {
		t.Fatalf("first stop: %d %v, worker %+v", n, fresh, w)
	}
	if len(st.Relay) != 1 || st.Relay[0].Kind != RelayStopped || st.Relay[0].Count != 1 || st.Relay[0].What != "three" || !st.Relay[0].Since.Equal(t0) {
		t.Fatalf("relay %+v", st.Relay)
	}
	r := st.Relay[0]
	if !st.AgentWaits(r) || !st.StopAsked("P3") || !r.Lapses() || r.Persistent() {
		t.Errorf("waits %v, asked %v, lapses %v", st.AgentWaits(r), st.StopAsked("P3"), r.Lapses())
	}
	if _, fresh := StopAgent(&st, e, "a1", "three", t0.Add(time.Second)); fresh || st.WorkerOn("P3").Stops != 1 || len(st.Relay) != 1 {
		t.Errorf("the same agent's stop again counted: %+v", st.WorkerOn("P3"))
	}

	// A fresh agent, stopped too: a second stop, and a second question.
	w = st.WorkerOn("P3")
	w.Status, w.AgentID = WorkerWorking, "a2"
	t1 := t0.Add(20 * time.Minute)
	if n, fresh := StopAgent(&st, e, "a2", "three", t1); n != 2 || !fresh || len(st.Relay) != 2 || st.Relay[1].Count != 2 {
		t.Fatalf("second stop: %d %v, relay %+v", n, fresh, st.Relay)
	}
	if st.AgentWaits(r) {
		t.Error("the first stop's question still waits after the second stop")
	}
	// Taken down: the question with no deadline is what waits, and it is not one baton answers.
	w = st.WorkerOn("P3")
	t2 := t1.Add(11 * time.Minute)
	w.TakenDown = t2
	p := Relay{Phase: "P3", Kind: RelayStopped, Since: t2, Count: 2, Untimed: "only they can say"}
	if !p.Persistent() || p.Lapses() || !st.AgentWaits(p) || st.AgentWaits(st.Relay[1]) {
		t.Errorf("persistent %v, lapses %v, waits %v, the second's waits %v", p.Persistent(), p.Lapses(), st.AgentWaits(p), st.AgentWaits(st.Relay[1]))
	}
	st.Relay = append(st.Relay, p)
	if st.StopAsked("P3") {
		t.Error("the question with no deadline counts as one baton answers")
	}
	// Restarted: its question waits while the fresh agent works, and not once it is held again.
	w.Status, w.Restarted, w.TakenDown = WorkerSuspended, t2, time.Time{}
	q := Relay{Phase: "P3", Kind: RelayRestarted, Since: t2}
	if !st.AgentWaits(q) || !q.Lapses() || st.AgentWaits(p) {
		t.Errorf("restarted waits %v, lapses %v; the persistent one waits %v", st.AgentWaits(q), q.Lapses(), st.AgentWaits(p))
	}
	w.Status = WorkerHeld
	if st.AgentWaits(q) {
		t.Error("a restart's question waits once its agent is held again")
	}
}
