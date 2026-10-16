package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// PlanChanged is baton's message to the agent at work on phase once the human changed the plan: what
// changed for it, as the plan reads now. rules is the standing rules and section its phase's section,
// each "" when it reads as the agent was given it.
func PlanChanged(phase, rules, section string) string {
	b := fmt.Sprintf("[baton] The human changed the plan while you work on %s, and baton follows the new text. Below is %s as the plan reads now: "+
		"follow it from here on. The commits you made stay as they are: where one no longer fits, change it with a new commit, and say so in your notes.",
		phase, planParts(phase, rules != "", section != ""))
	for _, text := range []string{rules, section} {
		if text != "" {
			b += "\n\n" + strings.TrimSpace(text)
		}
	}
	return b
}

// PlanTold tells the human that the agent on phase was told the new text of the plan: the standing rules
// (rules), its phase's section (section), or both.
func PlanTold(phase string, rules, section bool) string {
	return fmt.Sprintf("baton: the plan changed → the agent on %s is told %s as it reads now", phase, planParts(phase, rules, section))
}

// planParts names the parts of the plan that changed for the agent on phase.
func planParts(phase string, rules, section bool) string {
	switch {
	case rules && section:
		return "the standing rules and " + phase + "'s section"
	case rules:
		return "the standing rules"
	}
	return phase + "'s section"
}

// What baton says about a replan of the phases not started (`baton replan`, package replan): the brief the
// model replans from, what it is told when its draft is followed or refused, and what the human sees.

// ReplanPhase is a phase as a replan's brief lists it.
type ReplanPhase struct {
	ID, Title string
	Waits     string // what it waits for, as the plan says it: "after P2 and P4", "needs no other phase"
	Stands    string // how a phase that started stands: "done", "in hand", "a phase agent's", …
}

// ReplanBrief is what a replan's brief says.
type ReplanBrief struct {
	Title   string        // the plan's
	By      string        // who opened it (state.ReplanHuman, …)
	Words   string        // what the human asked it to consider
	Draft   string        // the draft's path
	Pending []ReplanPhase // the phases not started, which it may restructure
	Frozen  []ReplanPhase // the phases that started or are done
	Shape   plan.Shape    // the shape of the phases not started (plan.Plan.Part)
	Limits  plan.Limits   // what it may change beyond their order and After: lines
	Max     time.Duration // how long it may stay open (Timing.ReplanMax)
}

// Brief is the model's brief for a replan: what it replans, what it may change, and how to finish.
func (b ReplanBrief) Brief() string {
	var s strings.Builder
	fmt.Fprintf(&s, "[baton] Replan the phases of %q that have not started, so more of them can run at once. Do it now, in this turn.\n\n", b.Title)
	switch {
	case b.By == state.ReplanHuman && b.Words != "":
		fmt.Fprintf(&s, "The human asked you to consider: %q. Weigh it first.\n\n", b.Words)
	case b.By == state.ReplanHuman:
		s.WriteString("The human gave nothing in particular to consider: replan by baton's shape rules below.\n\n")
	}
	s.WriteString("Not started (yours to restructure):\n")
	for _, ph := range b.Pending {
		fmt.Fprintf(&s, "  %s %s — %s\n", ph.ID, ph.Title, ph.Waits)
	}
	if len(b.Frozen) > 0 {
		s.WriteString("Started or done (never change these: each one's heading, text and After: line stay byte for byte, and so do the phases it waits for):\n")
		for _, ph := range b.Frozen {
			fmt.Fprintf(&s, "  %s %s — %s\n", ph.ID, ph.Title, ph.Stands)
		}
	}
	if line := strings.TrimPrefix(PlanShape(b.Shape), "baton: "); line != "" {
		fmt.Fprintf(&s, "The phases not started now: %s.\n", line)
	}
	fmt.Fprintf(&s, "\nbaton's shape rules:\n%s\n\nWhat you may change:\n%s\n", ShapeRules, b.allowed())
	fmt.Fprintf(&s, "\nEdit only the draft, %s, never the plan file: baton checks the draft, then writes it over the plan file itself. Then, still in this turn, run one of:\n"+
		"- `baton replanned --notes \"<what changed and why>\"`: baton checks the draft, follows it, and records the change with its undo. If a check fails, it says exactly what: fix the draft and run it again.\n"+
		"- `baton replanned --none \"<why nothing should change>\"`, when the plan is right as it is.\n"+
		"Keep backticks and $ out of the double-quoted text, or use single quotes. While the replan is open, no new phase agent starts; agents at work go on. "+
		"If it is not finished within %s, baton drops the draft and the plan goes on as it was.", b.Draft, Spell(b.Max))
	return s.String()
}

// allowed lists what the replan may change, as its limits say.
func (b ReplanBrief) allowed() string {
	lines := []string{"- the order of the phases not started, and their `After:` lines (each names only phases before it, so the graph has no cycle)"}
	if b.Limits.Add {
		lines = append(lines, "- split a phase not started: keep its id on one part, and give each other part a new id in the plan's notation (P5b after P5, or the next free number) under a heading like the others'")
	}
	if b.Limits.Edit {
		lines = append(lines, "- reword a phase not started, as a split or the human's words need")
	}
	if b.Limits.Drop {
		lines = append(lines, "- merge or drop phases not started, only as far as the human's words ask; name each phase you drop or merge away in your --notes")
	} else {
		lines = append(lines, "- no phase not started is dropped or merged away: every one keeps its id")
	}
	if b.Limits.Goal {
		lines = append(lines, "- the goal sections (the title and the text before the first phase, the standing rules, the end section) only where the human's words ask; otherwise they stay byte for byte")
	} else {
		lines = append(lines, "- the goal sections (the title and the text before the first phase, the standing rules, the end section) stay byte for byte")
	}
	lines = append(lines, "- a QA phase keeps its `Type: qa` line")
	return strings.Join(lines, "\n")
}

// ReplanOpened tells the human baton opened a replan of the phases not started (by: who asked, with their
// words, if any), and holds new phase agents while it is open, for at most max.
func ReplanOpened(by, words string, max time.Duration) string {
	who := replanBy(by)
	if words != "" {
		who += ": " + fmt.Sprintf("%q", Sanitize(words))
	}
	return fmt.Sprintf("baton: replanning the phases not started (%s) → no new phase agent starts until Claude is done, %s at most", who, Spell(max))
}

// ReplanAgain is the line before the brief when a replan is open already: since when, and who opened it.
func ReplanAgain(by string, since time.Time) string {
	return fmt.Sprintf("baton: a replan is open already (%s, since %s); its brief again:", replanBy(by), since.Local().Format("15:04"))
}

// replanBy says who opened a replan.
func replanBy(by string) string {
	switch by {
	case state.ReplanHuman:
		return "you asked"
	case state.ReplanUpgrade:
		return "a newer baton's planning rules"
	case state.ReplanQA:
		return "a QA phase starts over a chain"
	case state.ReplanMarked:
		return "a phase marked Replan: after is done"
	}
	return by
}

// ReplanStands says how a phase a replan may not change stands, by its status (state.PhaseDone, …); next
// says it is the phase in hand, which may not have started yet.
func ReplanStands(status string, next bool) string {
	switch status {
	case state.PhaseDone:
		return "done"
	case state.PhaseActive:
		return "in hand"
	case state.PhaseWorking:
		return "a phase agent's"
	case state.PhaseVerifying:
		return "verifying"
	case state.PhaseParked:
		return "set aside, to resume"
	}
	if next {
		return "next, after the compaction"
	}
	return "given to a phase agent"
}

// NothingToReplan turns down a replan of a plan whose every phase has started or is done.
const NothingToReplan = "nothing to replan — every phase of the plan has started or is done, and a replan changes only the phases not started."

// ReplanNext is what the model does once its replan is followed: took, a phase the run took up (it starts
// after a compaction); else, by how the run stands (mode, a boundary owed before current, current in
// hand), carry on with it, end the turn, or sum up a plan now complete.
func ReplanNext(took, current, mode string, boundary, inHand bool) string {
	switch {
	case mode == state.ModePaused:
		return "The run stays paused: /baton run gives it back to baton."
	case mode == state.ModeComplete:
		return "Every phase left is done, so the plan is complete: sum up for the human and end your turn."
	case took != "":
		return fmt.Sprintf("No phase was in hand, and %s can start: end your turn now, so baton compacts this conversation, then %s starts with a fresh brief.", took, took)
	case boundary:
		return fmt.Sprintf("End your turn now: baton compacts the context, then %s starts with a fresh brief.", current)
	case inHand:
		return fmt.Sprintf("Carry on with %s as the plan describes it.", current)
	}
	return "No phase can start yet: end your turn, and baton wakes you when there is something to do."
}

// ReplanHoldsAgents is why no new phase agent starts while a replan is open.
const ReplanHoldsAgents = "a replan of the phases not started is open"

// NoReplanOpen turns down `baton replanned` when no replan is open: none was opened, it was finished or
// declined already, or it lapsed.
const NoReplanOpen = "no replan is open (it was finished or declined already, or it lapsed): the plan goes on as it is. Run baton replan to open one."

// BreachUnsaid is a phase the draft drops that the model's notes do not name (package replan).
const BreachUnsaid = "unsaid"

// ReplanRefused tells the model its draft was not followed, and exactly why: each breach. The plan file is
// untouched and the replan stays open, for it to fix the draft (draft) and run baton replanned again.
func ReplanRefused(bs []plan.Breach, draft string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "not replanned — the draft changes what this replan may not. The plan file is untouched, and the replan stays open: fix the draft (%s) and run baton replanned again, "+
		"or baton replanned --none \"<why>\" to leave the plan as it was.", draft)
	for _, b := range bs {
		s.WriteString("\n- " + breachLine(b))
	}
	return s.String()
}

// breachLine says one breach.
func breachLine(b plan.Breach) string {
	switch b.Kind {
	case plan.BreachBuild:
		return "the draft does not build as a plan:\n    " + strings.ReplaceAll(strings.TrimSpace(b.Detail), "\n", "\n    ")
	case plan.BreachGone:
		return b.Phase + " has started or is done, and the draft has no such phase: keep it, its heading and its text as they were"
	case plan.BreachText:
		return b.Phase + " has started or is done: its text must stay byte for byte as it was"
	case plan.BreachDeps:
		return fmt.Sprintf("%s has started or is done: it must wait for the phases it waited for (%s), not %s (keep the phases before it in their places, or give it an After: line)",
			b.Phase, idsOrNone(b.Was), idsOrNone(b.Now))
	case plan.BreachGoal:
		return "the " + goalPart(b.Phase) + " changed: it stays byte for byte unless the human's words ask for that"
	case plan.BreachDropped:
		return b.Phase + " is gone: this replan keeps every phase not started (only the human's words can drop or merge one)"
	case plan.BreachAdded:
		return b.Phase + " is new: this replan may not add phases"
	case plan.BreachEdited:
		return b.Phase + "'s text changed: this replan may change only its After: line and its place"
	case plan.BreachQA:
		return b.Phase + " lost its `Type: qa` line: a QA phase stays one"
	case BreachUnsaid:
		return b.Phase + " is dropped, and your --notes do not name it: say in them which phases you drop or merge away, and why"
	}
	return b.Kind + " " + b.Phase
}

// goalPart names a goal section of a plan (plan.GoalPreamble, …).
func goalPart(part string) string {
	switch part {
	case plan.GoalPreamble:
		return "title or the text before the first phase"
	case plan.GoalRules:
		return "standing rules section"
	case plan.GoalEnd:
		return "end section"
	}
	return part
}

func idsOrNone(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return andList(ids)
}

// ReplanChanges says what a replan changed (plan.Compare), in one line: "P5b added (after P4); P7 now after
// P2 (was after P6); P3 reworded; P8 moved; P6 dropped; the standing rules section changed", or "nothing".
func ReplanChanges(cs []plan.Change) string {
	var parts []string
	for _, c := range cs {
		switch c.Kind {
		case plan.ChangeAdded:
			parts = append(parts, fmt.Sprintf("%s added (%s)", c.Phase, waits(c.Now)))
		case plan.ChangeDropped:
			parts = append(parts, c.Phase+" dropped")
		case plan.ChangeAfter:
			parts = append(parts, fmt.Sprintf("%s now %s (was %s)", c.Phase, waits(c.Now), waits(c.Was)))
		case plan.ChangeText:
			parts = append(parts, c.Phase+" reworded")
		case plan.ChangeMoved:
			parts = append(parts, c.Phase+" moved")
		case plan.ChangeGoal:
			parts = append(parts, "the "+goalPart(c.Phase)+" changed")
		}
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, "; ")
}

// waits says what a phase waits for: "after P2 and P4", or "needing no other phase".
func waits(ids []string) string {
	if len(ids) == 0 {
		return "needing no other phase"
	}
	return "after " + andList(ids)
}

// ReplanWhat is the decision a followed replan is recorded as: who asked, what changed, and why (the
// model's notes).
func ReplanWhat(by string, cs []plan.Change, notes string) string {
	who := "at the human's /baton replan"
	if by != state.ReplanHuman {
		who = "as " + replanBy(by)
	}
	what := fmt.Sprintf("baton replanned the phases not started, %s: %s", who, ReplanChanges(cs))
	if notes = clause(Sanitize(notes)); notes != "" {
		what += " — why: " + notes
	}
	return what
}

// ReplanUndo is how to undo a followed replan: copy the text it found (kept) back over the plan file;
// baton follows it at the next stop, as long as every phase that started since is in it.
func ReplanUndo(kept, file string) string {
	return fmt.Sprintf("copy %s back over %s; baton follows it at the next stop, as long as every phase started since is in it", kept, file)
}

// ShapeShift says how the shape of the phases not started moved with a replan: "6 phases · longest chain
// 5 → 3 · up to 1 → 3 at once".
func ShapeShift(before, after plan.Shape) string {
	count := fmt.Sprintf("%d phases", after.Phases)
	if before.Phases != after.Phases {
		count = fmt.Sprintf("%d → %d phases", before.Phases, after.Phases)
	}
	return fmt.Sprintf("%s · longest chain %d → %d · up to %d → %d at once", count, len(before.Chain), len(after.Chain), before.Width, after.Width)
}

// Replanned tells the human baton followed a replan: what changed, the shape of the phases not started
// before and after, and the decision (id) that records it with its undo.
func Replanned(cs []plan.Change, before, after plan.Shape, id string) string {
	return fmt.Sprintf("baton: replanned → %s · not started: %s · recorded as %s, with its undo (/baton status)", ReplanChanges(cs), ShapeShift(before, after), id)
}

// ReplanNotice is the notice sent out of the session for a followed replan of the plan of title.
func ReplanNotice(title string, cs []plan.Change) string {
	return fmt.Sprintf("%s: replanned — %s", title, ReplanChanges(cs))
}

// ReplannedTell tells the model its draft is the plan now: what changed, the decision (id), and what to do
// next (next, from the caller: carry on with the phase in hand, end the turn for a compaction, …).
func ReplannedTell(cs []plan.Change, id, next string) string {
	return fmt.Sprintf("baton: replanned (%s): %s. The plan file now reads as your draft, and baton follows it. %s", id, ReplanChanges(cs), next)
}

// ReplanDeclined tells the human (say), and the model (tell), that a replan closed with the plan
// unchanged: for the model's reason (why), or since its draft was the plan as it was (unchanged).
func ReplanDeclined(why string, unchanged bool) (say, tell string) {
	if unchanged {
		return "baton: replan closed — Claude's draft changed nothing, so the plan goes on as it was",
			"baton: the draft is the plan as it was, so nothing changed: the replan is closed, and the plan goes on as it is."
	}
	return "baton: replan closed, the plan unchanged — " + clause(Sanitize(why)),
		"baton: the replan is closed, and the plan goes on as it is."
}

// ReplanLapsed tells the human a replan was not finished within max: the draft is dropped, and the plan
// goes on as it was.
func ReplanLapsed(max time.Duration) string {
	return fmt.Sprintf("baton: the replan was not finished within %s → its draft is dropped, and the plan goes on as it was", Spell(max))
}

// ReplanStale turns down a draft made while the plan file itself changed: the replan is closed, and the
// model may open another from the file as it is now.
const ReplanStale = "not replanned — the plan file changed while the replan was open, so the draft starts from an older text. " +
	"The replan is closed and the plan file left as it is: run baton replan again to start from the file as it is now."

// ReplanCannotOpen turns down a replan over a plan file baton cannot follow (why).
func ReplanCannotOpen(file, why string) string {
	return fmt.Sprintf("no replan opened — the plan file (%s) has changes baton cannot follow (%s). Fix the file first, then replan.", file, why)
}

// ReplanRestored tells the human baton followed a plan file copied back from a replan's kept text (n:
// replan/<n>.md; 0: the plan as they approved it), and what that changed.
func ReplanRestored(n int, cs []plan.Change) string {
	what := fmt.Sprintf("the text replan %d started from", n)
	if n == 0 {
		what = "the plan as you approved it"
	}
	return fmt.Sprintf("baton: the plan file is back to %s → baton follows it: %s", what, ReplanChanges(cs))
}

// ReplanLine is a followed replan as baton status lists it: its number, when, who asked, what it changed,
// and what the plan after it changed of the approved one.
func ReplanLine(r state.Replanned) string {
	line := fmt.Sprintf("%d  %s  %s: %s", r.N, Stamp(r.At), replanBy(r.By), ReplanChanges(r.Changed))
	if r.Decision != "" {
		line += " (" + r.Decision + ")"
	}
	if !r.Undone.IsZero() {
		line += ", undone " + Stamp(r.Undone)
	}
	return line + "\n       against the approved plan: " + ReplanChanges(r.Approved)
}

// ReplanOpenLine is an open replan as baton status shows it: who opened it, since when, and when it lapses
// (after max).
func ReplanOpenLine(r state.Replan, max time.Duration) string {
	return fmt.Sprintf("replan: open since %s (%s); no new phase agent starts until it ends, at %s at the latest",
		r.Since.Local().Format("15:04"), replanBy(r.By), r.Since.Add(max).Local().Format("15:04"))
}
