package loop

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// switchRig is the rig running a plan whose P0 and P1 need nothing and P2 needs P0, with P0 in hand and
// waiting on job j1, queued just now, and the session idle.
func switchRig(t *testing.T, phases ...plan.Phase) *rig {
	r := newRig(t)
	if len(phases) == 0 {
		phases = []plan.Phase{{ID: "P0", Title: "a", After: []string{}}, {ID: "P1", Title: "b", After: []string{}}, {ID: "P2", Title: "c", After: []string{"P0"}}}
	}
	pl := plan.Plan{Version: 1, Title: "Demo", File: "/x/demo.md", Phases: phases}
	r.loop.Store.WritePlan(pl)
	r.set(func(st *state.State) {
		*st = state.Reattach(*st, pl, r.now, state.Origin{})
		st.Run.Compaction = state.Compaction{Epoch: 1}
		st.Run.LastActivity, st.Run.LastStop = r.now, r.now
		st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobRunning, Queued: r.now, Notes: "a.go done; make test checks it"}}
	})
	return r
}

func (r *rig) switched() bool {
	st := r.state()
	return st.Current == "P1" && st.Phases["P0"].Status == state.PhaseParked
}

// The session idling on its phase's job is switched to a phase that can go on once the job has run for
// switch_after, and the switch's compaction is typed at once.
func TestTheHostSwitchesALongWait(t *testing.T) {
	r := switchRig(t)
	r.run(DefaultTiming.SwitchAfter - 10*time.Second)
	if r.switched() || len(r.in.text) != 0 {
		t.Fatalf("switched before switch_after: typed %q, state %+v", r.in.text, r.state())
	}
	r.run(20 * time.Second)
	st := r.state()
	ps := st.Phases["P0"]
	if !r.switched() || !st.BoundaryOwed || ps.Park == nil || ps.Park.Reason != state.ParkJob || ps.Park.Job != "j1" || ps.Park.Notes != "a.go done; make test checks it" {
		t.Fatalf("after switch_after: in hand %q, P0 %+v park %+v", st.Current, ps, ps.Park)
	}
	if c := st.Run.Compaction; c.Status != state.CompactTyped || c.Reason != "switch" || len(r.in.text) != 1 || r.in.text[0] != "/compact" {
		t.Fatalf("compaction %+v, typed %q", c, r.in.text)
	}
	if len(st.Run.Announce) != 1 || st.Run.Announce[0] != "baton: P0 waits on job j1 (`make test`) → set aside; compacting, then P1 starts, and P0 resumes once the job ends" {
		t.Errorf("announced %q", st.Run.Announce)
	}
	for _, want := range []string{`"job":"j1","kind":"phase_parked","phase":"P0","reason":"job","status":"running"`,
		`"from":"P0","instance":"inst","job":"j1","kind":"phase_switched","to":"P1"`, `"waited":"5m"}`,
		`"by":"switch","epoch":2,"instance":"inst","kind":"compact_queued","next":"P1","reason":"switch"`} {
		if !strings.Contains(r.events(), want) {
			t.Errorf("events lack %s:\n%s", want, r.events())
		}
	}
}

// Nothing switches while the session is busy, or while the human is asked something; the idle stop that
// follows a turn does.
func TestASwitchWaitsForAnIdleSession(t *testing.T) {
	for _, c := range []struct {
		name    string
		on, off func(*state.State)
	}{
		{"a turn under way", func(st *state.State) { st.Run.TurnOpen = true }, func(st *state.State) { st.Run.TurnOpen, st.Run.LastStop = false, st.Run.LastActivity }},
		{"a dialog open", func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "Bash"}) }, func(st *state.State) { st.Run.Dialogs.Clear() }},
		{"the session's own background work", func(st *state.State) { st.Run.Background = []state.Task{{ID: "b1", Type: "subagent"}} }, func(st *state.State) { st.Run.Background = nil }},
		{"a proposal pending", func(st *state.State) { state.AddProposal(st, state.Decision{What: "x"}, st.Run.LastStop) }, func(st *state.State) { st.Decisions = nil }},
	} {
		r := switchRig(t)
		r.set(c.on)
		r.run(DefaultTiming.SwitchAfter + time.Minute)
		if r.switched() {
			t.Errorf("%s: switched", c.name)
			continue
		}
		r.set(c.off)
		r.tick(time.Second)
		if !r.switched() {
			t.Errorf("%s: no switch once it was over", c.name)
		}
	}
}

// With no other phase that can go on, the session idles in place on its job as long as it runs: no
// switch, and no reminder either.
func TestNothingToSwitchTo(t *testing.T) {
	r := switchRig(t, plan.Phase{ID: "P0", Title: "a", After: []string{}}, plan.Phase{ID: "P1", Title: "b", After: []string{"P0"}})
	r.run(DefaultTiming.IdleNudge * 3)
	if st := r.state(); st.Current != "P0" || st.Phases["P0"].Status != state.PhaseActive || st.Run.Compaction.InFlight() || len(r.in.text) != 0 {
		t.Fatalf("typed %q, state %+v", r.in.text, st)
	}
}

// A command known to take longer than switch_after switches as soon as the session idles on it.
func TestASlowCommandSwitchesAtOnce(t *testing.T) {
	r := switchRig(t)
	r.set(func(st *state.State) {
		past := state.Job{ID: "j0", Phase: "P0", Command: "make test", Status: state.JobFailed, Exit: 1, Started: r.now.Add(-time.Hour), Ended: r.now.Add(-40 * time.Minute), Delivered: r.now}
		st.Jobs = append([]state.Job{past}, st.Jobs...)
	})
	r.tick(time.Second)
	if !r.switched() || !strings.Contains(r.events(), `"waited":"1s"`) {
		t.Fatalf("no switch at once: %+v\n%s", r.state(), r.events())
	}
}

// A switch whose job ends before its compaction is typed is called off: the human never hears of it, and
// the result is given in place as soon as baton may type.
func TestASwitchTheJobBeatsIsCalledOff(t *testing.T) {
	r := switchRig(t)
	r.view.LastHumanKey = r.now.Add(DefaultTiming.SwitchAfter) // the human types as the switch is queued
	r.run(DefaultTiming.SwitchAfter + time.Second)
	if st := r.state(); !r.switched() || st.Run.Compaction.Status != state.CompactQueued || len(r.in.text) != 0 {
		t.Fatalf("not queued and held: typed %q, %+v", r.in.text, st.Run.Compaction)
	}
	r.finish("j1", state.JobPassed)
	r.tick(time.Second)
	st := r.state()
	if st.Current != "P0" || st.BoundaryOwed || st.Phases["P0"].Status != state.PhaseActive || st.Run.Compaction.Status != state.CompactCancelled || len(st.Run.Announce) != 0 {
		t.Fatalf("not called off: in hand %q owed %v %+v, announced %q", st.Current, st.BoundaryOwed, st.Run.Compaction, st.Run.Announce)
	}
	if !strings.Contains(r.events(), `"from":"P0","instance":"inst","job":"j1","kind":"switch_cancelled","status":"passed","to":"P1"`) {
		t.Errorf("events:\n%s", r.events())
	}
	r.tick(DefaultTiming.HandsOff)
	if len(r.in.text) != 1 || r.in.text[0] != "[baton] Job j1 (P0) finished; its result is attached." {
		t.Fatalf("typed %q", r.in.text)
	}
}

// A switch typed already stands when its job ends: the phase set aside comes back at the next switch
// point, with its result.
func TestATypedSwitchStands(t *testing.T) {
	r := switchRig(t)
	r.run(DefaultTiming.SwitchAfter + time.Second)
	r.finish("j1", state.JobPassed)
	r.tick(time.Second)
	if st := r.state(); !r.switched() || st.Run.Compaction.Status != state.CompactTyped || len(st.Due()) != 0 {
		t.Fatalf("a typed switch was undone: %+v", st)
	}
}

// A phase whose work is not committed is never set aside: the work would sit in the checkout while another
// phase works there. Once it is committed, the switch goes ahead.
func TestUncommittedWorkHoldsASwitch(t *testing.T) {
	gittest.Isolate(t)
	r := switchRig(t)
	root := t.TempDir()
	gittest.Init(t, root)
	gittest.Write(t, root, "a.go", "package a\n")
	gittest.Commit(t, root, "init", "a.go")
	proj, _ := state.OpenProject(filepath.Join(root, ".baton"), func() time.Time { return r.now })
	store, err := proj.Bind(state.Binding{Session: "s1", Instance: "inst"})
	if err != nil {
		t.Fatal(err)
	}
	pl, _ := r.loop.Store.LoadPlan()
	st := r.state()
	st.Phases["P0"].StartDirty = &state.Dirt{} // nothing was uncommitted when P0 started
	store.WritePlan(pl)
	store.Update(func(x *state.State) error { *x = st; return nil })
	r.loop.Store = store
	gittest.Write(t, root, "a.go", "package a // edited after verify\n")

	r.run(DefaultTiming.SwitchAfter + time.Minute)
	if r.switched() || len(r.in.text) != 0 {
		t.Fatalf("switched with P0's work uncommitted: typed %q", r.in.text)
	}
	if n := strings.Count(r.events(), `"kind":"switch_held"`); n != 1 || !strings.Contains(r.events(), `"files":1,"instance":"inst","job":"j1","kind":"switch_held","phase":"P0","ts":"`) {
		t.Errorf("held %d times; events:\n%s", n, r.events())
	}
	if got := r.state().Run.Announce; len(got) != 1 || got[0] != "baton: P0's job j1 has run 5m, but P0 has 1 uncommitted file, so baton keeps it in hand rather than set it aside for P1" {
		t.Errorf("announced %q", got)
	}
	gittest.Commit(t, root, "edit", "a.go")
	r.run(switchRecheck)
	if !r.switched() {
		t.Fatal("no switch once the work was committed")
	}
}
