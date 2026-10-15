package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/machine"
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

// attach makes pl the run's plan, from its first phase, keeping the record of the plan it replaces
// (Store.Archive).
func attach(st *state.State, s *state.Store, c Context, pl plan.Plan, fresh bool, w decide.Words) valveAction {
	kept, err := s.Archive(*st)
	if err == nil {
		err = s.WritePlan(pl)
	}
	if err != nil {
		return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": pl.File, "why": err.Error()},
			say: "baton: plan approved, but baton could not attach it: " + err.Error()}
	}
	first := pl.Phases[0]
	v := valveAction{event: "attached", fields: attachedFields(pl, s.Root)}
	if kept != "" {
		v.also = append(v.also, archived(*st, kept))
	}
	branch := gitx.Branch(s.Root) // workers branch off it, and their work lands on it
	workers := ""
	if line := workersAttached(c, s.Checkout, pl); line != "" {
		workers = "\nbaton: " + line
	}
	if fresh {
		*st = state.Reattach(*st, pl, c.Now, state.OriginOf(s.Root))
		st.RunBranch = branch
		v.say = fmt.Sprintf("baton: attached %q (%d phases; %s) → %s starts now", pl.Title, len(pl.Phases), pl.Dependencies(), first.ID) + workers
		v.tell = w.AttachedNow(pl.Title, len(pl.Phases), pl.File, first.ID, first.Title)
		v.also = append(v.also, event{"phase_started", map[string]any{"phase": first.ID}})
		return v
	}
	*st = state.AttachAtBoundary(*st, pl, c.Now)
	st.RunBranch = branch
	v.say = fmt.Sprintf("baton: attached %q (%d phases; %s) → compacting the planning conversation, then %s starts", pl.Title, len(pl.Phases), pl.Dependencies(), first.ID) + workers
	v.tell = decide.AttachedAtBoundary(pl.Title, len(pl.Phases), pl.File, first.ID)
	return v
}

// archived is the event of the record of the plan st runs, kept in dir as another replaces it.
func archived(st state.State, dir string) event {
	return event{"plan_archived", map[string]any{"generation": st.Generation, "dir": dir}}
}

// workersAttached says, as plan pl is attached in checkout, how many workers will work beside the session,
// and warns when they would stop at permission prompts (decide.WorkersAttached). "" with no workers.
func workersAttached(c Context, checkout string, pl plan.Plan) string {
	read := c.Machine
	if read == nil {
		read = machine.Read
	}
	n, auto, err := config.Load(config.Root(c.Env), c.Env).WorkerCap(read)
	if err != nil {
		return ""
	}
	mode := config.WorkerMode(c.Env, checkout)
	return decide.WorkersAttached(n, auto, pl.Width(), mode)
}

// attachedFields are the fields of the "attached" event for a plan the human approved.
func attachedFields(pl plan.Plan, root string) map[string]any {
	k, _ := pl.LongestChain()
	f := map[string]any{"plan": pl.File, "phases": len(pl.Phases), "longest_chain": k, "git": gitx.Usable(root), "by": "approval"}
	if b := gitx.Branch(root); b != "" {
		f["branch"] = b
	}
	return f
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

// answeredReplan acts on the human's answer about a plan they approved while the run had one. auto says
// baton answered it for them, since nobody had by its deadline (decide.ReplanDefault).
func answeredReplan(st *state.State, s *state.Store, c Context, w decide.Words, auto bool) valveAction {
	r := st.Replan
	if r == nil {
		return valveAction{}
	}
	answer, found := answerTo(c, r.Question)
	if !found {
		return valveAction{}
	}
	st.Replan = nil
	by, chose := "human", decide.Chose(auto, c.Now)
	if auto {
		by = "timeout"
	} else {
		st.Run.HumanAt = c.Now
	}
	v := valveAction{event: "replan_resolved", fields: map[string]any{"plan": r.File, "answer": answer, "by": by}}
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
			v.tell = NudgePrefix + " " + chose + " " + strconv.Quote(answer) + ", but the plan file no longer attaches (" + first + "). Nothing changed. Tell the human, and carry on with " + decide.Named(st.Current) + "."
			return v
		}
		if answer == "Start the new plan from P0" {
			a := attach(st, s, c, pl, false, w)
			a.event, a.fields = v.event, v.fields
			a.also = append([]event{{"attached", attachedFields(pl, s.Root)}}, a.also...)
			if st.Mode == state.ModePaused {
				state.Resume(st) // the human's answer starts it, paused or not
				a.also = append(a.also, event{"resumed", map[string]any{"by": "replan"}})
			}
			return a
		}
		kept, err := s.Archive(*st)
		if err == nil {
			err = s.WritePlan(pl)
		}
		if err != nil {
			v.say = "baton: could not write the revised plan: " + err.Error()
			return v
		}
		if kept != "" {
			v.also = append(v.also, archived(*st, kept))
		}
		next := state.Revise(st, pl, c.Now)
		v.also = append(v.also, event{"revised", map[string]any{"plan": pl.File, "phases": len(pl.Phases), "next": next}})
		switch {
		case next == "" && st.Mode == state.ModeRunning:
			v.say = "baton: revised — no phase of the revision can start until a job finishes"
			v.tell = NudgePrefix + " " + chose + " to continue with the revised plan. Every phase of it still to run waits for a job to finish: end your turn, and baton wakes you when there is something to do."
		case next == "":
			v.say = "baton: revised — every phase of the revision is already done"
			v.tell = NudgePrefix + " " + chose + " to continue with the revised plan, and every phase in it is already done. Summarize for the human and end your turn."
		case st.BoundaryOwed:
			v.say = fmt.Sprintf("baton: revised → compacting, then %s starts", phaseTitle(pl, next))
			v.tell = NudgePrefix + " " + chose + " to continue with the revised plan. End your turn now: baton compacts the context and starts " + phaseTitle(pl, next) + " with a fresh brief."
		default:
			v.say = "baton: revised — " + phaseTitle(pl, next) + " carries on"
			v.tell = fmt.Sprintf("%s %s to continue with the revised plan (%s). Carry on with %s as the revision describes it.", NudgePrefix, chose, pl.File, phaseTitle(pl, next))
		}
	case "Keep the current run":
		v.say = "baton: keeping the current run; the approved plan is set aside"
		v.tell = NudgePrefix + " " + chose + " to keep the current run. Leave the plan they approved aside and carry on with " + decide.Named(st.Current) + "."
		if auto {
			v.say = fmt.Sprintf("baton: nobody answered by %s → keeping the current run; /baton run %s starts the plan you approved", c.Now.Local().Format("15:04"), r.File)
		}
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
// keeps every phase still to run is followed; one that loses any is reported, and the run goes on with the
// plan as attached, since nothing would answer for the human while they are away. Each change is reported once. A run
// that holds no phase takes up one the change lets start (state.Extend), and planDrift returns it as
// took: it starts after the boundary compaction this stop queues.
func planDrift(st *state.State, s *state.Store, pl plan.Plan, now time.Time) (say string, ev *event, took string) {
	if pl.File == "" || st.Replan != nil || (st.Mode != state.ModeRunning && st.Mode != state.ModePaused) {
		return "", nil, ""
	}
	adopted, changed, err := plan.Recheck(pl, state.Remaining(*st, pl))
	if !changed {
		return "", nil, ""
	}
	if err == nil {
		if s.WritePlan(adopted) != nil {
			return "", nil, ""
		}
		st.PlanDrift = ""
		var added []string
		added, took = state.Extend(st, adopted, now)
		ev = &event{"plan_changed", map[string]any{"plan": pl.File, "adopted": true}}
		if len(added) > 0 {
			ev.fields["added"] = added
		}
		if took != "" {
			ev.fields["next"] = took
		}
		switch {
		case len(added) > 0 && took != "":
			say = fmt.Sprintf("baton: the plan file changed and adds %s; baton follows the new text", phaseList(adopted, added))
		case len(added) > 0:
			say = fmt.Sprintf("baton: the plan file changed and adds %s; baton follows the new text, and runs %s in turn", phaseList(adopted, added), plural(len(added), "it", "them"))
		default:
			say = "baton: the plan file changed; every phase still to run is there, so baton follows the new text"
		}
		return say, ev, took
	}
	sha := pl.SHA()
	if sha == "" {
		sha = "gone"
	}
	if sha == st.PlanDrift {
		return "", nil, ""
	}
	st.PlanDrift = sha
	first := strings.SplitN(err.Error(), "\n", 2)[0]
	ev = &event{"plan_changed", map[string]any{"plan": pl.File, "adopted": false, "why": first, "kept": true}}
	// The run goes on with the plan as attached rather than waiting on the human to fix the file: the
	// phases' briefs say what they cannot quote, and the model reads the file itself. baton follows the
	// file again once it reads.
	notice(st, "plan_changed", fmt.Sprintf("%s goes on with the plan as attached: the plan file changed so baton cannot follow it (%s)", pl.Title, first), now)
	return fmt.Sprintf("baton: the plan file changed, and baton cannot follow it (%s) → it goes on with the plan as attached; fix %s and baton follows the file again",
		first, filepath.Base(pl.File)), ev, ""
}
