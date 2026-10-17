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
	"github.com/ozzyfromspace/baton/internal/replan"
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
// approval): the first phase then starts at once, with no compaction. g is the ground read for it before
// the run's lock (readGround).
func approved(st *state.State, s *state.Store, c Context, file string, fresh bool, w decide.Words, g *ground) valveAction {
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
	spec.Root = s.Root // where its record file, if it names one, is checked
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
		// The run's own file is revised in place; one baton moved out of Claude Code's way is revised where
		// it was (Origin), and the revision moves aside in its turn (revisedAside).
		revision := cur.Origin != "" && cur.Origin == file
		same := cur.File == file || revision
		q := decide.PlanQuestion(cur.Title, st.Current, st.Mode, same)
		st.PlanQuestion = &state.PlanQuestion{File: file, Same: same, Question: q.Text, Since: c.Now}
		v := valveAction{event: "plan_question_asked", fields: map[string]any{"plan": file, "same": same},
			say:  fmt.Sprintf("baton: you approved a plan while %q is %s → asking you what to do with it", cur.Title, st.Mode),
			tell: NudgePrefix + " Before anything else, " + q.Call() + ". baton acts on the answer itself; then follow what it tells you."}
		if revision && g != nil {
			v.revisedAside(st.PlanQuestion, pl, g.aside)
		}
		return v
	}
	return attach(st, s, c, pl, fresh, w, g)
}

// attach makes pl the run's plan, from its first phase, keeping the record of the plan it replaces (the
// action's record, which keep writes once the run's lock is let go). What it needs of git and the machine
// is g, read before the lock (readGround).
func attach(st *state.State, s *state.Store, c Context, pl plan.Plan, fresh bool, w decide.Words, g *ground) valveAction {
	if g == nil || fresh && g.origin == nil {
		// Never so: every caller reads the ground whenever it may attach. Better unattached than attached on
		// a working tree it did not read.
		return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": pl.File, "why": "the working tree was not read"},
			say: "baton: plan approved, but baton could not attach it (/baton run attaches it)"}
	}
	// The session's plan-mode file moves out of Claude Code's way first, and the plan follows it (state.Aside).
	file := pl.File
	rec, err := s.Record(*st)
	var moved bool
	var unmoved error
	if err == nil {
		moved, unmoved = g.aside.Move(&pl)
		if err = s.WritePlan(pl); err != nil && moved {
			state.Unmove(&pl)
		}
	}
	if err != nil {
		return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": pl.File, "why": err.Error()},
			say: "baton: plan approved, but baton could not attach it: " + err.Error()}
	}
	first := pl.Phases[0]
	// Its text is what every replan of it is compared with, kept with the record once the lock is let go.
	v := valveAction{event: "attached", fields: attachedFields(pl, g), record: rec, approved: &pl}
	branch := g.branch // workers branch off it, and their work lands on it
	workers := ""
	if line := g.room.attached(pl); line != "" {
		workers = "\nbaton: " + line
	}
	if fresh {
		*st = state.Reattach(*st, pl, c.Now, *g.origin)
		st.RunBranch, st.PlanLooked = branch, true
		v.say = fmt.Sprintf("baton: attached %q (%s; %s) → %s starts now", pl.Title, decide.PhaseCount(len(pl.Phases)), pl.Dependencies(), first.ID) + workers
		v.tell = w.AttachedNow(pl.Title, len(pl.Phases), pl.File, first.ID, first.Title)
		v.also = append(v.also, event{"phase_started", map[string]any{"phase": first.ID}})
		v.movedAside(pl, g.aside, file, moved, unmoved, "approval")
		return v
	}
	*st = state.AttachAtBoundary(*st, pl, c.Now)
	st.RunBranch, st.PlanLooked = branch, true
	v.say = fmt.Sprintf("baton: attached %q (%s; %s) → compacting the planning conversation, then %s starts", pl.Title, decide.PhaseCount(len(pl.Phases)), pl.Dependencies(), first.ID) + workers
	v.tell = decide.AttachedAtBoundary(pl.Title, len(pl.Phases), pl.File, first.ID)
	v.movedAside(pl, g.aside, file, moved, unmoved, "approval")
	return v
}

// keep writes, in s's run, the record of the plan v's approval replaced (Store.Record, taken under the
// run's lock), now that the lock is let go, and logs it first among v's events: plan_archived, or
// plan_not_archived with why. Then it keeps the text of the plan approved (replan.KeepApproved), which
// every replan of it is compared with, its earlier replans going with the record. It does nothing for an
// action that approved no plan.
func (v *valveAction) keep(s *state.Store) {
	var dir string
	if v.record != nil {
		var err error
		dir, err = v.record.Save()
		e := event{"plan_archived", map[string]any{"generation": v.record.Generation, "dir": dir}}
		if err != nil {
			e = event{"plan_not_archived", map[string]any{"generation": v.record.Generation, "why": err.Error()}}
		}
		v.also = append([]event{e}, v.also...)
	}
	if v.approved != nil {
		replan.KeepApproved(s, *v.approved, dir)
	}
	v.record, v.approved = nil, nil
}

// A ground is what attaching a plan reads of the working tree and the machine. It is read before the
// run's lock is taken (readGround), since no git command, and no reading of the machine, runs under it.
type ground struct {
	git    bool          // git is usable in the run's working tree
	branch string        // the branch checked out there: workers branch off it, and their work lands on it
	origin *state.Origin // where the repository stands, for a first phase that starts at once (nil: not read)
	room   workerRoom
	aside  state.Aside // what moving the plan file out of Claude Code's way needs (lookAside)
}

// readGround reads the ground for attaching a plan in s's run; origin says to read where the repository
// stands as well, for a "clear context" approval, whose first phase starts at once.
func readGround(c Context, s *state.Store, origin bool) *ground {
	g := &ground{git: gitx.Usable(s.Root), branch: gitx.Branch(s.Root), room: readWorkerRoom(c, s.Checkout)}
	if origin {
		o := state.OriginOf(s.Root)
		g.origin = &o
	}
	return g
}

// mayStartOver reports whether an AskUserQuestion call's answers in c include starting an approved plan
// over from P0, which attaches it (answeredPlanQuestion): its ground is then read before the lock.
func mayStartOver(c Context) bool {
	resp, _ := c.Input["tool_response"].(map[string]any)
	answers, _ := resp["answers"].(map[string]any)
	for _, a := range answers {
		if s, _ := a.(string); strings.TrimSpace(s) == "Start the new plan from P0" {
			return true
		}
	}
	return false
}

// A workerRoom is how many workers the settings and the machine allow beside the session (config's
// WorkerCap) and the permission mode they would work in, read for a plan's attach (readGround).
type workerRoom struct {
	n        int
	auto, ok bool
	mode     string
}

// readWorkerRoom reads the room for workers of a run in checkout.
func readWorkerRoom(c Context, checkout string) workerRoom {
	read := c.Machine
	if read == nil {
		read = machine.Read
	}
	n, auto, err := config.Load(config.Root(c.Env), c.Env).WorkerCap(read)
	if err != nil {
		return workerRoom{}
	}
	return workerRoom{n: n, auto: auto, ok: true, mode: config.WorkerMode(c.Env, checkout)}
}

// attached says, as plan pl is attached, how many workers will work beside the session, and warns when
// they would stop at permission prompts (decide.WorkersAttached). "" with no workers.
func (r workerRoom) attached(pl plan.Plan) string {
	if len(pl.Phases) < 2 || !r.ok {
		return "" // nothing could go ahead beside a plan's one phase
	}
	return decide.WorkersAttached(r.n, r.auto, pl.Width(), r.mode)
}

// attachedFields are the fields of the "attached" event for a plan the human approved, on ground g.
func attachedFields(pl plan.Plan, g *ground) map[string]any {
	k, _ := pl.LongestChain()
	f := map[string]any{"plan": pl.File, "phases": len(pl.Phases), "longest_chain": k, "git": g.git, "by": "approval"}
	if g.branch != "" {
		f["branch"] = g.branch
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

// answeredPlanQuestion acts on the human's answer about a plan they approved while the run had one. auto says
// baton answered it for them, since nobody had by its deadline (decide.PlanQuestionDefault). g is the
// ground read before the run's lock for an answer that starts the plan over (mayStartOver), nil otherwise.
func answeredPlanQuestion(st *state.State, s *state.Store, c Context, w decide.Words, auto bool, g *ground) valveAction {
	r := st.PlanQuestion
	if r == nil {
		return valveAction{}
	}
	answer, found := answerTo(c, r.Question)
	if !found {
		return valveAction{}
	}
	auto = auto && answer == decide.PlanQuestionDefault(r.Same) // baton only ever answers the default
	if answer == "Wait for me" {
		// Held for the human: nothing changes until they write (userPromptSubmit), and with no word from
		// them the hold lapses into the default (loop's lapseQuestions).
		st.Run.HumanAt, r.Held = c.Now, c.Now
		v := valveAction{event: "plan_question_held", fields: map[string]any{"plan": r.File, "answer": answer, "by": "human"}}
		v.say, v.tell = decide.PlanQuestionHolding(r.File, decide.PlanQuestionDefault(r.Same), decide.HoldLapse)
		return v
	}
	st.PlanQuestion = nil
	by, chose := "human", decide.Chose(auto, c.Now)
	if auto {
		by = "timeout"
	} else {
		st.Run.HumanAt = c.Now
	}
	v := valveAction{event: "plan_question_resolved", fields: map[string]any{"plan": r.File, "answer": answer, "by": by}}
	switch answer {
	case "Continue with the revised plan", "Start the new plan from P0":
		doc, err := os.ReadFile(r.File)
		var pl plan.Plan
		if err == nil {
			spec := plan.Suggest(doc)
			spec.Root = s.Root
			pl, err = plan.Build(r.File, doc, spec)
		}
		if err != nil {
			first := strings.SplitN(err.Error(), "\n", 2)[0]
			v.fields["error"] = first
			v.say = "baton: the approved plan no longer attaches (" + first + "); the current run is unchanged"
			v.tell = NudgePrefix + " " + chose + " " + strconv.Quote(answer) + ", but the plan file no longer attaches (" + first + "). Nothing changed. Tell the human, and carry on with " + decide.Named(st.Current) + "."
			return v
		}
		pl.Origin = r.Origin // a revision baton moved aside as it was approved (revisedAside)
		if answer == "Start the new plan from P0" {
			a := attach(st, s, c, pl, false, w, g)
			if a.event == "attached" {
				a.also = append([]event{{"attached", a.fields}}, a.also...)
			}
			a.event, a.fields = v.event, v.fields
			if st.Mode == state.ModePaused {
				state.Resume(st) // the human's answer starts it, paused or not
				a.also = append(a.also, event{"resumed", map[string]any{"by": "plan_question"}})
			}
			return a
		}
		rec, err := s.Record(*st)
		if err == nil {
			err = s.WritePlan(pl)
		}
		if err != nil {
			v.say = "baton: could not write the revised plan: " + err.Error()
			return v
		}
		// The revision is the plan the human approved now: replans are compared with it from here on. Its
		// text and the record are kept once the run's lock is let go (keep).
		v.record, v.approved = rec, &pl
		st.Replan, st.Replans = nil, nil
		st.RulesVersion = plan.RulesVersion // made by the planning rules of the baton running it
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

// planQuestionHold is why tools are held while baton's question about an approved plan waits to be asked.
func planQuestionHold(r *state.PlanQuestion) string {
	return NudgePrefix + " Not yet: put baton's question about the plan the human approved to them first: " + planQuestionOf(r).Call() + ". Until they answer, nothing else runs."
}

func planQuestionOf(r *state.PlanQuestion) decide.Question {
	if r.Same {
		return decide.Question{Text: r.Question, Options: decide.ReviseOptions}
	}
	return decide.Question{Text: r.Question, Options: decide.ReplaceOptions}
}

// planDrift checks the run's plan document against the text baton attached (plan.Recheck). A change that
// keeps every phase still to run is followed; one that loses any is reported, and the run goes on with the
// plan as attached, since nothing would answer for the human while they are away. Each change is reported once. A run
// that holds no phase takes up one the change lets start (state.Extend), and planDrift returns it as
// took: it starts after the boundary compaction this stop queues. While a replan is open, the plan file is
// the replan's to change (package replan), and left alone; a text a replan kept copied back over it undoes
// that replan and those after it, and is followed as such (replan.Restore).
func planDrift(st *state.State, s *state.Store, pl plan.Plan, now time.Time) (say string, ev *event, took string) {
	if pl.File == "" || st.PlanQuestion != nil || st.Replan != nil || (st.Mode != state.ModeRunning && st.Mode != state.ModePaused) {
		return "", nil, ""
	}
	adopted, changed, err := plan.Recheck(pl, state.Remaining(*st, pl))
	if !changed {
		return "", nil, ""
	}
	if u, ok := replan.Restore(st, s, pl, now); ok && u.Err == nil {
		return decide.ReplanRestored(u.N, u.Changes), &event{"replan_undone", u.Fields()}, u.Took
	} else if ok {
		err = u.Err
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
