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
// (brief.Fresh), built as a compaction's is, so the phase goes on from the plan, the notes and the
// repository. Nothing wakes the model: the human's next prompt does. say is the line for the human.
func (h *handlers) freshStart(c Context, s *state.Store, pl plan.Plan, st state.State, say string) Result {
	doc, _ := os.ReadFile(pl.File)
	handoff, _ := os.ReadFile(s.HandoffPath())
	w := words(c, s)
	in := brief.Input{Kind: brief.Fresh, Plan: pl, Doc: doc, State: st, Handoff: string(handoff), Words: w, Decisions: s.Decisions(st)}
	if s.Worker() != "" {
		in.Tree, in.Needs = s.Root, builtOn(s, st.Current)
	}
	text := brief.Write(in)
	s.Event("brief", map[string]any{"type": brief.Fresh, "phase": st.Current, "chars": len(text)})
	return Result{Output: map[string]any{
		"systemMessage": say,
		"hookSpecificOutput": map[string]any{
			"hookEventName": "SessionStart",
			// The brief lists the run's decisions: the primer leaves them to it.
			"additionalContext": strings.TrimSpace(Primer(pl, st, w, nil)) + "\n\n" + text,
		},
	}}
}
