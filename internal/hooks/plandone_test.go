package hooks

import (
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A plan attached after another completed is announced complete in its turn, by the lead's stop, whether it
// was attached at once (state.Reattach) or after the planning conversation compacts (state.AttachAtBoundary):
// the first plan's end, told, is not the second's. In a real run the second plan finished with no word.
func TestASecondPlanIsAnnouncedComplete(t *testing.T) {
	pl, next := demoPlan(), plan.Plan{Version: 1, Title: "Next", Phases: []plan.Phase{{ID: "Q0", Title: "Only"}}}
	first := running()
	state.Done(&first, pl, "P0", tStop, state.DoneOpts{})
	state.Start(&first, tStop, state.Origin{})
	state.Done(&first, pl, "P1", tStop, state.DoneOpts{})
	if d := decideStop(&first, pl, tStop, decide.Words{Git: true}, nil); !strings.Contains(said(d.output), "plan complete — all 2 phases done") {
		t.Fatalf("the first plan's end said %q", said(d.output))
	}
	cases := []struct {
		name   string
		attach func(state.State) state.State
	}{
		{"attached at once", func(st state.State) state.State { return state.Reattach(st, next, tStop, state.Origin{}) }},
		{"attached at a boundary", func(st state.State) state.State {
			st = state.AttachAtBoundary(st, next, tStop)
			state.Start(&st, tStop, state.Origin{}) // the boundary compaction happened
			return st
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := c.attach(first)
			if _, err := state.Done(&st, next, "Q0", tStop, state.DoneOpts{}); err != nil || st.Mode != state.ModeComplete {
				t.Fatalf("done: %v, mode %s", err, st.Mode)
			}
			d := decideStop(&st, next, tStop, decide.Words{Git: true}, nil)
			if !strings.Contains(said(d.output), "plan complete — its 1 phase done") || !slices.ContainsFunc(d.events, func(e event) bool { return e.kind == "plan_complete" }) {
				t.Errorf("the second plan's end said %q, logged %+v", said(d.output), d.events)
			}
			if n := st.Run.Notices; len(n) == 0 || n[len(n)-1].Kind != "plan_complete" || !strings.HasPrefix(n[len(n)-1].Text, "Next: ") {
				t.Errorf("notices %+v", st.Run.Notices)
			}
		})
	}
}

// A phase agent's landing that completed the plan while the lead's turn ran left it baton's message
// (state.MessageDone): its stop is refused with it, once, for the model to sum the run up, and the end is
// not announced a second time.
func TestTheLeadsStopGivesTheMailOfAPlanALandingCompleted(t *testing.T) {
	pl := demoPlan()
	st := running()
	state.Done(&st, pl, "P0", tStop, state.DoneOpts{})
	state.Start(&st, tStop, state.Origin{})
	state.Done(&st, pl, "P1", tStop, state.DoneOpts{})
	st.Run.CompleteNotified = true // the landing told the human
	text := decide.PlanCompleteMail("P1", 2, state.Saving{}, nil)
	state.Tell(&st, state.Message{Kind: state.MessageDone, Text: text}, tStop)

	d := decideStop(&st, pl, tStop, decide.Words{Git: true}, nil)
	if d.output["decision"] != "block" || d.output["reason"] != text || len(d.mail) != 1 || strings.Contains(said(d.output), "plan complete") {
		t.Fatalf("the stop gave %+v (mail %+v)", d.output, d.mail)
	}
	if d := decideStop(&st, pl, tStop, decide.Words{Git: true}, nil); d.output["decision"] == "block" {
		t.Errorf("the next stop was refused too: %+v", d.output)
	}
	worker := running()
	state.Done(&worker, pl, "P0", tStop, state.DoneOpts{})
	state.Start(&worker, tStop, state.Origin{})
	state.Done(&worker, pl, "P1", tStop, state.DoneOpts{})
	worker.Run.CompleteNotified = true
	state.Tell(&worker, state.Message{Kind: state.MessagePlan, Text: "the plan changed"}, tStop)
	if d := decideStop(&worker, pl, tStop, decide.Words{Git: true, Worker: true}, nil); d.output["decision"] == "block" {
		t.Errorf("a phase agent's stop was refused: %+v", d.output)
	}
}
