package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// upgradeRig is the rig with a newer baton installed and a session id on record.
func upgradeRig(t *testing.T) (*rig, *string) {
	r := newRig(t)
	newer := "v0.3.2"
	r.loop.Version, r.loop.Newer = "v0.3.1", func() string { return newer }
	r.set(func(st *state.State) { st.Run.SessionID = "sess-1" })
	return r, &newer
}

func (r *rig) restarted() bool {
	_, _, ok := r.loop.Restart()
	return ok
}

// A running plan restarts on a newer baton at the compaction baton is about to type, and only there: the
// restarted session types it. Nothing is typed into the claude that is on its way out.
func TestARunningPlanRestartsInsteadOfTypingTheCompaction(t *testing.T) {
	r, _ := upgradeRig(t)
	r.tick(time.Second)
	to, session, ok := r.loop.Restart()
	if !ok || to != "v0.3.2" || session != "sess-1" || r.in.quit != 1 {
		t.Fatalf("restart %q %q %v, quit %d", to, session, ok, r.in.quit)
	}
	if len(r.in.text) != 0 {
		t.Fatalf("typed %q into a claude that is ending", r.in.text)
	}
	st := r.state()
	if rs := st.Run.Restart; rs == nil || rs.From != "v0.3.1" || rs.To != "v0.3.2" {
		t.Fatalf("Runtime.Restart: %+v", rs)
	}
	if st.Run.Compaction.Status != state.CompactQueued {
		t.Fatalf("the compaction must stay queued for the restarted session: %+v", st.Run.Compaction)
	}
	b, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"restarting"`) || !strings.Contains(string(b), `"to":"v0.3.2"`) {
		t.Fatalf("events: %s", b)
	}
	r.run(time.Minute)
	if r.in.quit != 1 || len(r.in.text) != 0 {
		t.Fatalf("after the restart began: quit %d, typed %q", r.in.quit, r.in.text)
	}
}

// Mid-phase, a running plan never restarts, however long it sits: the next compaction is the place.
func TestARunningPlanDoesNotRestartMidPhase(t *testing.T) {
	r, _ := upgradeRig(t)
	r.set(func(st *state.State) {
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, Finished: r.now}
	})
	r.run(30 * time.Minute)
	if r.restarted() {
		t.Fatal("restarted mid-phase")
	}
}

// Whatever lives only inside the claude process would end with it, so it holds a restart (and the
// compaction goes ahead as usual: a restart never holds a plan up).
func TestBackgroundWorkHoldsARestartButNotThePlan(t *testing.T) {
	for name, set := range map[string]func(*state.State){
		"a background shell": func(st *state.State) { st.Run.Background = []state.Task{{Type: "shell"}} },
		"a monitor": func(st *state.State) {
			st.Run.Background, st.Run.Monitors = []state.Task{{ID: "m1", Type: "shell"}}, []string{"m1"}
		},
		"a session-scoped cron":  func(st *state.State) { st.Run.Crons = 1 },
		"a background subagent ": func(st *state.State) { st.Run.Background = []state.Task{{Type: "subagent"}} },
		"one of baton's phase agents": func(st *state.State) {
			st.Run.Background = []state.Task{{Type: "subagent", AgentType: state.PhaseAgent}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := upgradeRig(t)
			r.set(set)
			r.tick(time.Second)
			if r.restarted() {
				t.Fatal("restarted")
			}
		})
	}
	r, _ := upgradeRig(t)
	r.set(func(st *state.State) { st.Run.Background = []state.Task{{Type: "shell"}} })
	r.tick(time.Second)
	if len(r.in.text) != 1 || !strings.Contains(r.in.text[0], "/compact") {
		t.Fatalf("the compaction must go ahead: typed %q", r.in.text)
	}
}

// With no plan running, a session restarts once it has been idle for RestartIdle: no turn, no keys, no
// dialog, no draft.
func TestAnIdleSessionRestarts(t *testing.T) {
	r, _ := upgradeRig(t)
	r.set(func(st *state.State) {
		st.Mode = state.ModeIdle
		st.Run.Compaction = state.Compaction{}
		st.Run.LastStop = r.now
	})
	r.run(r.loop.Timing.RestartIdle - 10*time.Second)
	if r.restarted() {
		t.Fatal("restarted before the session was idle long enough")
	}
	r.run(20 * time.Second)
	if !r.restarted() || r.in.quit != 1 {
		t.Fatal("an idle session did not restart")
	}
}

func TestABusySessionDoesNotRestart(t *testing.T) {
	blockers := map[string]func(r *rig){
		"turn open": func(r *rig) {
			r.set(func(st *state.State) { st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now })
		},
		"dialog": func(r *rig) {
			r.set(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion"}) })
		},
		"draft": func(r *rig) { r.view.Draft = true },
		"compacting": func(r *rig) {
			r.set(func(st *state.State) {
				st.Run.Compaction = state.Compaction{Status: state.CompactActive, Started: r.now}
			})
		},
	}
	for name, block := range blockers {
		t.Run(name, func(t *testing.T) {
			r, _ := upgradeRig(t)
			r.set(func(st *state.State) { st.Mode, st.Run.Compaction = state.ModePaused, state.Compaction{} })
			block(r)
			r.run(2 * r.loop.Timing.RestartIdle)
			if r.restarted() {
				t.Fatal("restarted")
			}
		})
	}
	// A key pressed starts the idle time over.
	r, _ := upgradeRig(t)
	r.set(func(st *state.State) { st.Mode, st.Run.Compaction = state.ModePaused, state.Compaction{} })
	r.view.LastHumanKey = r.now
	r.run(r.loop.Timing.RestartIdle - 10*time.Second)
	if r.restarted() {
		t.Fatal("restarted while the human was at the keyboard")
	}
	r.run(20 * time.Second)
	if !r.restarted() {
		t.Fatal("did not restart once the keyboard was idle")
	}
}

func TestNoNewerBatonNoRestart(t *testing.T) {
	r, newer := upgradeRig(t)
	*newer = ""
	r.tick(time.Second)
	if r.restarted() || len(r.in.text) != 1 {
		t.Fatalf("restarted %v, typed %q", r.restarted(), r.in.text)
	}
	r, _ = upgradeRig(t)
	r.set(func(st *state.State) { st.Run.SessionID = "" }) // no conversation to resume
	r.tick(time.Second)
	if r.restarted() {
		t.Fatal("restarted without a session to resume")
	}
}

// With workers, an upgrade restart waits until every one of them is idle, since it ends their sessions
// too. A running plan does not wait for that: the lead compacts as usual, and restarts at the next safe
// point. From the moment a newer baton waits, no new worker starts. Once the restart is under way, a
// worker's session ending is the host's end, not a crash: it is neither resumed nor retired.
func TestARestartWaitsForTheWorkers(t *testing.T) {
	x := newCrew(t, 2)
	x.tick(time.Second)
	x.started("P1")
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastActivity = true, x.now; return nil })
	x.c.Lead.Version, x.c.Lead.Newer = "v0.5.0", func() string { return "v0.5.1" }
	boundary := func(epoch int) {
		x.lead.Update(func(st *state.State) error {
			st.Run.SessionID = "sess-1"
			st.Run.Compaction = state.Compaction{Epoch: epoch, Status: state.CompactQueued, Reason: "boundary", Queued: x.now}
			return nil
		})
	}
	boundary(1)
	x.tick(time.Second)
	if _, _, ok := x.c.Lead.Restart(); ok {
		t.Fatal("restarted while a worker was mid-turn")
	}
	if st := x.state(); st.Run.Compaction.Status != state.CompactTyped {
		t.Errorf("the plan waited for the restart: %+v", st.Run.Compaction)
	}
	if ev := x.events(); !strings.Contains(ev, `"kind":"restart_held"`) || !strings.Contains(ev, `"why":"the worker on P1 is at work"`) {
		t.Errorf("events:\n%s", x.events())
	}
	const held = "baton: baton v0.5.1 is installed; this session restarts on it once the workers are idle (now the worker on P1 is at work)"
	if ann := strings.Join(x.state().Run.Announce, "\n"); strings.Count(ann, "restarts on it once the workers are idle") != 1 || !strings.Contains(ann, held) {
		t.Errorf("announced %q", ann)
	}
	x.tick(time.Second)
	if len(x.specs) != 1 {
		t.Errorf("a worker started while a restart waits: %d", len(x.specs))
	}

	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = false; return nil })
	x.tick(workerQuiet)
	boundary(2)
	x.tick(time.Second)
	if to, _, ok := x.c.Lead.Restart(); !ok || to != "v0.5.1" {
		t.Fatal("did not restart once the worker was idle")
	}
	_, f := x.last()
	f.exit(0)
	x.tick(time.Second)
	if w := x.worker("P1"); len(x.specs) != 1 || w.Status != state.WorkerWorking || w.Resumes != 0 {
		t.Errorf("the worker's end was taken for a crash: %d sessions, %+v", len(x.specs), w)
	}
}

// What a worker's session may be doing that an upgrade restart, which ends it, would lose.
func TestWorkerBusy(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	launched := now.Add(-time.Hour)
	idle := state.Runtime{TurnStarted: launched.Add(time.Minute), LastActivity: now.Add(-workerQuiet)}
	for _, c := range []struct {
		name string
		set  func(*state.Runtime)
		want string
	}{
		{"idle", nil, ""},
		{"not started yet", func(r *state.Runtime) { r.TurnStarted = launched.Add(-time.Second) }, "has not started yet"},
		{"mid-turn", func(r *state.Runtime) { r.TurnOpen = true }, "is at work"},
		{"its screen not quiet yet", func(r *state.Runtime) { r.LastActivity = now.Add(-time.Second) }, "is at work"},
		{"a dialog open", func(r *state.Runtime) { r.Dialogs.Open(state.Dialog{Tool: "Bash"}) }, "waits on a dialog"},
		{"a background shell", func(r *state.Runtime) { r.Background = []state.Task{{ID: "b1", Type: "shell"}} }, "has background work running"},
		{"a monitor", func(r *state.Runtime) {
			r.Background, r.Monitors = []state.Task{{ID: "m1", Type: "shell"}}, []string{"m1"}
		}, "has background work running"},
		{"a session-scoped cron", func(r *state.Runtime) { r.Crons = 1 }, "has background work running"},
		{"compacting", func(r *state.Runtime) { r.Compaction = state.Compaction{Status: state.CompactActive} }, "is compacting"},
	} {
		r := idle
		if c.set != nil {
			c.set(&r)
		}
		if got := workerBusy(r, now, launched); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Once the lead's claude is asked to end because the human leaves baton, the host is on its way out: no
// worker starts any more, and a worker's session ending with it is suspended as the host ends, not
// resumed as a crash.
func TestLeavingBatonStartsNoMoreWorkers(t *testing.T) {
	x := newCrew(t, 2)
	x.tick(time.Second)
	x.lead.Update(func(st *state.State) error { st.Run.ExitAt = x.now; return nil })
	x.tick(time.Second)
	if !x.c.Lead.quit {
		t.Fatal("the lead did not leave")
	}
	_, f := x.last()
	f.exit(0)
	x.tick(time.Second)
	x.tick(time.Second)
	if w := x.worker("P1"); len(x.specs) != 1 || w.Status != state.WorkerStarting || w.Resumes != 0 {
		t.Errorf("%d sessions started; P1's worker %+v", len(x.specs), w)
	}
}
