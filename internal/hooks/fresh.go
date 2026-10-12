package hooks

import (
	"os"
	"strings"

	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// freshStart is SessionStart for a conversation that starts with a phase in hand (state.InHand) and none
// of its context: the human ran /clear mid-phase. The model gets the primer and the phase's brief
// (freshContext), so the phase goes on from the plan, the notes and the repository. Nothing wakes the
// model: the human's next prompt does. say is the line for the human.
func (h *handlers) freshStart(c Context, s *state.Store, pl plan.Plan, st state.State, say string) Result {
	text, n := freshContext(c, s, pl, st)
	s.Event("brief", map[string]any{"type": brief.Fresh, "phase": st.Current, "chars": n})
	return Result{Output: map[string]any{
		"systemMessage": say,
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	}}
}

// freshContext is what a conversation with none of the run's context is given: the primer and, with a
// phase in hand (state.InHand), the phase's brief (brief.Fresh), built as a compaction's is. n is the
// brief's length, 0 for none.
func freshContext(c Context, s *state.Store, pl plan.Plan, st state.State) (text string, n int) {
	w := words(c, s)
	if !st.InHand() {
		return Primer(pl, st, w, s.Decisions(st)), 0
	}
	doc, _ := os.ReadFile(pl.File)
	handoff, _ := os.ReadFile(s.HandoffPath())
	in := brief.Input{Kind: brief.Fresh, Plan: pl, Doc: doc, State: st, Handoff: string(handoff), Words: w, Decisions: s.Decisions(st)}
	if s.Worker() != "" {
		in.Tree, in.Needs = s.Root, builtOn(s, st.Current)
	}
	b := brief.Write(in)
	// The brief lists the run's decisions: the primer leaves them to it.
	return strings.TrimSpace(Primer(pl, st, w, nil)) + "\n\n" + b, len(b)
}
