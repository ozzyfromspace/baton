package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A conversation /clear starts with a phase in hand gets the primer and the phase's brief, so the phase
// goes on; one that starts anywhere else in the run gets the primer alone: with a boundary owed, the
// compaction's brief comes next.
func TestAClearedConversationGetsItsPhasesBrief(t *testing.T) {
	for _, c := range []struct {
		name, source string
		plan         bool
		set          func(*state.State, plan.Plan)
		brief        bool
		say, primer  string // the human's line, and what the primer says
	}{
		{name: "a phase in hand", source: "clear", plan: true, brief: true,
			say: "baton: conversation cleared — P0 (First) goes on, with its brief", primer: "Current phase: P0 — First"},
		{name: "a checkpoint due", source: "clear", plan: true, brief: true,
			set: func(st *state.State, _ plan.Plan) { st.CheckpointDue = true },
			say: "baton: conversation cleared — P0 (First) goes on, with its brief", primer: "Current phase: P0 — First"},
		{name: "a boundary owed", source: "clear", plan: true,
			set: func(st *state.State, pl plan.Plan) {
				state.Done(st, pl, "P0", st.Phases["P0"].StartedAt, state.DoneOpts{})
			},
			say: "baton: hosting · Demo · 1/2 phases done · current P1", primer: "P1 has not started: baton compacts the context first"},
		{name: "paused", source: "clear", plan: true,
			set: func(st *state.State, _ plan.Plan) { state.Pause(st) },
			say: "baton: hosting · Demo · 0/2 phases done · current P0 · paused", primer: "baton is paused"},
		{name: "a resumed conversation", source: "resume", plan: true,
			say: "baton: hosting · Demo · 0/2 phases done · current P0", primer: "Current phase: P0 — First"},
		{name: "no plan", source: "clear",
			say: "baton: hosting this session (no plan attached — /baton plan or /baton run)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.plan)
			if c.plan {
				pl, _ := f.store.LoadPlan()
				f.set(func(st *state.State) {
					state.AddNote(st, "committed P0 unsigned", "re-sign it", f.now)
					if c.set != nil {
						c.set(st, pl)
					}
				})
				f.store.AppendHandoff("P0 (checkpoint)", "Half of a in abc123.")
			}
			in := map[string]any{"source": c.source}
			if c.source == "clear" {
				in["session_id"] = "sess-2" // a cleared conversation has a session id of its own
			}
			out := f.fire("SessionStart", in)
			if out["systemMessage"] != c.say {
				t.Errorf("systemMessage = %q, want %q", out["systemMessage"], c.say)
			}
			ctx := additionalContext(out)
			if !strings.Contains(ctx, c.primer) || c.plan && !strings.HasPrefix(ctx, "This session is hosted by baton") {
				t.Errorf("the primer lacks %q:\n%s", c.primer, ctx)
			}
			briefed := f.logged("brief", map[string]any{"type": "fresh", "phase": "P0"})
			if !c.brief {
				if strings.Contains(ctx, "[baton]") || briefed {
					t.Errorf("a brief where the primer alone belongs (logged: %v):\n%s", briefed, ctx)
				}
				return
			}
			for _, want := range []string{
				"Never type /compact yourself, and don't stop between phases", // the primer's whole way to report
				"\n\n[baton] This conversation started without the phase's context",
				"Now: continue P0 — First. Some of it may be done already",
				"Phase P0, from the plan:\n\n## P0 — First\nDo a.",
				"Half of a in abc123.",
			} {
				if !strings.Contains(ctx, want) {
					t.Errorf("the context lacks %q:\n%s", want, ctx)
				}
			}
			if n := strings.Count(ctx, "noted: committed P0 unsigned. Undo: re-sign it"); n != 1 {
				t.Errorf("the record is in the context %d times, want once:\n%s", n, ctx)
			}
			if strings.Contains(ctx, "The conversation grew while baton was paused") || f.state().CheckpointDue {
				t.Errorf("a checkpoint is still due for the conversation /clear dropped:\n%s", ctx)
			}
			if !briefed {
				t.Errorf("no brief event:\n%s", f.eventLog())
			}
			if f.state().Run.SessionID != in["session_id"] {
				t.Errorf("the run did not follow the cleared session: %q", f.state().Run.SessionID)
			}
		})
	}
}
