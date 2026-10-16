package hooks

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A plan the human approves in plan mode is the session's plan-mode file, which Claude Code attaches in
// full to the conversation after every compaction while it exists (spike 38). As baton attaches it, it
// moves it out of the way in its folder (state.Aside) and follows it there; the file's old path is the
// plan's Origin, and an approval of that path, which Claude Code writes afresh at the session's next
// planning, is a revision of the run's plan, moved aside in its turn as it is approved.

// lookAside reads, before the run's lock, what moving the plan file an approval would attach needs
// (state.Project.LookAside): the session's plan slug from its transcript, and whether another live run's
// plan is the file. The file is the one the approval names, else the one a "clear context" approval left
// on record (peek, the run's state a moment before), else the one baton's question about an approved plan
// is about.
func lookAside(c Context, s *state.Store, peek state.State) state.Aside {
	var file string
	switch {
	case str(c.Input, "tool_name") == "ExitPlanMode":
		file = approvedFile(c.Input)
	case peek.Run.ClearedPlan != nil:
		file = peek.Run.ClearedPlan.File
	case peek.PlanQuestion != nil:
		file = peek.PlanQuestion.File
	}
	return s.ProjectOf().LookAside(s.Ref, file, str(c.Input, "transcript_path"))
}

// movedAside adds to v what moving the plan file file out of Claude Code's way did (state.Aside.Move,
// under the run's lock), by what (approval, revision): plan_moved and its line for the human when it moved
// to pl's file, or plan_not_moved with why for the session's plan-mode file left where it is. Its line
// comes after the ones v says already.
func (v *valveAction) movedAside(pl plan.Plan, a state.Aside, file string, moved bool, err error, by string) {
	switch {
	case moved:
		v.also = append(v.also, event{"plan_moved", map[string]any{"from": pl.Origin, "to": pl.File, "by": by}})
		v.say = lines(v.say, decide.PlanMoved(pl.Origin, pl.File))
	case err != nil:
		v.also = append(v.also, event{"plan_not_moved", map[string]any{"plan": file, "why": err.Error(), "by": by}})
	case a.PlanMode(file) && a.UsedBy != "":
		v.also = append(v.also, event{"plan_not_moved", map[string]any{"plan": file, "why": "another live run's plan", "run": a.UsedBy, "by": by}})
	}
}

// revisedAside moves a revision of the run's plan, approved in the file baton moved that plan from
// (Origin), out of Claude Code's way as it is approved, whatever the human answers about it: baton's
// question q is then about the revision where it is now, with the plan's Origin, which an answer that runs
// it keeps (answeredPlanQuestion). pl is the revision as approved.
func (v *valveAction) revisedAside(q *state.PlanQuestion, pl plan.Plan, a state.Aside) {
	file := pl.File
	moved, err := a.Move(&pl)
	if moved {
		q.File, q.Origin = pl.File, pl.Origin
		v.fields["plan"] = pl.File
	}
	v.movedAside(pl, a, file, moved, err, "revision")
}
