package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// endByLanding has the worker on P1 land the work that completes the crew's plan, P0, P2 and P3 done.
func (x *crew) endByLanding() {
	x.lead.Update(func(st *state.State) error {
		for _, id := range []string{"P0", "P2", "P3"} {
			st.Phases[id].Status = state.PhaseDone
		}
		st.Current = ""
		st.Run.TurnOpen, st.Run.LastStop, st.Run.LastActivity = false, x.now, x.now
		return nil
	})
	x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
}

// A phase agent's landing that completes the plan leaves the lead baton's message, which names the
// decisions made without the human: the lead, idle, is woken for it once, and its prompt hands it over.
func TestAPlanEndedByALandingWakesTheLead(t *testing.T) {
	x := landingCrew(t)
	x.leadIdle = true
	x.sub("P1").Update(func(st *state.State) error {
		_, err := state.AddNote(st, "used a fake clock", "revert it", x.now)
		return err
	})
	x.endByLanding()
	st := x.state()
	if st.Mode != state.ModeComplete || len(st.Inbox) != 1 || st.Inbox[0].Kind != state.MessageDone {
		t.Fatalf("mode %s, inbox %+v\n%s", st.Mode, st.Inbox, x.events())
	}
	text := st.Inbox[0].Text
	for _, want := range []string{"P1's work landed on the run's branch", "the plan is complete, all 4 phases done (phase agents saved ",
		"Summarize the results for the human, including the 1 decision this run made without them"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message lacks %q: %q", want, text)
		}
	}
	for range 5 {
		x.tick(5 * time.Second)
	}
	if n := x.count("mail_typed", map[string]any{"messages": []string{state.MessageDone}, "via": "mod"}); n != 1 {
		t.Fatalf("woken %d times, want once\n%s", n, x.events())
	}
	if _, out := x.hook("UserPromptSubmit", map[string]any{"prompt": decide.MailDone}); !strings.Contains(out, "including the 1 decision this run made without them") {
		t.Errorf("the prompt gave %s", out)
	}
	if len(x.state().Inbox) != 0 || !x.logged("message_delivered", map[string]any{"message": state.MessageDone, "via": "prompt"}) {
		t.Errorf("not handed over: inbox %+v\n%s", x.state().Inbox, x.events())
	}
}

// A plan attached after another completed is announced complete in its turn when a phase agent's landing
// ends it, whether it was attached at once (state.Reattach) or after the planning conversation compacts
// (state.AttachAtBoundary): the first plan's end, told, is not the second's. In a real run the second plan
// finished with no word and no push.
func TestASecondPlanIsAnnouncedComplete(t *testing.T) {
	next := plan.Plan{Version: 1, Title: "Next", File: "/plans/next.md", Phases: []plan.Phase{
		{ID: "Q0", Title: "lead's", After: []string{}}, {ID: "Q1", Title: "agent's", After: []string{}}}}
	for name, attach := range map[string]func(state.State, time.Time) state.State{
		"attached at once":       func(st state.State, now time.Time) state.State { return state.Reattach(st, next, now, state.Origin{}) },
		"attached at a boundary": func(st state.State, now time.Time) state.State { return state.AttachAtBoundary(st, next, now) },
	} {
		t.Run(name, func(t *testing.T) {
			x := landingCrew(t)
			x.endByLanding()
			if !x.has("plan_complete", `"by":"landed"`) || !x.state().Run.CompleteNotified {
				t.Fatalf("the first plan's end was not told\n%s", x.events())
			}
			x.lead.WritePlan(next)
			x.lead.Update(func(st *state.State) error {
				*st = attach(*st, x.now)
				st.RunBranch = "main"
				return nil
			})
			x.atWork("Q1")
			x.lead.Update(func(st *state.State) error {
				st.Phases["Q0"].Status, st.Current, st.BoundaryOwed = state.PhaseDone, "", false
				return nil
			})
			x.finish("Q1", map[string]string{"two.txt": "two\n"}, false)
			x.tick(time.Second)
			st := x.state()
			if st.Mode != state.ModeComplete || x.count("plan_complete", map[string]any{"by": "landed"}) != 2 ||
				!strings.Contains(x.announced(), "plan complete — all 2 phases done") {
				t.Fatalf("mode %s, the second plan's end not told\n%s", st.Mode, x.events())
			}
			if n := st.Run.Notices; len(n) == 0 || n[len(n)-1].Kind != "plan_complete" || !strings.HasPrefix(n[len(n)-1].Text, "Next: ") {
				t.Errorf("notices %+v", st.Run.Notices)
			}
			if len(st.Inbox) != 1 || st.Inbox[0].Kind != state.MessageDone || !strings.HasPrefix(st.Inbox[0].Text, "[baton] Q1's work landed") {
				t.Errorf("inbox %+v", st.Inbox)
			}
		})
	}
}
