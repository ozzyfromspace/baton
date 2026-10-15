package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Where baton's mod runs, a phase agent's permission prompt holds only that agent: the lead's compaction,
// its deliveries, a switch and leaving baton all go on under it (spike 36). The lead's own dialog, or one
// of an agent of the model's own, still holds them all; and in serial mode every dialog does, since a key
// baton types lands in the one on screen, and Enter approves a permission prompt.
func TestAPhaseAgentsDialogHoldsOnlyThatAgentWhereTheModRuns(t *testing.T) {
	lead := state.Dialog{Tool: "Bash", Key: "k"}
	own := state.Dialog{Tool: "Bash", Agent: "sub", Key: "k"}
	phase := state.Dialog{Tool: "Bash", Agent: "a1", Key: "k", Phase: "P1"}
	for _, c := range []struct {
		name    string
		mod     bool
		dialogs []state.Dialog
		holds   bool
	}{
		{"the mod, a phase agent's", true, []state.Dialog{phase}, false},
		{"the mod, the lead's", true, []state.Dialog{lead}, true},
		{"the mod, an agent of the model's own", true, []state.Dialog{own}, true},
		{"the mod, a phase agent's and the lead's behind it", true, []state.Dialog{phase, lead}, true},
		{"serial, a phase agent's", false, []state.Dialog{phase}, true},
		{"serial, the lead's", false, []state.Dialog{lead}, true},
	} {
		for _, w := range []struct {
			name string
			set  func(r *rig)
			went func(r *rig) bool
		}{
			{"a boundary's compaction", func(*rig) {}, func(r *rig) bool {
				if c.mod {
					open := r.open()
					return len(open) == 1 && open[0].Kind == bridge.Compact
				}
				return slices.Contains(r.in.text, "/compact")
			}},
			{"a result", func(r *rig) {
				r.set(func(st *state.State) {
					st.BoundaryOwed = false
					state.Start(st, r.now, state.Origin{})
					st.Run.Compaction = state.Compaction{Epoch: 1}
					st.Run.LastActivity, st.Run.LastStop = r.now, r.now
					st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Command: "make test", Status: state.JobFailed, Exit: 1}}
				})
			}, func(r *rig) bool {
				if c.mod {
					open := r.open()
					return len(open) == 1 && open[0].Kind == bridge.Submit && strings.Contains(r.command(open[0].ID).Text, "Job j1 (P1) finished")
				}
				return slices.ContainsFunc(r.in.text, func(s string) bool { return strings.Contains(s, "Job j1 (P1) finished") })
			}},
			{"leaving baton", func(r *rig) {
				r.set(func(st *state.State) { st.Run.ExitAt = r.now })
			}, func(r *rig) bool { return r.in.quit > 0 }},
			{"a switch (the session idle)", func(*rig) {}, func(r *rig) bool { return r.loop.idle(r.view, r.state()) }},
		} {
			t.Run(c.name+"/"+w.name, func(t *testing.T) {
				r := newRig(t)
				if c.mod {
					helloFrom(t, r.loop.Store)
				}
				w.set(r)
				r.set(func(st *state.State) {
					for _, d := range c.dialogs {
						d.Since = r.now
						st.Run.Dialogs.Open(d)
					}
				})
				r.run(20 * time.Second)
				if went := w.went(r); went == c.holds {
					t.Errorf("went %v, want %v: typed %q, the mod's %+v", went, !c.holds, r.in.text, r.open())
				}
				if r.in.cleared != 0 {
					t.Errorf("cleared the box %d times", r.in.cleared)
				}
			})
		}
	}
}

// The stall alarm still sees a phase agent's dialog: the human is told of it already, and the run waits
// on them there. It names the phase whose agent waits.
func TestTheStallAlarmSeesAPhaseAgentsDialog(t *testing.T) {
	after := DefaultTiming.StallAfter
	for _, c := range []struct {
		name    string
		dialogs []state.Dialog
		why     string
	}{
		{"a phase agent's", []state.Dialog{{Tool: "Bash", Agent: "a1", Phase: "P1"}}, "a dialog of P1's agent is open"},
		{"the lead's behind it", []state.Dialog{{Tool: "Bash", Agent: "a1", Phase: "P1"}, {Tool: "Bash"}}, gateDialog},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newStill(t)
			x.setLead(func(st *state.State) {
				for _, d := range c.dialogs {
					d.Since = x.t0
					st.Run.Dialogs.Open(d)
				}
			})
			x.look(x.t0.Add(after + time.Minute))
			if told := x.told(); len(told) != 0 {
				t.Errorf("told %q", told)
			}
			st, _ := x.lead.Load()
			pl, _ := x.lead.LoadPlan()
			if why := x.c.stillWhy(x.now, st, pl, []state.State{st}, nil); why != c.why {
				t.Errorf("still because %q, want %q", why, c.why)
			}
		})
	}
}

// A question of a phase agent's the mod has issued is not issued again while a phase agent's dialog is in
// front of it: the mod's band, which asks it, is not drawn under any dialog (spike 36), and its deadline
// runs all the same.
func TestARelayQuestionWaitsBehindAPhaseAgentsDialog(t *testing.T) {
	x := relayCrew(t)
	x.tick(time.Second)
	x.lead.Update(func(st *state.State) error {
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "a2", Key: "k", Since: x.now, Phase: "P2"})
		return nil
	})
	x.advance(relayLost - time.Second)
	x.tick(time.Second)
	if len(x.asks) != 1 {
		t.Fatalf("asked again behind the dialog: %+v", x.asks)
	}
}
