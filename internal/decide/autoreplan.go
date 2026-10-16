package decide

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says about the replans it opens by itself (package replan's Auto), at the lead's phase
// boundary: after an upgrade whose planning rules changed (state.ReplanUpgrade), at the start of a QA
// phase over a remainder that is mostly one chain (state.ReplanQA), and once a phase marked `Replan: after`
// is done (state.ReplanMarked). The model replans before it begins the phase whose brief follows.

// structural reports a kind of replan that may change only the structure of the phases not started, and
// is kept only if their shape improves: baton's own at an upgrade or a QA phase.
func structural(by string) bool { return by == state.ReplanUpgrade || by == state.ReplanQA }

// cause says why baton opened a replan by itself, for its brief.
func (b ReplanBrief) cause() string {
	switch b.By {
	case state.ReplanUpgrade:
		return "Why now: this plan was made under older planning rules than those of the baton running it now, the shape rules below. " +
			"baton has the phases not started checked against them once."
	case state.ReplanQA:
		return fmt.Sprintf("Why now: %s, a QA phase, starts, and the phases not started are mostly one chain. "+
			"Check each link of that chain against the shape rules below: a phase waits for another only for something that phase makes.", phaseNames(b.Cue))
	case state.ReplanMarked:
		notes := "in their notes"
		if len(b.Cue) == 1 {
			notes = "in its notes"
		}
		if b.Notes != "" {
			notes += " (" + b.Notes + ")"
		}
		return fmt.Sprintf("Why now: %s %s done, and the plan marks %s `Replan: after`: what %s found should reshape the phases not started. "+
			"What %s found is %s.", phaseNames(b.Cue), plural(len(b.Cue), "is", "are"), plural(len(b.Cue), "it", "them"),
			plural(len(b.Cue), "it", "they"), plural(len(b.Cue), "it", "they"), notes)
	}
	return "Why now: " + replanBy(b.By) + "."
}

// phaseNames names phases as "P3 (Spike)", "P3 (Spike) and P5 (Probe)".
func phaseNames(ps []ReplanPhase) string {
	var names []string
	for _, ph := range ps {
		if ph.Title == "" {
			names = append(names, ph.ID)
			continue
		}
		names = append(names, fmt.Sprintf("%s (%s)", ph.ID, ph.Title))
	}
	return andList(names)
}

// phaseIDs names phases by their ids: "P3", "P3 and P5".
func phaseIDs(ps []ReplanPhase) string {
	var ids []string
	for _, ph := range ps {
		ids = append(ids, ph.ID)
	}
	return andList(ids)
}

// AutoReplanOpened tells the human baton opened a replan by itself (by), for the phases that cued it, and
// holds new phase agents while it is open, for at most max.
func AutoReplanOpened(by string, cue []string, max time.Duration) string {
	why := replanBy(by)
	switch {
	case by == state.ReplanQA && len(cue) > 0:
		why = andList(cue) + ", a QA phase, starts over a chain of phases not started"
	case by == state.ReplanMarked && len(cue) > 0:
		why = andList(cue) + " " + plural(len(cue), "is", "are") + " done, marked Replan: after"
	}
	return fmt.Sprintf("baton: replanning the phases not started (%s) → Claude does it before the next phase; no new phase agent starts until it is done, %s at most", why, Spell(max))
}

// ReplanUnimproved tells the human (say), and the model (tell), that a replan of baton's own was not kept:
// its draft left the shape of the phases not started no better (before, after), so the plan stays as it
// was, and the replan is closed.
func ReplanUnimproved(before, after plan.Shape) (say, tell string) {
	shift := ShapeShift(before, after)
	return "baton: replan closed, the plan unchanged — Claude's draft left the phases not started no better (" + shift + ")",
		"baton: not replanned — the draft leaves the phases not started no better (" + shift + "), and baton keeps a replan of its own only if it shortens the longest chain " +
			"or lets more phases run at once. The plan file stays as it was, and the replan is closed."
}

// ReplanDueHoldsAgents is why no new phase agent starts while the lead's phase boundary is under way with a
// replan of baton's own due at it: the replan restructures the phases not started, and an agent would take
// one first.
const ReplanDueHoldsAgents = "a replan of the phases not started is due at the lead's phase boundary"

// CompactedReplan is the line under the brief of a compaction at a phase boundary where baton opened a
// replan (its brief comes first): replan, then begin the phase (title).
func CompactedReplan(title string, resumed bool) string {
	verb := "begin"
	if resumed {
		verb = "resume"
	}
	return fmt.Sprintf("[baton] Context compacted. Replan first, as the brief above says (finish with baton replanned), then %s %s — its brief above has your instructions.", verb, title)
}
