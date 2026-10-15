package state

import (
	"testing"
	"time"
)

// How long the phase in hand waits on its job before a switch: switch_after, unless the same command is
// known to take at least that long.
func TestSwitchAfter(t *testing.T) {
	const after = 5 * time.Minute
	ran := func(id, cmd, status string, took time.Duration) Job {
		return Job{ID: id, Command: cmd, Status: status, Started: t0, Ended: t0.Add(took)}
	}
	now := Job{ID: "j9", Command: "make test", Status: JobRunning}
	for _, c := range []struct {
		name string
		past []Job
		want time.Duration
	}{
		{"no history", nil, after},
		{"a quick command waits for switch_after", []Job{ran("j1", "make test", JobPassed, time.Minute)}, after},
		{"a long command switches at once", []Job{ran("j1", "make test", JobPassed, 20*time.Minute)}, 0},
		{"a failed run counts", []Job{ran("j1", "make test", JobFailed, 8*time.Minute)}, 0},
		{"so does one that ran out of time", []Job{ran("j1", "make test", JobTimeout, 3*time.Hour)}, 0},
		{"the median, not the longest", []Job{ran("j1", "make test", JobPassed, time.Minute), ran("j2", "make test", JobPassed, 2*time.Minute),
			ran("j3", "make test", JobPassed, time.Hour)}, after},
		{"an even count takes the middle two", []Job{ran("j1", "make test", JobPassed, 2*time.Minute), ran("j2", "make test", JobPassed, 8*time.Minute)}, 0},
		{"other commands do not count", []Job{ran("j1", "make e2e", JobPassed, time.Hour)}, after},
		{"runs with no verdict do not count", []Job{ran("j1", "make test", JobInterrupted, time.Hour), ran("j2", "make test", JobLaneError, time.Hour)}, after},
		{"nor does a job that never started", []Job{{ID: "j1", Command: "make test", Status: JobFailed, Ended: t0}}, after},
	} {
		st := State{Jobs: append(c.past, now)}
		if got := SwitchAfter(st, now, after); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// waitingOnJob is a run of P0, P1 and P2 (each needing nothing) with P0 in hand and waiting on job j1, whose
// verify notes say where it stands, and the session gone on after baton's last compaction.
func waitingOnJob() State {
	st := Attach(graph("P0:", "P1:", "P2:"), t0, Origin{})
	st.Jobs = []Job{{ID: "j1", Phase: "P0", Command: "make test", Status: JobRunning, Notes: "a.go done; make test running", Queued: t0}}
	st.Run.Compaction = Compaction{Epoch: 2, Status: CompactDone, ByBaton: true, Finished: t0}
	st.Run.TurnStarted = t0.Add(time.Second)
	return st
}

func TestSwitchable(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*State)
		want bool
	}{
		{"waiting on its job", func(*State) {}, true},
		{"on a queued job too", func(st *State) { st.Jobs[0].Status = JobQueued }, true},
		{"no job", func(st *State) { st.Jobs = nil }, false},
		{"its job has ended", func(st *State) { st.Jobs[0].Status = JobPassed }, false},
		{"another phase's job", func(st *State) { st.Jobs[0].Phase = "P1" }, false},
		{"paused", func(st *State) { st.Mode = ModePaused }, false},
		{"a boundary owed", func(st *State) { st.BoundaryOwed = true }, false},
		{"a checkpoint owed", func(st *State) { st.CheckpointOwed = true }, false},
		{"a checkpoint asked for", func(st *State) { st.CheckpointAsked = true }, false},
		{"a checkpoint due", func(st *State) { st.CheckpointDue = true }, false},
		{"a result due", func(st *State) { st.Jobs = append([]Job{{ID: "j0", Phase: "P0", Status: JobFailed}}, st.Jobs...) }, false},
		{"compacting", func(st *State) { st.Run.Compaction.Queue("checkpoint", t0) }, false},
		{"a compaction failed", func(st *State) { st.Run.Compaction.Status = CompactFailed }, false},
		{"not resumed after baton's compaction", func(st *State) { st.Run.TurnStarted = time.Time{} }, false},
		{"after Claude Code's own compaction", func(st *State) { st.Run.TurnStarted, st.Run.Compaction.ByBaton = time.Time{}, false }, true},
		{"blocked", func(st *State) { SetBlocked(st, "x", t0) }, false},
		{"escalated", func(st *State) { st.Run.Escalation = &Escalation{Kind: "stalled"} }, false},
		{"a review due", func(st *State) { st.ReviewDue = "P0" }, false},
		{"a replan waits", func(st *State) { st.Replan = &Replan{File: "x"} }, false},
		{"a proposal pending", func(st *State) { AddProposal(st, Decision{What: "x"}, t0) }, false},
		{"background work of the session's own", func(st *State) { st.Run.Background = []Task{{ID: "b1", Type: "shell"}} }, false},
		{"its gate runs in the working tree", func(st *State) { st.Jobs[0].Here = true }, false},
		{"a Monitor is not work", func(st *State) {
			st.Run.Background, st.Run.Monitors = []Task{{ID: "m1", Type: "shell"}}, []string{"m1"}
		}, true},
	} {
		st := waitingOnJob()
		c.set(&st)
		if got := Switchable(st); got != c.want {
			t.Errorf("%s: switchable %v", c.name, got)
		}
	}
}

// A switch parks the phase in hand with its job's notes and takes up the next phase after a compaction; it
// changes nothing when no other phase can go on.
func TestSwitch(t *testing.T) {
	p := graph("P0:", "P1:", "P2:")
	t1 := t0.Add(6 * time.Minute)
	st := waitingOnJob()
	prev := &Park{Reason: ParkRed, Since: t0, Notes: "earlier", Resumed: t0}
	st.Phases["P0"].Park = prev
	st.Run.StopBlocks = 2
	sw, ok := Switch(&st, p, t1)
	if !ok || sw != (Switched{From: "P0", To: "P1", Job: "j1", Prev: prev}) {
		t.Fatalf("switch: %+v %v", sw, ok)
	}
	ps := st.Phases["P0"]
	if ps.Status != PhaseParked || *ps.Park != (Park{Reason: ParkJob, Since: t1, Notes: "a.go done; make test running", Job: "j1"}) {
		t.Errorf("P0 %+v park %+v", ps, ps.Park)
	}
	comp := st.Run.Compaction
	if st.Current != "P1" || !st.BoundaryOwed || comp.Status != CompactQueued || comp.Reason != "switch" || comp.Epoch != 3 || comp.Switch == nil || *comp.Switch != sw || st.Run.StopBlocks != 0 {
		t.Errorf("after the switch: in hand %q owed %v compaction %+v", st.Current, st.BoundaryOwed, comp)
	}
	if !comp.StartsPhase() || Next(st, p) == "P0" || Returning(st, p) != "" {
		t.Errorf("the switch's compaction starts no phase, or P0 can resume while its job runs: next %q", Next(st, p))
	}

	// Nothing else can go on: the session stays where it is.
	chain := graph("P0:", "P1:P0")
	st = waitingOnJob()
	before := st
	if _, ok := Switch(&st, chain, t1); ok || st.Current != "P0" || st.Phases["P0"].Status != PhaseActive || st.BoundaryOwed || st.Run.Compaction != before.Run.Compaction {
		t.Fatalf("switched with nothing to go on to: %+v", st)
	}
}

// A switch whose job ends before its compaction is typed is called off: the phase is in hand again, as it
// was, and its result is due there.
func TestUnswitch(t *testing.T) {
	p := graph("P0:", "P1:", "P2:")
	t1 := t0.Add(6 * time.Minute)
	switched := func() State {
		st := waitingOnJob()
		st.Phases["P0"].Park = &Park{Reason: ParkRed, Since: t0, Notes: "earlier", Resumed: t0}
		if _, ok := Switch(&st, p, t1); !ok {
			t.Fatal("no switch")
		}
		return st
	}
	for _, c := range []struct {
		name string
		set  func(*State)
		off  bool
	}{
		{"the job ended", func(st *State) { st.Jobs[0].Status = JobPassed }, true},
		{"the job is gone from the record", func(st *State) { st.Jobs = nil }, true},
		{"the job still runs", func(*State) {}, false},
		{"typed already", func(st *State) {
			st.Jobs[0].Status = JobPassed
			st.Run.Compaction.Status, st.Run.Compaction.Tries = CompactTyped, 1
		}, false},
		{"queued again after a try", func(st *State) { st.Jobs[0].Status = JobPassed; st.Run.Compaction.Tries = 1 }, false},
		{"the run moved on", func(st *State) { st.Jobs[0].Status = JobPassed; st.Current = "P2" }, false},
	} {
		st := switched()
		c.set(&st)
		sw, off := Unswitch(&st)
		if off != c.off {
			t.Errorf("%s: called off %v", c.name, off)
			continue
		}
		if !off {
			continue
		}
		ps := st.Phases["P0"]
		if sw.From != "P0" || st.Current != "P0" || st.BoundaryOwed || ps.Status != PhaseActive || ps.Park == nil || ps.Park.Notes != "earlier" {
			t.Errorf("%s: after: in hand %q owed %v P0 %+v park %+v", c.name, st.Current, st.BoundaryOwed, ps, ps.Park)
		}
		if comp := st.Run.Compaction; comp.InFlight() || comp.Status != CompactCancelled || comp.Switch != nil {
			t.Errorf("%s: compaction %+v", c.name, comp)
		}
		if c.name == "the job ended" && len(st.Due()) != 1 {
			t.Errorf("%s: the result is not due in place: %+v", c.name, st.Due())
		}
	}
}

// A checkpoint goes back to a parked phase whose job has ended, setting the phase in hand aside with the
// checkpoint's notes.
func TestSwitchAtCheckpoint(t *testing.T) {
	p := graph("P0:", "P1:", "P2:", "P3:")
	t1 := t0.Add(time.Hour)
	// P1 is in hand; P0 and P2 are parked.
	at := func(p0Job, p2Job string, p2Reason string, p2Since time.Time) State {
		st := Attach(p, t0, Origin{})
		st.Phases["P0"] = &PhaseState{Status: PhaseParked, Park: &Park{Reason: ParkJob, Since: t0.Add(time.Minute), Notes: "P0's notes", Job: "j1"}}
		st.Phases["P2"] = &PhaseState{Status: PhaseParked, Park: &Park{Reason: p2Reason, Since: p2Since, Job: p2Job}}
		st.Phases["P1"] = &PhaseState{Status: PhaseActive}
		st.Current = "P1"
		st.Jobs = []Job{{ID: "j1", Phase: "P0", Status: p0Job}, {ID: "j2", Phase: "P2", Status: JobRunning}}
		return st
	}
	for _, c := range []struct {
		name  string
		st    State
		notes string
		want  string
	}{
		{"no phase has news", at(JobRunning, "j2", ParkJob, t0), "x", ""},
		{"a job ended", at(JobFailed, "j2", ParkJob, t0), "x", "P0"},
		{"no notes, no switch", at(JobFailed, "j2", ParkJob, t0), " ", ""},
		{"a phase set aside at a checkpoint has no news", at(JobRunning, "", ParkCheckpoint, t0), "x", ""},
		{"the longest parked with news first", at(JobPassed, "", ParkRed, t0), "x", "P2"},
		{"not while a gate runs in the working tree", at(JobFailed, "j2", ParkJob, t0), "here", ""},
	} {
		st := c.st
		st.Jobs = append(st.Jobs, Job{ID: "j3", Phase: "P1", Status: JobRunning, Here: c.notes == "here"})
		SetCheckpoint(&st, t0)
		to := SwitchAtCheckpoint(&st, p, c.notes, t1)
		if to != c.want {
			t.Errorf("%s: switched to %q", c.name, to)
			continue
		}
		if to == "" {
			if st.Current != "P1" || !st.CheckpointOwed || st.Run.Compaction.InFlight() {
				t.Errorf("%s: no longer a plain checkpoint: %+v", c.name, st)
			}
			continue
		}
		ps := st.Phases["P1"]
		if ps.Status != PhaseParked || *ps.Park != (Park{Reason: ParkCheckpoint, Since: t1, Notes: "x", Job: "j3"}) {
			t.Errorf("%s: P1 %+v park %+v", c.name, ps, ps.Park)
		}
		if comp := st.Run.Compaction; st.Current != to || !st.BoundaryOwed || st.CheckpointOwed || comp.Status != CompactQueued || comp.Reason != "switch" || comp.Switch != nil {
			t.Errorf("%s: in hand %q owed %v checkpoint %v compaction %+v", c.name, st.Current, st.BoundaryOwed, st.CheckpointOwed, comp)
		}
	}
}

// Round trip: P0 is set aside while its gate runs, P1 is done meanwhile, and P0 comes back with its notes
// once the gate has passed, ahead of P2.
func TestSwitchAndComeBack(t *testing.T) {
	p := graph("P0:", "P1:", "P2:")
	st := waitingOnJob()
	t1 := t0.Add(6 * time.Minute)
	if _, ok := Switch(&st, p, t1); !ok {
		t.Fatal("no switch")
	}
	Start(&st, t1, Origin{})
	st.Jobs[0].Status = JobPassed
	if next, err := Done(&st, p, "P1", t1, DoneOpts{}); err != nil || next != "P0" {
		t.Fatalf("done P1: next %q err %v", next, err)
	}
	Start(&st, t1, Origin{})
	if ps := st.Phases["P0"]; ps.Status != PhaseActive || !st.Resumed() || ps.Park.Notes != "a.go done; make test running" {
		t.Fatalf("P0 resumed: %+v park %+v", ps, ps.Park)
	}
}
