package state

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
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
	if !st.AgentWaits(r) || !st.StopAsked("P3") || r.Persistent() {
		t.Errorf("waits %v, asked %v", st.AgentWaits(r), st.StopAsked("P3"))
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
	p := Relay{Phase: "P3", Kind: RelayStopped, Since: t2, Count: 2, Persist: "only they can say"}
	if !p.Persistent() || !st.AgentWaits(p) || st.AgentWaits(st.Relay[1]) {
		t.Errorf("persistent %v, waits %v, the second's waits %v", p.Persistent(), st.AgentWaits(p), st.AgentWaits(st.Relay[1]))
	}
	st.Relay = append(st.Relay, p)
	if st.StopAsked("P3") {
		t.Error("the question with no deadline counts as one baton answers")
	}
	// Restarted: its question waits while the fresh agent works, and not once it is held again.
	w.Status, w.Restarted, w.TakenDown = WorkerSuspended, t2, time.Time{}
	q := Relay{Phase: "P3", Kind: RelayRestarted, Since: t2}
	if !st.AgentWaits(q) || q.Persistent() || st.AgentWaits(p) {
		t.Errorf("restarted waits %v; the persistent one waits %v", st.AgentWaits(q), st.AgentWaits(p))
	}
	w.Status = WorkerHeld
	if st.AgentWaits(q) {
		t.Error("a restart's question waits once its agent is held again")
	}
}

// stoppedTwice is a running run of p0 (the lead's, done), p1 (its agent stopped twice and taken down at
// down, its question with no deadline queued then), p2 (after p1) and p3, done: nothing else can move.
func stoppedTwice(down time.Time) (State, plan.Plan) {
	p := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0"}, {ID: "P1", After: []string{}}, {ID: "P2", After: []string{"P1"}}, {ID: "P3", After: []string{}}}}
	st := Attach(p, down.Add(-time.Hour), Origin{})
	st.Phases["P0"].Status, st.Phases["P3"].Status, st.Current = PhaseDone, PhaseDone, ""
	st.Phases["P1"] = &PhaseState{Status: PhaseWorking, Worker: "run/P1"}
	st.Workers = []Worker{{Phase: "P1", Ref: "run/P1", Status: WorkerHeld, Assigned: down.Add(-time.Hour), Generation: st.Generation, Stops: 2, TakenDown: down}}
	st.Relay = []Relay{{Phase: "P1", Kind: RelayStopped, Since: down, Count: 2, Persist: "only they can say", Queued: down}}
	return st, p
}

// The question about a twice-stopped phase is open until the human answers it, and only while the phase
// still waits on it; what else in the run can move is named, and once nothing can, the phase is taken back
// for the lead escalation_timeout later, never sooner than its dialog's time, nor than the lead's last turn
// or the human's last word allow.
func TestATwiceStoppedPhaseWaitsOnTheRunAlone(t *testing.T) {
	down := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	now := down.Add(time.Minute)
	for _, c := range []struct {
		name  string
		set   func(st *State)
		open  bool
		moves string
	}{
		{"nothing else can move", nil, true, ""},
		{"stepped aside", func(st *State) {
			st.Relay[0].Answered, st.Relay[0].Applied = &Resolution{By: ByTimeout, Answer: "Ask me later", At: now}, now
		}, true, ""},
		{"answered by the human", func(st *State) { st.Relay[0].Answered = &Resolution{By: ByHuman, Answer: "Restart it", At: now} }, false, ""},
		{"withdrawn", func(st *State) { st.Relay[0].Answered = &Resolution{By: ByWithdrawn, At: now} }, false, ""},
		{"its worker restarted", func(st *State) { st.Workers[0].Status, st.Workers[0].TakenDown = WorkerSuspended, time.Time{} }, false, ""},
		{"paused", func(st *State) { st.Mode = ModePaused }, true, "the run is paused"},
		{"the lead holds a phase", func(st *State) { st.Current = "P3" }, true, "the lead holds P3"},
		{"the lead's turn runs", func(st *State) { st.Run.TurnOpen = true }, true, "the lead's turn runs"},
		{"a compaction", func(st *State) { st.Run.Compaction.Queue("boundary", now) }, true, "a compaction is under way"},
		{"a replan", func(st *State) { st.Replan = &Replan{} }, true, "a replan is open"},
		{"the lead's own question", func(st *State) { st.Run.Escalation = &Escalation{Kind: "blocked"} }, true, "a question of the lead's waits"},
		{"a usage limit", func(st *State) { st.Usage = &Usage{Limited: now.Add(time.Hour)} }, true, "a usage limit lifts at 2026-10-15T13:01:00Z"},
		{"a usage limit lifted", func(st *State) { st.Usage = &Usage{Limited: now.Add(-time.Minute)} }, true, ""},
		{"a job", func(st *State) { st.Jobs = []Job{{ID: "j1", Phase: "P0", Status: JobRunning}} }, true, "job j1 runs"},
		{"a phase can start", func(st *State) { st.Phases["P3"].Status = PhasePending }, true, "P3 can start"},
		{"another agent at work", func(st *State) {
			st.Phases["P3"] = &PhaseState{Status: PhaseWorking, Worker: "run/P3"}
			st.Workers = append(st.Workers, Worker{Phase: "P3", Ref: "run/P3", Status: WorkerSuspended, Generation: st.Generation})
		}, true, "the agent on P3 is at work"},
		{"another agent stopped, asked with a deadline", func(st *State) {
			st.Phases["P3"] = &PhaseState{Status: PhaseWorking, Worker: "run/P3"}
			st.Workers = append(st.Workers, Worker{Phase: "P3", Ref: "run/P3", Status: WorkerHeld, Generation: st.Generation, Stops: 1, StoppedAt: now})
			st.Relay = append(st.Relay, Relay{Phase: "P3", Kind: RelayStopped, Since: now, Count: 1, Queued: now})
		}, true, "the question about the agent on P3 has a deadline"},
		{"another agent left stopped by the human", func(st *State) {
			st.Phases["P3"] = &PhaseState{Status: PhaseWorking, Worker: "run/P3"}
			st.Workers = append(st.Workers, Worker{Phase: "P3", Ref: "run/P3", Status: WorkerHeld, Generation: st.Generation, Stops: 1, StoppedAt: now})
		}, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, p := stoppedTwice(down)
			if c.set != nil {
				c.set(&st)
			}
			r := st.StoppedTwice()
			if (r != nil) != c.open {
				t.Fatalf("open %v, want %v", r != nil, c.open)
			}
			if got := st.Moves(p, "P1", now); got != c.moves {
				t.Errorf("moves %q, want %q", got, c.moves)
			}
		})
	}

	st, _ := stoppedTwice(down)
	r := st.Relay[0]
	const wait = 10 * time.Minute
	for _, c := range []struct {
		name  string
		set   func(st *State, r *Relay)
		since time.Time
		want  time.Time
	}{
		{"from when nothing could move", nil, now, now.Add(wait)},
		{"named as HH:MM, never sooner", nil, now.Add(30 * time.Second), now.Add(wait + time.Minute)},
		{"from the lead's last stop", func(st *State, _ *Relay) { st.Run.LastStop = now.Add(2 * time.Minute) }, now, now.Add(wait + 2*time.Minute)},
		{"from the human's last word", func(st *State, _ *Relay) { st.Run.HumanAt = now.Add(5 * time.Minute) }, now, now.Add(wait + 5*time.Minute)},
		{"from the question asked again", func(_ *State, r *Relay) { r.Queued = now.Add(3 * time.Minute) }, now, now.Add(wait + 3*time.Minute)},
		{"not before its dialog's time", func(_ *State, r *Relay) { r.Deadline = now.Add(wait + 4*time.Minute) }, now, now.Add(wait + 4*time.Minute)},
	} {
		s, q := st, r // copies: Run and the relay are values
		if c.set != nil {
			c.set(&s, &q)
		}
		if got := s.TakeUpAt(q, c.since, wait); !got.Equal(c.want) {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
