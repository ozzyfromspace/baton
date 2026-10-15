package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// resultRig is the rig with its plan on disk (P0, then P1, which follows it), P1 in hand and nothing owed.
func resultRig(t *testing.T) *rig {
	r := newRig(t)
	r.loop.Store.WritePlan(plan.Plan{Version: 1, Title: "Demo", File: "/x/demo.md", Phases: []plan.Phase{{ID: "P0", Title: "a"}, {ID: "P1", Title: "b"}}})
	r.set(func(st *state.State) {
		state.Start(st, r.now, state.Origin{})
		st.Run.Compaction = state.Compaction{Epoch: 1}
		st.Run.LastActivity, st.Run.LastStop = r.now, r.now
	})
	return r
}

// verifying puts the run where `baton done` with a job running leaves it: phase verifying on job j1.
func (r *rig) verifying(phase string) {
	r.set(func(st *state.State) {
		st.Phases[phase].Status = state.PhaseVerifying
		st.Jobs = []state.Job{{ID: "j1", Phase: phase, Kind: state.JobVerify, Command: "make test", Commit: "abc", Status: state.JobRunning, Notes: "all in; testing"}}
		if phase == st.Current {
			st.Current = ""
		}
	})
}

func (r *rig) finish(id, status string) {
	r.set(func(st *state.State) {
		j := st.Job(id)
		j.Status, j.Started, j.Ended = status, r.now.Add(-time.Minute), r.now
		if status == state.JobFailed {
			j.Exit = 1
		}
	})
}

// The host applies a verification's verdict to a phase verifying on it, while the session works on
// another phase, and leaves the human a line about it.
func TestTheHostSettlesAVerification(t *testing.T) {
	for _, c := range []struct {
		status, phase, park, says string
	}{
		{state.JobPassed, state.PhaseDone, "", "baton: P0 verified (job j1 passed) → P0 done"},
		{state.JobFailed, state.PhaseParked, state.ParkRed, "baton: P0's verification failed (exit 1) after 1m (job j1) → P0 reopens"},
		{state.JobLaneError, state.PhaseParked, state.ParkUnverified, "with no verdict → P0 reopens"},
	} {
		r := resultRig(t)
		r.verifying("P0")
		r.tick(time.Second)
		if st := r.state(); st.Phases["P0"].Status != state.PhaseVerifying {
			t.Fatalf("%s: settled while the job runs: %+v", c.status, st.Phases["P0"])
		}
		r.finish("j1", c.status)
		r.tick(time.Second)
		st := r.state()
		ps := st.Phases["P0"]
		if ps.Status != c.phase || c.park != "" && (ps.Park == nil || ps.Park.Reason != c.park || ps.Park.Notes != "all in; testing") {
			t.Errorf("%s: P0 %+v park %+v", c.status, ps, ps.Park)
		}
		if st.Current != "P1" || st.BoundaryOwed || st.Run.Compaction.InFlight() {
			t.Errorf("%s: the phase in hand moved: %q owed %v %+v", c.status, st.Current, st.BoundaryOwed, st.Run.Compaction)
		}
		if len(st.Run.Announce) != 1 || !strings.Contains(st.Run.Announce[0], c.says) {
			t.Errorf("%s: announced %q", c.status, st.Run.Announce)
		}
		want := `"kind":"phase_parked","phase":"P0","reason":"` + c.park
		if c.park == "" {
			want = `"by":"verification","instance":"inst","job":"j1","kind":"phase_done","next":"","phase":"P0"`
		}
		if !strings.Contains(r.events(), want) {
			t.Errorf("%s: events lack %s:\n%s", c.status, want, r.events())
		}
		if len(r.in.text) != 0 {
			t.Errorf("%s: typed %q", c.status, r.in.text)
		}
	}
}

// With no phase in hand, a verdict lets the run go on: the host queues the compaction itself, as the
// session is idle and no Stop would, and types it.
func TestAVerdictTakesUpTheNextPhase(t *testing.T) {
	for status, next := range map[string]string{state.JobPassed: "P1", state.JobFailed: "P0"} {
		r := resultRig(t)
		r.set(func(st *state.State) { st.Phases["P1"].Status = state.PhasePending })
		r.verifying("P0")
		r.set(func(st *state.State) { st.Current = "" })
		r.run(DefaultTiming.IdleNudge * 2)
		if len(r.in.text) != 0 {
			t.Fatalf("%s: typed %q while every phase waits for a job", status, r.in.text)
		}
		r.finish("j1", status)
		r.tick(time.Second)
		st := r.state()
		if st.Current != next || !st.BoundaryOwed || st.Run.Compaction.Status != state.CompactTyped || st.Run.Compaction.Reason != "boundary" {
			t.Errorf("%s: in hand %q owed %v compaction %+v", status, st.Current, st.BoundaryOwed, st.Run.Compaction)
		}
		if len(r.in.text) != 1 || r.in.text[0] != "/compact" {
			t.Errorf("%s: typed %q", status, r.in.text)
		}
		if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "→ compacting, then "+next) || !strings.Contains(r.events(), `"by":"settle"`) {
			t.Errorf("%s: announced %q, events:\n%s", status, st.Run.Announce, r.events())
		}
	}
}

// A verdict that takes up a phase while a turn runs (one the human started, say) queues the boundary at
// once, but types nothing into that turn: the compaction waits for its stop, and comes then. An idle lead
// compacts at once (TestAVerdictTakesUpTheNextPhase).
func TestAVerdictWaitsForTheTurnUnderWay(t *testing.T) {
	r := resultRig(t)
	r.set(func(st *state.State) { st.Phases["P1"].Status = state.PhasePending })
	r.verifying("P0")
	r.set(func(st *state.State) {
		st.Current = ""
		st.Run.TurnOpen, st.Run.TurnBy, st.Run.TurnStarted = true, "human", r.now
	})
	r.finish("j1", state.JobPassed)
	for range 30 { // the turn goes on, busy, for five minutes
		r.set(func(st *state.State) { st.Run.LastActivity = r.now })
		r.tick(10 * time.Second)
	}
	st := r.state()
	if st.Current != "P1" || !st.BoundaryOwed || st.Run.Compaction.Status != state.CompactQueued || !st.BoundaryWaits() || len(r.in.text) != 0 {
		t.Fatalf("while the turn runs: in hand %q owed %v compaction %+v waits %v, typed %q", st.Current, st.BoundaryOwed, st.Run.Compaction, st.BoundaryWaits(), r.in.text)
	}
	r.set(func(st *state.State) { st.Run.TurnOpen, st.Run.LastStop = false, r.now }) // its stop
	r.tick(time.Second)
	if st := r.state(); st.Run.Compaction.Status != state.CompactTyped || len(r.in.text) != 1 || r.in.text[0] != "/compact" {
		t.Errorf("after the stop: compaction %+v, typed %q", st.Run.Compaction, r.in.text)
	}
}

// The verification of the last phase completes the run; the idle session is told, once, so the model
// sums the run up for the human.
func TestAVerificationCompletesTheRun(t *testing.T) {
	r := resultRig(t)
	r.verifying("P1")
	r.finish("j1", state.JobPassed)
	r.tick(time.Second)
	st := r.state()
	if st.Mode != state.ModeComplete || len(st.Due()) != 1 {
		t.Fatalf("mode %s due %+v", st.Mode, st.Due())
	}
	if len(r.in.text) != 1 || r.in.text[0] != "[baton] Job j1 (P1) finished; its result is attached." {
		t.Fatalf("typed %q", r.in.text)
	}
	r.run(30 * time.Second)
	if len(r.in.text) != 1 {
		t.Fatalf("typed again before the prompt arrived: %q", r.in.text)
	}
	r.set(func(st *state.State) { state.TakeDue(st, r.now) }) // the prompt's hook took the result
	r.run(DefaultTiming.ResumeNudge * 2)
	if len(r.in.text) != 1 {
		t.Fatalf("typed after the result was given: %q", r.in.text)
	}
}

// A result due to an idle session waits for every gate anything baton types waits for, and for the
// human when baton is asking them something; a draft in the way is saved and cleared.
func TestAResultWaitsForTheGates(t *testing.T) {
	r := resultRig(t)
	r.set(func(st *state.State) {
		st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Command: "make test", Status: state.JobFailed, Exit: 1}}
	})
	set := func(fn func(*state.State)) func() { return func() { r.set(fn) } }
	blockers := []struct {
		name    string
		on, off func()
	}{
		{"turn open", set(func(st *state.State) { st.Run.TurnOpen = true }), set(func(st *state.State) { st.Run.TurnOpen = false })},
		{"dialog", set(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "Bash"}) }), set(func(st *state.State) { st.Run.Dialogs.Clear() })},
		{"human typing", func() { r.view.LastHumanKey = r.now }, func() { r.view.LastHumanKey = r.now.Add(-time.Hour) }},
		{"screen busy", func() { r.view.LastOutput = r.now }, func() { r.view.LastOutput = r.now.Add(-time.Hour) }},
		{"escalated", set(func(st *state.State) { st.Run.Escalation = &state.Escalation{Kind: "stalled", Since: r.now} }), set(func(st *state.State) { st.Run.Escalation = nil })},
		{"blocked", set(func(st *state.State) { state.SetBlocked(st, "x", r.now) }), set(func(st *state.State) { st.Blocked = nil })},
		{"checkpoint owed", set(func(st *state.State) { st.CheckpointOwed = true }), set(func(st *state.State) { st.CheckpointOwed = false })},
		{"compacting", set(func(st *state.State) { st.Run.Compaction.Status = state.CompactActive }), set(func(st *state.State) { st.Run.Compaction.Status = "" })},
		{"paused", set(func(st *state.State) { st.Mode = state.ModePaused }), set(func(st *state.State) { st.Mode = state.ModeRunning })},
	}
	for _, b := range blockers {
		b.on()
		r.tick(100 * time.Millisecond)
		if len(r.in.text) != 0 {
			t.Fatalf("typed while %s: %q", b.name, r.in.text)
		}
		b.off()
	}
	r.view.Draft, r.view.DraftText = true, "half a thought"
	r.tick(time.Second)
	r.tick(DefaultTiming.DraftGrace + time.Second)
	if r.in.cleared != 1 || !strings.Contains(r.events(), "draft_rescued") {
		t.Fatalf("the draft in the way was not cleared: %d", r.in.cleared)
	}
	if len(r.in.text) != 1 || r.in.text[0] != "[baton] Job j1 (P1) finished; its result is attached." {
		t.Fatalf("typed %q", r.in.text)
	}
	if !strings.Contains(r.events(), `"jobs":"j1","kind":"results_typed"`) {
		t.Errorf("events:\n%s", r.events())
	}
}

// A session's own proposal is to be asked first: its job results wait for it, and the watchdog reminds the
// idle session of it.
func TestASessionsOwnProposalIsAskedFirst(t *testing.T) {
	r := resultRig(t)
	r.set(func(st *state.State) {
		state.AddProposal(st, state.Decision{What: "use a fake"}, r.now)
		st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Command: "make test", Status: state.JobFailed, Exit: 1}}
	})
	r.tick(time.Second)
	if len(r.in.text) != 0 {
		t.Errorf("typed %q", r.in.text)
	}
	r.set(func(st *state.State) { st.Jobs = nil })
	r.run(3 * DefaultTiming.IdleNudge)
	if !strings.Contains(r.events(), "idle_nudge") {
		t.Errorf("not reminded:\n%s", r.events())
	}
}
