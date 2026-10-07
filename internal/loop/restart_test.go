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
		"a background shell":     func(st *state.State) { st.Run.Background = []state.Task{{Type: "shell"}} },
		"a monitor":              func(st *state.State) { st.Run.Background = []state.Task{{Type: "monitor"}} },
		"a session-scoped cron":  func(st *state.State) { st.Run.Crons = 1 },
		"a background subagent ": func(st *state.State) { st.Run.Background = []state.Task{{Type: "subagent"}} },
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
