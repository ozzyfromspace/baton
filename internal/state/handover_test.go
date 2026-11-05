package state

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// handPlan is three phases beside each other on opus/high, the last one after every phase before it.
func handPlan() plan.Plan {
	return plan.Plan{Version: 1, Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}, Model: "opus", Effort: "high"},
		{ID: "P1", Title: "one", After: []string{}, Model: "opus", Effort: "high"},
		{ID: "P2", Title: "two", After: []string{}, Model: "opus", Effort: "high"},
		{ID: "P3", Title: "three", After: []string{"P0", "P1", "P2"}, Model: "opus", Effort: "high"},
	}}
}

// raisedTo is a raise of a phase on opus/high, its plan's tier, to effort.
func raisedTo(effort string) TierRaise {
	return TierRaise{From: plan.Tier{Model: "opus", Effort: "high"}, To: plan.Tier{Model: "opus", Effort: effort},
		Plan: plan.Tier{Model: "opus", Effort: "high"}, Why: "3 red runs of its gate", Reds: 3, At: t0}
}

// handRun is a run of handPlan with phase agents, whose lead runs on opus/high and holds P0: P1 was started
// by the lead, raised to opus/xhigh, and set aside red on its gate j1, which ended.
func handRun() State {
	st := runState(handPlan(), map[string]string{"P0": PhaseActive, "P1": PhaseParked}, map[string]Park{
		"P1": {Reason: ParkRed, Since: t0.Add(time.Minute), Notes: "the parser is half done", Job: "j1"},
	}, Job{ID: "j1", Phase: "P1", Kind: JobVerify, Command: "go test ./...", Status: JobFailed, Exit: 1, Queued: t0, Ended: t0.Add(time.Minute)})
	withAgents(&st)
	st.Current = "P0"
	st.Run.Launched = plan.Tier{Model: "opus", Effort: "high"}
	ps := st.Phases["P1"]
	ps.StartedAt, ps.StartHead, ps.Edited, ps.Raises = t0.Add(-time.Hour), "abc123", []string{"parse.go"}, []TierRaise{raisedTo("xhigh")}
	return st
}

// A parked phase whose tier the lead no longer runs on (raised, here) goes to a phase agent: the lead
// passes over it, an agent takes it first, and it keeps its history: its raises, its start, what the lead
// edited, its park as Handed. The lead's red gate stands for it no more. If its agent fails on it, it is
// parked again as it was, for the lead, its raise kept.
func TestAParkedPhaseOffTheLeadsTierGoesToAnAgent(t *testing.T) {
	p := handPlan()
	st := handRun()
	if got := NextWorker(st, p); got != "P1" {
		t.Fatalf("NextWorker = %q, want the parked P1 first", got)
	}
	idle := st
	idle.Phases = map[string]*PhaseState{}
	for id, ps := range st.Phases {
		cp := *ps
		idle.Phases[id] = &cp
	}
	idle.Current, idle.Phases["P0"].Status = "", PhaseDone
	if got := Next(idle, p); got != "P2" {
		t.Errorf("Next = %q, want P2: the lead passes over P1", got)
	}
	if got := Returning(idle, p); got != "" {
		t.Errorf("Returning = %q: a checkpoint does not go back to a phase an agent takes", got)
	}

	at := t0.Add(2 * time.Hour)
	if err := Assign(&st, p, worker("P1"), at); err != nil {
		t.Fatal(err)
	}
	ps := st.Phases["P1"]
	switch {
	case ps.Status != PhaseWorking || ps.Worker != "r1/P1" || ps.Park != nil || ps.On != nil:
		t.Errorf("not working: %+v", ps)
	case !ps.StartedAt.Equal(t0.Add(-time.Hour)) || ps.StartHead != "abc123" || len(ps.Edited) != 1 || len(ps.Raises) != 1:
		t.Errorf("its history is gone: %+v", ps)
	case ps.Handed == nil || !ps.Handed.At.Equal(at) || ps.Handed.Park == nil || ps.Handed.Park.Job != "j1":
		t.Errorf("its park is not kept: %+v", ps.Handed)
	}
	if tier := TierFor(st, p, "P1", nil); tier.String() != "opus/xhigh" {
		t.Errorf("its agent's tier: %s", tier)
	}
	if j := st.Job("j1"); j.Delivered.IsZero() {
		t.Error("the lead's red gate is still owed to the lead")
	}
	if got := st.Standing("P1"); len(got) != 0 || len(st.Red()) != 0 || st.RedGate("P1") != nil {
		t.Errorf("the lead's red gate stands for a phase an agent has: %+v", got)
	}

	// Its agent failed on it: it is the lead's again, parked as it was handed, raised still.
	st.Current = ""
	st.Phases["P0"].Status = PhaseDone
	next, err := Unassign(&st, p, "P1", "its agent did not start", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ps = st.Phases["P1"]
	switch {
	case next != "P1" || st.Current != "P1" || !st.BoundaryOwed:
		t.Errorf("the lead takes up %q (in hand %q, boundary owed %v), want P1", next, st.Current, st.BoundaryOwed)
	case ps.Status != PhaseParked || ps.Park == nil || ps.Park.Reason != ParkRed || ps.Park.Job != "j1" || ps.Worker != "" || ps.Handed != nil:
		t.Errorf("not parked as it was: %+v", ps)
	case len(ps.Raises) != 1 || TierFor(st, p, "P1", nil).String() != "opus/xhigh":
		t.Errorf("its raise is gone: %+v", ps.Raises)
	}
	if got := st.Standing("P1"); len(got) != 1 || got[0].ID != "j1" {
		t.Errorf("the lead's red gate does not stand again: %+v", got)
	}
}

// A parked phase stays the lead's where its park or the run says so: the lead runs on its tier, the run has
// no agents, an agent of the plan failed on it, it waits for every phase before it, a gate of its runs in
// the lead's checkout, or its done left files of its own uncommitted. One whose job still runs is passed
// over by the lead, and waits for an agent until the job ends.
func TestAParkedPhaseStaysTheLeads(t *testing.T) {
	p := handPlan()
	for _, c := range []struct {
		name       string
		edit       func(*State)
		next, want string // the lead's next phase with none in hand (Next), and an agent's (NextWorker)
	}{
		{"its tier is the lead's", func(st *State) { st.Phases["P1"].Raises = nil }, "P1", "P2"},
		{"no agents", func(st *State) { st.Run.Fleet = "" }, "P1", "P2"},
		{"an agent failed on it", func(st *State) {
			st.Workers = append(st.Workers, Worker{Phase: "P1", Ref: "r1/P1", Status: WorkerFailed, Generation: st.Generation})
		}, "P1", "P2"},
		{"a gate of its runs in the lead's checkout", func(st *State) {
			st.Jobs = append(st.Jobs, Job{ID: "j2", Phase: "P1", Kind: JobVerify, Command: "make check", Here: true, Status: JobRunning, Queued: t0})
		}, "", "P2"},
		{"its done kept files uncommitted", func(st *State) { st.Phases["P1"].KeptDirty = 2 }, "P1", "P2"},
		{"its job still runs", func(st *State) {
			st.Jobs = append(st.Jobs, Job{ID: "j2", Phase: "P1", Kind: JobAwait, Command: "gh run watch", Status: JobRunning, Queued: t0})
			st.Phases["P1"].On = []string{"j2"}
		}, "P2", ""},
		{"it waits for every phase before it", func(st *State) {
			for _, id := range []string{"P1", "P2"} {
				st.Phases[id].Status = PhaseDone
			}
			st.Phases["P3"] = &PhaseState{Status: PhaseParked, Park: &Park{Reason: ParkCheckpoint, Notes: "half"}, Raises: []TierRaise{raisedTo("max")}}
		}, "P3", ""},
	} {
		st := handRun()
		st.Current, st.Phases["P0"].Status = "", PhaseDone
		c.edit(&st)
		if got := Next(st, p); got != c.next {
			t.Errorf("%s: Next = %q, want %q", c.name, got, c.next)
		}
		if got := NextWorker(st, p); got != c.want {
			t.Errorf("%s: NextWorker = %q, want %q", c.name, got, c.want)
		}
		parked := "P1"
		if st.Phases["P3"].Status == PhaseParked {
			parked = "P3"
		}
		if err := Assign(&st, p, worker(parked), t0.Add(time.Hour)); err == nil {
			t.Errorf("%s: %s was assigned to an agent", c.name, parked)
		}
	}
}

// The raises baton made a pending phase's tier stay with it as an agent takes it, and as its agent fails.
func TestAPendingPhaseKeepsItsRaises(t *testing.T) {
	p := handPlan()
	st := handRun()
	st.Phases["P2"].Raises = []TierRaise{raisedTo("max")}
	if err := Assign(&st, p, worker("P2"), t0); err != nil {
		t.Fatal(err)
	}
	if ps := st.Phases["P2"]; ps.Status != PhaseWorking || len(ps.Raises) != 1 {
		t.Fatalf("assigned: %+v", ps)
	}
	if _, err := Unassign(&st, p, "P2", "it failed", t0); err != nil {
		t.Fatal(err)
	}
	if ps := st.Phases["P2"]; ps.Status != PhasePending || len(ps.Raises) != 1 || ps.Handed != nil {
		t.Errorf("taken back: %+v", ps)
	}
}

// inHand is handRun with P1 in hand instead, raised mid-phase after its gate j1 went red.
func inHand() State {
	st := handRun()
	st.Current = "P1"
	st.Phases["P0"].Status = PhasePending
	ps := st.Phases["P1"]
	ps.Status, ps.Park = PhaseActive, nil
	st.Jobs[0].Notes = "the parser is half done"
	return st
}

// The phase in hand, raised while agents are at work, is set aside for one: parked with its last gate's
// notes, and the lead takes up its next phase after a boundary compaction, or holds none. It is not while
// anything else of it is under way, or when an agent may not take it.
func TestHandOver(t *testing.T) {
	p := handPlan()
	st := inHand()
	h, ok := HandOver(&st, p, t0.Add(time.Hour))
	ps := st.Phases["P1"]
	switch {
	case !ok || h.Phase != "P1" || h.Tier.String() != "opus/xhigh" || h.Lead.String() != "opus/high" || h.Job != "j1" || h.Next != "P0":
		t.Fatalf("handed %+v (%v)", h, ok)
	case ps.Status != PhaseParked || ps.Park == nil || ps.Park.Reason != ParkHanded || ps.Park.Notes != "the parser is half done" || ps.Park.Job != "j1":
		t.Errorf("not parked for an agent: %+v", ps.Park)
	case st.Current != "P0" || !st.BoundaryOwed || st.Run.Compaction.Status != CompactQueued || st.Run.Compaction.Reason != "boundary":
		t.Errorf("the lead takes up %q (boundary owed %v), compaction %+v", st.Current, st.BoundaryOwed, st.Run.Compaction)
	case !st.AgentTakes(p, "P1") || NextWorker(st, p) != "P1":
		t.Errorf("no agent takes it: %q", NextWorker(st, p))
	}

	st = inHand()
	st.Phases["P0"].Status, st.Phases["P2"].Status = PhaseWorking, PhaseWorking
	if h, ok := HandOver(&st, p, t0.Add(time.Hour)); !ok || h.Next != "" || st.Current != "" || st.BoundaryOwed || st.Run.Compaction.InFlight() {
		t.Errorf("with nothing else for the lead: %+v, in hand %q, compaction %+v", h, st.Current, st.Run.Compaction)
	}

	for _, c := range []struct {
		name, why string
		edit      func(*State)
	}{
		{"no agents", "no phase agents", func(st *State) { st.Run.Fleet = "" }},
		{"its tier is the lead's", "runs on its tier", func(st *State) { st.Phases["P1"].Raises = nil }},
		{"an agent failed on it", "failed on it", func(st *State) {
			st.Workers = append(st.Workers, Worker{Phase: "P1", Ref: "r1/P1", Status: WorkerFailed, Generation: st.Generation})
		}},
		{"its job runs", "its jobs run", func(st *State) {
			st.Jobs = append(st.Jobs, Job{ID: "j2", Phase: "P1", Kind: JobVerify, Command: "go test ./...", Status: JobRunning, Queued: t0})
		}},
		{"a checkpoint is owed", "checkpoint", func(st *State) { st.CheckpointOwed = true }},
		{"its done kept files uncommitted", "uncommitted", func(st *State) { st.Phases["P1"].KeptDirty = 1 }},
		{"the human is asked", "asked", func(st *State) { st.Blocked = &Block{Reason: "a key", Since: t0} }},
		{"a boundary is owed", "no phase is in hand", func(st *State) { st.BoundaryOwed = true }},
	} {
		st := inHand()
		c.edit(&st)
		if why := Handable(st, p); !strings.Contains(why, c.why) {
			t.Errorf("%s: Handable = %q, want %q", c.name, why, c.why)
		}
		if _, ok := HandOver(&st, p, t0.Add(time.Hour)); ok || st.Current != "P1" {
			t.Errorf("%s: handed over", c.name)
		}
	}

	// A phase after every phase before it stays the lead's whatever its tier.
	st = inHand()
	for _, id := range []string{"P0", "P1", "P2"} {
		st.Phases[id].Status = PhaseDone
	}
	st.Current, st.Phases["P3"] = "P3", &PhaseState{Status: PhaseActive, Raises: []TierRaise{raisedTo("max")}}
	if why := Handable(st, p); !strings.Contains(why, "every phase before it") {
		t.Errorf("a phase after all: Handable = %q", why)
	}
}

// A boundary owed into a parked phase an agent takes up again turns to the lead's next phase before its
// compaction is typed, or, with none, the compaction is called off; once typed, or for a phase the lead
// takes, it stands.
func TestRedirect(t *testing.T) {
	p := handPlan()
	owed := func() State {
		st := handRun()
		st.Current, st.Phases["P0"].Status = "P1", PhaseDone
		st.OweBoundary(t0)
		st.Run.Compaction.Queue("boundary", t0)
		return st
	}
	st := owed()
	if from, to, ok := Redirect(&st, p); !ok || from != "P1" || to != "P2" || st.Current != "P2" || !st.BoundaryOwed || st.Run.Compaction.Status != CompactQueued {
		t.Errorf("redirected %q → %q (%v): in hand %q, compaction %+v", from, to, ok, st.Current, st.Run.Compaction)
	}
	st = owed()
	st.Phases["P2"].Status = PhaseWorking
	if _, to, ok := Redirect(&st, p); !ok || to != "" || st.Current != "" || st.BoundaryOwed || st.Run.Compaction.Status != CompactCancelled {
		t.Errorf("with nothing for the lead: %q (%v), in hand %q, compaction %+v", to, ok, st.Current, st.Run.Compaction)
	}
	for name, edit := range map[string]func(*State){
		"typed":           func(st *State) { st.Run.Compaction.Status = CompactTyped },
		"tried":           func(st *State) { st.Run.Compaction.Tries = 1 },
		"the lead's tier": func(st *State) { st.Phases["P1"].Raises = nil },
	} {
		st := owed()
		edit(&st)
		if _, _, ok := Redirect(&st, p); ok || st.Current != "P1" {
			t.Errorf("%s: redirected to %q", name, st.Current)
		}
	}
}
