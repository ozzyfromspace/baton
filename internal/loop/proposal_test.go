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
// since `opened` ago. outward makes it one answered Not now at its deadline.
func proposalRig(t *testing.T, outward bool) (*rig, time.Time) {
	r := newIdleRig(t)
	deadline := decide.Deadline(r.now, DefaultTiming.EscalationTimeout)
	r.set(func(st *state.State) {
		d := state.Decision{What: "commit P1 unsigned", Because: "gpg times out", Undo: "re-sign it", Asked: true, Deadline: deadline}
		d.Question = decide.ProposalQuestion(d.Because, d.What, d.Deadline).Text
		if outward {
			d.What, d.Outward = "push the branch", "'push' looks outward-facing"
			d.Question = decide.OutwardQuestion(d.Because, d.What, d.Outward, d.Deadline).Text
		}
		state.AddProposal(st, d, r.now)
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(d.Question), Since: r.now, Kind: "proposal"})
	})
	return r, deadline
}

// runTo ticks until t.
func (r *rig) runTo(t time.Time) { r.run(t.Sub(r.now)) }

// A proposal nobody answers goes ahead at the time its question names: baton types 3, Go ahead. An
// outward one's third option is Not now, so the same key answers it so.
func TestAProposalGoesAheadAtItsDeadline(t *testing.T) {
	for _, outward := range []bool{false, true} {
		answer := "Go ahead"
		if outward {
			answer = "Not now"
		}
		t.Run(answer, func(t *testing.T) { aProposalGoesAheadAtItsDeadline(t, outward, answer) })
	}
}

func aProposalGoesAheadAtItsDeadline(t *testing.T, outward bool, answer string) {
	r, deadline := proposalRig(t, outward)
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
	if st := r.state(); decide.OptionsOf(*st.PendingProposal())[2].Label != answer {
		t.Fatalf("option 3 is %q, want %q", decide.OptionsOf(*st.PendingProposal())[2].Label, answer)
	}
	b, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"dialog":"proposal"`) || !strings.Contains(string(b), `"id":"d1"`) || !strings.Contains(string(b), `"key":"3"`) ||
		!strings.Contains(string(b), `"answer":"`+answer+`"`) {
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

// A proposal whose question the human dismissed, or that the model never put to them, is nobody's
// answer: at its deadline baton answers it all the same, with no dialog to answer, and tells the model. It
// goes ahead, or an outward one is answered Not now, which leaves it for the human unless it is the safe
// kind.
func TestTheLeadsDismissedProposalGoesAheadAtItsDeadline(t *testing.T) {
	for _, c := range []struct {
		name      string
		outward   bool
		dismissed bool
		answer    string
		told, say string
	}{
		{"dismissed", false, true, "Go ahead", "Nobody answered your proposal d1", "went ahead with d1"},
		{"never asked", false, false, "Go ahead", "Nobody answered your proposal d1", "went ahead with d1"},
		{"outward, dismissed", true, true, state.NotNow, "so baton answered Not now for the human", "so baton answered Not now for you on d1"},
		{"outward, never asked", true, false, state.NotNow, "so baton answered Not now for the human", "so baton answered Not now for you on d1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, deadline := proposalRig(t, c.outward)
			r.set(func(st *state.State) {
				st.Run.Dialogs = nil
				st.Run.TurnOpen, st.Run.LastStop = false, r.now
				if c.dismissed {
					st.PendingProposal().Dismissed = r.now
				} else {
					st.PendingProposal().Asked = false
				}
			})
			r.runTo(deadline.Add(-10 * time.Second))
			if st := r.state(); st.PendingProposal() == nil {
				t.Fatal("settled before its deadline")
			}
			r.run(20 * time.Second)
			st := r.state()
			d := st.Decisions[0]
			if d.Resolved == nil || d.Resolved.By != state.ByTimeout || d.Resolved.Answer != c.answer || d.LeftForHuman() != c.outward {
				t.Fatalf("proposal %+v", d)
			}
			told := false
			for _, m := range st.Inbox {
				told = told || m.Kind == state.MessageAnswer && strings.Contains(m.Text, c.told)
			}
			if !told {
				t.Errorf("the model is not told: inbox %+v", st.Inbox)
			}
			b, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl"))
			if !strings.Contains(string(b), `"kind":"proposal_resolved"`) || !strings.Contains(string(b), `"via":"lapsed"`) ||
				!strings.Contains(string(b), `"answer":"`+c.answer+`"`) || strings.Contains(string(b), `"asked":false`) == c.dismissed {
				t.Fatalf("events: %s", b)
			}
			if !strings.Contains(strings.Join(st.Run.Announce, "\n"), c.say) {
				t.Errorf("not announced: %q", st.Run.Announce)
			}
		})
	}
	// Asked and not dismissed, the question is on screen, and baton answers it there (above), never here.
	r, deadline := proposalRig(t, false)
	r.set(func(st *state.State) { st.Run.Dialogs = nil })
	r.runTo(deadline.Add(time.Minute))
	if st := r.state(); st.PendingProposal() == nil {
		t.Fatal("a proposal nobody dismissed was settled with no dialog to answer")
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

// Never answered: one that is not alone on screen, and a dialog marked as a proposal that is not the
// question the pending proposal issued.
func TestProposalsBatonNeverAnswers(t *testing.T) {
	for name, setup := range map[string]func(r *rig){
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
			r, _ := proposalRig(t, false)
			setup(r)
			r.run(time.Hour)
			if len(r.in.text) != 0 {
				t.Fatalf("typed %q", r.in.text)
			}
		})
	}
}

// baton's own question about the run, waiting the escalation timeout with no dialog of it on screen, is
// answered for the human: the plan goes on, and a plan approved mid-run gets its default. A block, a
// review the human opted into, a dialog on screen (the mod answers that) or a paused run: none is.
func TestBatonsOwnQuestionsLapse(t *testing.T) {
	type lapsed struct{ question, answer string }
	for _, c := range []struct {
		name string
		set  func(st *state.State, now time.Time)
		want *lapsed
	}{
		{"a stall", func(st *state.State, now time.Time) {
			st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "r", Since: now, Question: "baton: r", Asked: true}
		}, &lapsed{"baton: r", "Continue"}},
		{"a revised plan", func(st *state.State, now time.Time) {
			st.Replan = &state.Replan{File: "p.md", Same: true, Question: "baton: which?", Since: now}
		}, &lapsed{"baton: which?", "Continue with the revised plan"}},
		{"another plan", func(st *state.State, now time.Time) {
			st.Replan = &state.Replan{File: "q.md", Question: "baton: which?", Since: now}
		}, &lapsed{"baton: which?", "Keep the current run"}},
		{"a block", func(st *state.State, now time.Time) {
			st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "r", Since: now, Question: "baton: r", Asked: true}
		}, nil},
		{"a review", func(st *state.State, now time.Time) {
			st.Run.Escalation = &state.Escalation{Kind: "review", Reason: "r", Since: now, Question: "baton: r", Asked: true}
		}, nil},
		{"on screen", func(st *state.State, now time.Time) {
			st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "r", Since: now, Question: "baton: r", Asked: true}
			st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize("baton: r"), Since: now, Kind: "escalation"})
		}, nil},
		{"paused", func(st *state.State, now time.Time) {
			st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "r", Since: now, Question: "baton: r", Asked: true}
			st.Mode = state.ModePaused
		}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newIdleRig(t)
			var got []lapsed
			r.loop.Lapse = func(s *state.Store, question, answer string, now time.Time) error {
				got = append(got, lapsed{question, answer})
				return nil
			}
			r.set(func(st *state.State) { c.set(st, r.now) })
			r.run(DefaultTiming.EscalationTimeout - 20*time.Second)
			if len(got) != 0 {
				t.Fatalf("answered before the escalation timeout: %+v", got)
			}
			r.run(30 * time.Second)
			switch {
			case c.want == nil && len(got) != 0:
				t.Fatalf("answered %+v", got)
			case c.want != nil && (len(got) == 0 || got[0] != *c.want):
				t.Fatalf("answered %+v, want %+v", got, *c.want)
			}
		})
	}
}
