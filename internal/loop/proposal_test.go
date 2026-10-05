package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// proposalRig is an idle run with a proposal put to the human: its question is the only dialog open,
// since `opened` ago. untimed gives it no deadline.
func proposalRig(t *testing.T, untimed bool) (*rig, time.Time) {
	r := newIdleRig(t)
	deadline := decide.Deadline(r.now, DefaultTiming.EscalationTimeout)
	r.set(func(st *state.State) {
		d := state.Decision{What: "commit P1 unsigned", Because: "gpg times out", Undo: "re-sign it", Asked: true}
		if untimed {
			d.Untimed = "it looks outward-facing"
			d.Question = decide.UntimedQuestion(d.Because, d.What, d.Untimed).Text
		} else {
			d.Deadline = deadline
			d.Question = decide.ProposalQuestion(d.Because, d.What, d.Deadline).Text
		}
		state.AddProposal(st, d, r.now)
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(d.Question), Since: r.now, Kind: "proposal"})
	})
	return r, deadline
}

// runTo ticks until t.
func (r *rig) runTo(t time.Time) { r.run(t.Sub(r.now)) }

// A proposal nobody answers goes ahead at the time its question names: baton types 3, Go ahead.
func TestAProposalGoesAheadAtItsDeadline(t *testing.T) {
	r, deadline := proposalRig(t, false)
	r.view.Draft = true // keys go to the dialog: a draft does not matter
	r.runTo(deadline.Add(-10 * time.Second))
	if len(r.in.text) != 0 {
		t.Fatalf("answered before the deadline: %q", r.in.text)
	}
	r.run(20 * time.Second)
	if len(r.in.text) != 1 || r.in.text[0] != "3" {
		t.Fatalf("typed %q", r.in.text)
	}
	if d, _ := r.state().Run.Dialogs.Only(); d.AutoAnswered.IsZero() {
		t.Fatal("the answer is not marked as baton's own")
	}
	b, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"dialog":"proposal"`) || !strings.Contains(string(b), `"id":"d1"`) || !strings.Contains(string(b), `"key":"3"`) {
		t.Fatalf("events: %s", b)
	}
	r.run(10 * time.Minute)
	if len(r.in.text) != 1 {
		t.Fatalf("answered twice: %q", r.in.text)
	}
	// baton answers it itself, so the human is not pushed about a dialog waiting.
	for _, k := range r.push.kinds {
		if k == "dialog" {
			t.Fatalf("pushed %v", r.push.kinds)
		}
	}
}

func TestWhenAProposalGoesAhead(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r *rig, deadline time.Time) // runs at the start
		at    func(r *rig, deadline time.Time) time.Time
	}{
		// Queued behind another dialog until just before the deadline: the human still gets a minute
		// with the question alone on screen.
		{"at least a minute alone", func(r *rig, deadline time.Time) {
			r.set(func(st *state.State) {
				st.Run.Dialogs = append(state.DialogQueue{{Tool: "Bash", Agent: "sub", Key: "k", Since: r.now}}, st.Run.Dialogs...)
			})
			r.runTo(deadline.Add(-20 * time.Second))
			r.set(func(st *state.State) { st.Run.Dialogs.CloseAgent("sub") })
		}, func(r *rig, deadline time.Time) time.Time { return deadline.Add(-20*time.Second + MinProposalWait) }},
		// A key restarts the timer: somebody at the keyboard is answering.
		{"a key restarts it", func(r *rig, deadline time.Time) {
			r.runTo(deadline.Add(-time.Minute))
			r.view.LastHumanKey = r.now
		}, func(r *rig, deadline time.Time) time.Time {
			return deadline.Add(-time.Minute + DefaultTiming.EscalationTimeout)
		}},
		// Asleep through the deadline, nobody could answer: a minute from the wake.
		{"after a sleep", func(r *rig, deadline time.Time) {
			r.tick(time.Hour)
		}, func(r *rig, deadline time.Time) time.Time { return r.now.Add(MinProposalWait) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, deadline := proposalRig(t, false)
			c.setup(r, deadline)
			at := c.at(r, deadline)
			r.runTo(at.Add(-10 * time.Second))
			if len(r.in.text) != 0 {
				t.Fatalf("answered %s early: %q", at.Sub(r.now), r.in.text)
			}
			r.run(20 * time.Second)
			if len(r.in.text) != 1 || r.in.text[0] != "3" {
				t.Fatalf("typed %q", r.in.text)
			}
		})
	}
}

// Never answered: an untimed proposal (only the human can let it through), one that is not alone on
// screen, and a dialog marked as a proposal that is not the question the pending proposal issued.
func TestProposalsBatonNeverAnswers(t *testing.T) {
	for name, setup := range map[string]func(r *rig){
		"untimed": nil,
		"not alone": func(r *rig) {
			r.set(func(st *state.State) {
				st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "sub", Key: "k", Since: r.now})
			})
		},
		"another question": func(r *rig) {
			r.set(func(st *state.State) { st.Run.Dialogs[0].Key = "baton: something else" })
		},
		"already settled": func(r *rig) {
			r.set(func(st *state.State) { state.Resolve(st, "d1", state.ByHuman, "use the staging key", r.now) })
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := proposalRig(t, setup == nil)
			if setup != nil {
				setup(r)
			}
			r.run(time.Hour)
			if len(r.in.text) != 0 {
				t.Fatalf("typed %q", r.in.text)
			}
		})
	}

	// An untimed proposal waits on the human like any dialog: they are pushed about it.
	r, _ := proposalRig(t, true)
	r.run(DefaultTiming.DialogNotify + time.Minute)
	if strings.Join(r.push.kinds, ",") != "dialog" {
		t.Fatalf("pushes %v", r.push.kinds)
	}
}
