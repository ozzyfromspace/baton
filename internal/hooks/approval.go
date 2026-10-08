package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// When the human approves a plan in plan mode, baton attaches it, instead of trusting the model to
// remember to (docs/research/sessions.md). PostToolUse(ExitPlanMode) is the approval: a plan sent back
// fires no hook at all. Its tool_response names the plan file.
//
// A plan with phase headings is attached at once if the run has no plan or has finished one, and the
// first phase starts after a compaction, so it begins from a brief rather than the whole planning
// conversation. If a run is under way, baton asks the human what to do with the approved plan. A plan
// without phase headings is left alone: not every plan is a baton plan.

// clearedPlanTTL is how long a "clear context" approval waits for the first tool call of the new session
// to confirm it.
const clearedPlanTTL = 30 * time.Minute

// approvedFile is the plan file a PostToolUse(ExitPlanMode) approved.
func approvedFile(in map[string]any) string {
	resp, _ := in["tool_response"].(map[string]any)
	if f := str(resp, "filePath"); f != "" {
		return f
	}
	ti, _ := in["tool_input"].(map[string]any)
	return str(ti, "planFilePath")
}

// approved acts on a plan the human approved. fresh says the context is already clear (a "clear context"
// approval): the first phase then starts at once, with no compaction.
func approved(st *state.State, s *state.Store, c Context, file string, fresh bool, w decide.Words) valveAction {
	doc, err := os.ReadFile(file)
	if err != nil {
		return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": file, "why": err.Error()},
			say: "baton: plan approved, but baton could not read " + file + " to attach it"}
	}
	spec := plan.Suggest(doc)
	if !plan.Shaped(spec) {
		return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": file, "why": "no phase headings"},
			say: "baton: plan approved — not attached, as it has no phase headings like `## P0 — …` (/baton run attaches it anyway)"}
	}
	pl, err := plan.Build(file, doc, spec)
	if err != nil {
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": file, "why": first},
			say: "baton: plan approved — not attached: " + first + " (/baton run attaches it with a corrected spec)"}
	}
	st.Run.HumanAt = c.Now // approving a plan is taking part
	switch st.Mode {
	case state.ModeRunning, state.ModePaused:
		cur, _ := s.LoadPlan()
		same := cur.File == file
		q := decide.ReplanQuestion(cur.Title, st.Current, st.Mode, same)
		st.Replan = &state.Replan{File: file, Same: same, Question: q.Text, Since: c.Now}
		return valveAction{event: "replan_asked", fields: map[string]any{"plan": file, "same": same},
			say:  fmt.Sprintf("baton: you approved a plan while %q is %s → asking you what to do with it", cur.Title, st.Mode),
			tell: NudgePrefix + " Before anything else, " + q.Call() + ". baton acts on the answer itself; then follow what it tells you."}
	}
	return attach(st, s, c, pl, fresh, w)
}

// attach makes pl the run's plan, from its first phase.
func attach(st *state.State, s *state.Store, c Context, pl plan.Plan, fresh bool, w decide.Words) valveAction {
	if err := s.WritePlan(pl); err != nil {
		return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": pl.File, "why": err.Error()},
			say: "baton: plan approved, but baton could not attach it: " + err.Error()}
	}
	first := pl.Phases[0]
	v := valveAction{event: "attached", fields: map[string]any{"plan": pl.File, "phases": len(pl.Phases), "git": gitx.Usable(s.Root), "by": "approval"}}
	if fresh {
		*st = state.Reattach(*st, pl, c.Now, state.OriginOf(s.Root))
		v.say = fmt.Sprintf("baton: attached %q (%d phases) → %s starts now", pl.Title, len(pl.Phases), first.ID)
		v.tell = w.AttachedNow(pl.Title, len(pl.Phases), pl.File, first.ID, first.Title)
		v.also = []event{{"phase_started", map[string]any{"phase": first.ID}}}
		return v
	}
	*st = state.AttachAtBoundary(*st, pl, c.Now)
	v.say = fmt.Sprintf("baton: attached %q (%d phases) → compacting the planning conversation, then %s starts", pl.Title, len(pl.Phases), first.ID)
	v.tell = decide.AttachedAtBoundary(pl.Title, len(pl.Phases), pl.File, first.ID)
	return v
}

// phaseList names phases as the human knows them: "P3 (Polish) and P4 (Docs)".
func phaseList(pl plan.Plan, ids []string) string {
	var names []string
	for _, id := range ids {
		names = append(names, phaseTitle(pl, id))
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// answeredReplan acts on the human's answer about a plan they approved while the run had one.
func answeredReplan(st *state.State, s *state.Store, c Context, w decide.Words) valveAction {
	r := st.Replan
	if r == nil {
		return valveAction{}
	}
	answer, found := answerTo(c, r.Question)
	if !found {
		return valveAction{}
	}
	st.Replan, st.Run.HumanAt = nil, c.Now
	v := valveAction{event: "replan_resolved", fields: map[string]any{"plan": r.File, "answer": answer}}
	switch answer {
	case "Continue with the revised plan", "Start the new plan from P0":
		doc, err := os.ReadFile(r.File)
		var pl plan.Plan
		if err == nil {
			pl, err = plan.Build(r.File, doc, plan.Suggest(doc))
		}
		if err != nil {
			first := strings.SplitN(err.Error(), "\n", 2)[0]
			v.fields["error"] = first
			v.say = "baton: the approved plan no longer attaches (" + first + "); the current run is unchanged"
			v.tell = NudgePrefix + " The human chose " + strconv.Quote(answer) + ", but the plan file no longer attaches (" + first + "). Nothing changed. Tell the human, then wait for them."
			return v
		}
		if answer == "Start the new plan from P0" {
			a := attach(st, s, c, pl, false, w)
			a.event, a.fields = v.event, v.fields
			a.also = append([]event{{"attached", map[string]any{"plan": pl.File, "phases": len(pl.Phases), "git": gitx.Usable(s.Root), "by": "approval"}}}, a.also...)
			return a
		}
		if err := s.WritePlan(pl); err != nil {
			v.say = "baton: could not write the revised plan: " + err.Error()
			return v
		}
		next := state.Revise(st, pl)
		v.also = []event{{"revised", map[string]any{"plan": pl.File, "phases": len(pl.Phases), "next": next}}}
		switch {
		case next == "":
			v.say = "baton: revised — every phase of the revision is already done"
			v.tell = NudgePrefix + " The human chose to continue with the revised plan, and every phase in it is already done. Summarize for the human and end your turn."
		case st.BoundaryOwed:
			v.say = fmt.Sprintf("baton: revised → compacting, then %s starts", phaseTitle(pl, next))
			v.tell = NudgePrefix + " The human chose to continue with the revised plan. End your turn now: baton compacts the context and starts " + phaseTitle(pl, next) + " with a fresh brief."
		default:
			v.say = "baton: revised — " + phaseTitle(pl, next) + " carries on"
			v.tell = fmt.Sprintf("%s The human chose to continue with the revised plan (%s). Carry on with %s as the revision describes it.", NudgePrefix, pl.File, phaseTitle(pl, next))
		}
	case "Keep the current run":
		v.say = "baton: keeping the current run; the approved plan is set aside"
		v.tell = NudgePrefix + " The human chose to keep the current run. Leave the plan they approved aside and carry on with " + st.Current + "."
	default:
		v.say = "baton: your answer about the approved plan went to Claude; the run is unchanged"
		v.tell = fmt.Sprintf("%s The human answered baton's question about the plan they approved in their own words: %s. baton changed nothing. "+
			"Do what they say; if they want the approved plan run, `/baton run %s` attaches it.", NudgePrefix, strconv.Quote(answer), r.File)
	}
	return v
}

// clearedApproval confirms a plan approved with "clear context". The approval left the plan's dialog on
// record when the old session ended (SessionEnd, reason clear); the new session's first tool call,
// outside plan mode and before any prompt, shows it was approved rather than sent back.
func clearedApproval(st *state.State, c Context) (string, bool) {
	a := st.Run.ClearedPlan
	if a == nil || str(c.Input, "agent_id") != "" {
		return "", false
	}
	st.Run.ClearedPlan = nil
	if str(c.Input, "permission_mode") == "plan" || c.Now.Sub(a.At) > clearedPlanTTL {
		return "", false
	}
	return a.File, true
}

// replanHold is why tools are held while baton's question about an approved plan waits to be asked.
func replanHold(r *state.Replan) string {
	return NudgePrefix + " Not yet: put baton's question about the plan the human approved to them first: " + replanQuestion(r).Call() + ". Until they answer, nothing else runs."
}

func replanQuestion(r *state.Replan) decide.Question {
	if r.Same {
		return decide.Question{Text: r.Question, Options: decide.ReviseOptions}
	}
	return decide.Question{Text: r.Question, Options: decide.ReplaceOptions}
}

// planDrift checks the run's plan document against the text baton attached (plan.Recheck). A change that
// keeps every phase still to run is followed; one that loses any pauses a running run, since baton
// would brief the next phase from text that is no longer there. Each change is reported once.
func planDrift(st *state.State, s *state.Store, pl plan.Plan, now time.Time) (say string, ev *event) {
	if pl.File == "" || st.Replan != nil || (st.Mode != state.ModeRunning && st.Mode != state.ModePaused) {
		return "", nil
	}
	adopted, changed, err := plan.Recheck(pl, state.Remaining(*st, pl))
	if !changed {
		return "", nil
	}
	if err == nil {
		if s.WritePlan(adopted) != nil {
			return "", nil
		}
		st.PlanDrift = ""
		if added := state.Extend(st, adopted); len(added) > 0 {
			return fmt.Sprintf("baton: the plan file changed and adds %s; baton follows the new text, and runs %s in turn", phaseList(adopted, added), plural(len(added), "it", "them")),
				&event{"plan_changed", map[string]any{"plan": pl.File, "adopted": true, "added": added}}
		}
		return "baton: the plan file changed; every phase still to run is there, so baton follows the new text",
			&event{"plan_changed", map[string]any{"plan": pl.File, "adopted": true}}
	}
	sha := pl.SHA()
	if sha == "" {
		sha = "gone"
	}
	if sha == st.PlanDrift {
		return "", nil
	}
	st.PlanDrift = sha
	first := strings.SplitN(err.Error(), "\n", 2)[0]
	ev = &event{"plan_changed", map[string]any{"plan": pl.File, "adopted": false, "why": first}}
	if st.Mode != state.ModeRunning || state.Pause(st) != nil {
		return "baton: the plan file changed, and baton can no longer follow it: " + first, ev
	}
	notice(st, "plan_changed", fmt.Sprintf("%s paused: the plan file changed (%s)", pl.Title, first), now)
	return fmt.Sprintf("baton: paused — the plan file changed, and baton can no longer follow it: %s. Fix %s, then /baton run", first, filepath.Base(pl.File)), ev
}
