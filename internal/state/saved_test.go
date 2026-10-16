package state

import (
	"testing"
	"time"
)

// What phase agents saved a plan is read from its record: each phase's work, the lead's from its start to
// its done, an agent's from its agent's start to its finish; against the wall clock from the first phase's
// start to the last one's done.
func TestSavingIsReadFromTheRecord(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return t0.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	lead := func(from, to time.Time) *PhaseState {
		return &PhaseState{Status: PhaseDone, StartedAt: from, DoneAt: to}
	}
	// agent is a phase given to a worker at assigned, landed at landed, and the roster's entry of it.
	agent := func(phase string, assigned, started, finished, landed time.Time) (*PhaseState, Worker) {
		ref := WorkerRef("run", phase)
		return &PhaseState{Status: PhaseDone, StartedAt: assigned, DoneAt: landed, Worker: ref},
			Worker{Phase: phase, Ref: ref, Status: WorkerLanded, Assigned: assigned, Started: started, Finished: finished, Since: landed}
	}
	cases := []struct {
		name  string
		setup func(st *State)
		want  Saving
	}{
		{name: "a plan with agents",
			setup: func(st *State) {
				st.Phases["P0"] = lead(at(0, 0), at(1, 0))
				p1, w1 := agent("P1", at(0, 0), at(0, 5), at(2, 0), at(2, 30)) // its landing waited half an hour
				p2, w2 := agent("P2", at(0, 10), at(0, 12), at(1, 12), at(1, 20))
				st.Phases["P1"], st.Phases["P2"], st.Workers = p1, p2, []Worker{w1, w2}
				st.Phases["P3"] = lead(at(2, 30), at(3, 0))
			},
			want: Saving{Work: time.Hour + 115*time.Minute + time.Hour + 30*time.Minute, Wall: 3 * time.Hour, Agents: 2}},
		{name: "a plan without agents",
			setup: func(st *State) {
				st.Phases["P0"] = lead(at(0, 0), at(1, 0))
				st.Phases["P1"] = lead(at(1, 5), at(2, 0))
			},
			want: Saving{Work: 115 * time.Minute, Wall: 2 * time.Hour}},
		{name: "overlapping stretches each count in full",
			setup: func(st *State) {
				st.Phases["P0"] = lead(at(0, 0), at(2, 0))
				p1, w1 := agent("P1", at(0, 0), at(0, 0), at(2, 0), at(2, 0))
				p2, w2 := agent("P2", at(0, 0), at(0, 0), at(2, 0), at(2, 0))
				st.Phases["P1"], st.Phases["P2"], st.Workers = p1, p2, []Worker{w1, w2}
			},
			want: Saving{Work: 6 * time.Hour, Wall: 2 * time.Hour, Agents: 2}},
		{name: "a lead phase whose gate passed after the next began overlaps it",
			setup: func(st *State) {
				st.Phases["P0"] = lead(at(0, 0), at(1, 30))
				st.Phases["P1"] = lead(at(1, 0), at(2, 0))
			},
			want: Saving{Work: 150 * time.Minute, Wall: 2 * time.Hour}},
		{name: "an idle stretch counts on the wall clock alone",
			setup: func(st *State) {
				st.Phases["P0"] = lead(at(0, 0), at(1, 0))
				p1, w1 := agent("P1", at(0, 0), at(0, 0), at(1, 0), at(1, 5))
				st.Phases["P1"], st.Workers = p1, []Worker{w1}
				st.Phases["P2"] = lead(at(9, 0), at(10, 0)) // the night between, nothing at work
			},
			want: Saving{Work: 3 * time.Hour, Wall: 10 * time.Hour, Agents: 1}},
		{name: "an agent with no finish on record counts to its landing",
			setup: func(st *State) {
				p1, w1 := agent("P1", at(0, 0), at(0, 5), time.Time{}, at(1, 0))
				st.Phases["P1"], st.Workers = p1, []Worker{w1}
			},
			want: Saving{Work: 55 * time.Minute, Wall: time.Hour, Agents: 1}},
		{name: "an agent with no start on record counts from its assignment",
			setup: func(st *State) {
				p1, w1 := agent("P1", at(0, 0), time.Time{}, at(0, 40), at(1, 0))
				st.Phases["P1"], st.Workers = p1, []Worker{w1}
			},
			want: Saving{Work: 40 * time.Minute, Wall: time.Hour, Agents: 1}},
		{name: "only the worker whose work landed counts",
			setup: func(st *State) {
				p1, w1 := agent("P1", at(1, 0), at(1, 5), at(2, 0), at(2, 10))
				failed := Worker{Phase: "P1", Ref: w1.Ref, Status: WorkerFailed, Assigned: at(0, 0), Started: at(0, 0), Since: at(0, 50)}
				st.Phases["P1"], st.Workers = p1, []Worker{failed, w1}
				st.Phases["P0"] = lead(at(0, 0), at(2, 0))
			},
			want: Saving{Work: 55*time.Minute + 2*time.Hour, Wall: 130 * time.Minute, Agents: 1}},
		{name: "a phase taken back from its agent is the lead's",
			setup: func(st *State) {
				st.Phases["P1"] = lead(at(1, 0), at(2, 0)) // Unassign left it pending, with no worker
				st.Workers = []Worker{{Phase: "P1", Ref: WorkerRef("run", "P1"), Status: WorkerFailed, Assigned: at(0, 0), Started: at(0, 0)}}
			},
			want: Saving{Work: time.Hour, Wall: time.Hour}},
		{name: "a worker of an earlier plan is not this plan's",
			setup: func(st *State) {
				st.Generation = 2
				p1, w1 := agent("P1", at(0, 0), at(0, 30), at(0, 40), at(1, 0))
				w1.Generation = 1
				st.Phases["P1"], st.Workers = p1, []Worker{w1}
			},
			want: Saving{Work: time.Hour, Wall: time.Hour, Agents: 1}},
		{name: "phases not done count nothing",
			setup: func(st *State) {
				st.Phases["P0"] = lead(at(0, 0), at(1, 0))
				st.Phases["P1"] = &PhaseState{Status: PhaseActive, StartedAt: at(0, 0)}
				st.Phases["P2"] = &PhaseState{Status: PhaseWorking, StartedAt: at(0, 0), Worker: WorkerRef("run", "P2")}
				st.Phases["P3"] = &PhaseState{Status: PhasePending}
			},
			want: Saving{Work: time.Hour, Wall: time.Hour}},
		{name: "a phase done with no start on record counts no work, and ends the wall clock",
			setup: func(st *State) {
				st.Phases["P0"] = lead(at(0, 0), at(1, 0))
				st.Phases["P1"] = &PhaseState{Status: PhaseDone, DoneAt: at(2, 0)} // done --force, never started
			},
			want: Saving{Work: time.Hour, Wall: 2 * time.Hour}},
		{name: "nothing done",
			want: Saving{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := New()
			st.Mode = ModeComplete
			if c.setup != nil {
				c.setup(&st)
			}
			if got := SavingOf(st); got != c.want {
				t.Errorf("SavingOf = %+v, want %+v", got, c.want)
			}
		})
	}
}

// What was saved is the work beyond the wall clock, never below zero; the event logs it in seconds.
func TestSavedIsNeverBelowZero(t *testing.T) {
	for _, c := range []struct {
		s    Saving
		want time.Duration
	}{
		{Saving{Work: 3 * time.Hour, Wall: 2 * time.Hour, Agents: 1}, time.Hour},
		{Saving{Work: time.Hour, Wall: 10 * time.Hour, Agents: 1}, 0},
		{Saving{}, 0},
	} {
		if got := c.s.Saved(); got != c.want {
			t.Errorf("%+v: saved %v, want %v", c.s, got, c.want)
		}
	}
	got := Saving{Work: 8*time.Hour + 29*time.Minute, Wall: 5*time.Hour + 9*time.Minute + 30*time.Second, Agents: 3}.Log(map[string]any{"by": "landed"})
	want := map[string]any{"by": "landed", "agent_phases": 3, "work_seconds": 30540, "wall_seconds": 18570, "saved_seconds": 11970}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (%v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("fields %v, want %v", got, want)
	}
	if got := (Saving{}).Log(nil); got["agent_phases"] != 0 {
		t.Errorf("fields of none: %v", got)
	}
}
