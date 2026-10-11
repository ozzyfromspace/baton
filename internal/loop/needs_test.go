package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What waits on the human in a worker's own session is on the run's roster, for the lead's status line and
// baton status, and announced in the lead's session once, with where to answer it: nobody watches the
// worker's. A question of the worker's the human chose to answer in its session was announced as they
// chose it, and one the lead puts to them is not the worker's session's to show.
func TestTheLeadHearsWhatWaitsOnTheHumanInAWorkersSession(t *testing.T) {
	for _, c := range []struct {
		name      string
		set       func(x *crew, st *state.State)
		relay     *state.Relay // a question of P1's in the run's queue
		want      string
		announced bool
	}{
		{"a permission prompt", func(x *crew, st *state.State) {
			st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Key: "k1", Since: x.now})
		}, nil, "a permission prompt for Bash", true},
		{"a question on its screen", func(x *crew, st *state.State) {
			st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: "k1", Since: x.now})
		}, nil, "a question on its screen", true},
		{"its watchdog's escalation", func(x *crew, st *state.State) {
			st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "no activity for 2h", Since: x.now, Watchdog: true}
		}, nil, "no activity for 2h", true},
		{"a block the lead puts to the human", func(x *crew, st *state.State) {
			st.Blocked = &state.Block{Reason: "needs a key", Since: x.now}
			st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "needs a key", Since: x.now, Asked: true}
		}, &state.Relay{Kind: state.RelayBlocked, Because: "needs a key"}, "", false},
		{"a block the human chose to answer in its session", func(x *crew, st *state.State) {
			st.Blocked = &state.Block{Reason: "needs a key", Since: x.now}
			st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "needs a key", Since: x.now, Asked: true}
		}, &state.Relay{Kind: state.RelayBlocked, Because: "needs a key", Answered: &state.Resolution{By: state.ByHuman, Answer: "Wait for me"}}, "its block, needs a key", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			x.tick(time.Second)
			x.started("P1")
			x.tick(time.Second)
			since := x.now
			x.sub("P1").Update(func(st *state.State) error { c.set(x, st); return nil })
			if r := c.relay; r != nil {
				r.Phase, r.Since = "P1", since
				x.lead.Update(func(st *state.State) error { st.Relay = append(st.Relay, *r); return nil })
			}
			x.tick(time.Second)
			x.tick(time.Second)
			w := x.worker("P1")
			if w.Needs != c.want || (c.want != "") != !w.NeedsSince.IsZero() {
				t.Fatalf("the roster says it needs %q since %s, want %q", w.Needs, w.NeedsSince, c.want)
			}
			ev, ann := x.events(), strings.Join(x.state().Run.Announce, "\n")
			n := strings.Count(ev, `"kind":"worker_waiting"`)
			said := strings.Contains(ann, "baton: the worker on P1 waits for you: "+c.want+". Answer it in its session: baton view P1, in another terminal")
			if c.announced != (n == 1 && said) || !c.announced && (n > 0 || strings.Contains(ann, "waits for you")) {
				t.Errorf("%d worker_waiting events, announced %q", n, ann)
			}

			// Once it is answered, the roster has nothing waiting, and the next thing is told again.
			x.sub("P1").Update(func(st *state.State) error {
				st.Run.Dialogs.Clear()
				st.Run.Escalation, st.Blocked = nil, nil
				return nil
			})
			x.tick(time.Second)
			if w := x.worker("P1"); w.Needs != "" || !w.NeedsSince.IsZero() {
				t.Errorf("still needs %q", w.Needs)
			}
			if c.announced {
				x.sub("P1").Update(func(st *state.State) error { c.set(x, st); return nil })
				x.tick(time.Second)
				if n := strings.Count(x.events(), `"kind":"worker_waiting"`); n != 2 {
					t.Errorf("told %d times", n)
				}
			}
		})
	}
}

// A permission prompt in a worker's session is pushed as the lead's is, under the project and the
// worker's phase, saying where to answer it.
func TestAWorkersDialogIsPushedUnderItsPhase(t *testing.T) {
	x := newCrew(t, 1)
	p := &pushes{}
	x.c.Lead.Notify = p
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	x.sub("P1").Update(func(st *state.State) error {
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Key: "k1", Since: x.now})
		return nil
	})
	for i := 0; i < 4; i++ {
		x.tick(time.Minute)
	}
	for i, k := range p.kinds {
		if k == "dialog" && p.projects[i] == "demo · P1" && p.details[i] == "waiting for your answer (Bash) in the worker's session: baton view P1" {
			return
		}
	}
	t.Errorf("pushed %q to %q: %q", p.kinds, p.projects, p.details)
}
