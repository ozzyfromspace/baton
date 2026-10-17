package decide

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// What baton says about a plan's gates: the rules, once per plan at its first ExitPlanMode (GateCheck,
// beside CompetingDirectives and ShapeCheck), and in short in every phase's brief (GateHabits, beside
// QAFocus and QAPhase). Two real runs spent most of their hours in checks that could not change what
// shipped: full suites rerun after a test-only fix, a gate run on long after its first failure, a QA
// phase whose second and third passes reran what had passed. A human who feels the time would not.

// GateRules are baton's rules for a plan's gates: when a phase checks its work, how, and where the full
// checks run. The skill's planning guidance (internal/skill/skill.md) says the same in short.
const GateRules = "Test the way a human who feels time does: scoped checks while you work, and the big one once, at the end. " +
	"Every minute a check runs that could not change what ships is the human's, spent for nothing.\n" +
	"1. QA phases sit at real seams, never per phase: at the end, and, in a long plan, at the end of a group of related phases that later work builds on. " +
	"Each comes `After:` everything it checks. A short plan usually needs one.\n" +
	"2. Every other phase runs scoped checks that finish in seconds or minutes: the tests of what it changed, by name. " +
	"Its \"done when\" is a commit, never a green full suite.\n" +
	"3. Gate scripts are committed in the repo, built from `baton stage <name> -- <command>` steps, and stream every stage.\n" +
	"4. A gate stops at its first failure. After a red, `baton verify --failed` reruns only what failed: the full suite never runs again for it.\n" +
	"5. A setup phase proves the lane recipe (`.baton-lane`) before phases run in parallel.\n" +
	"6. Timings on a shared machine are warnings; bytes and counts gate.\n" +
	"7. No file many phases edit: records per phase, or the plan's `Record:` file.\n" +
	"8. Long checks go through `baton verify` or `baton await`, never a background shell.\n" +
	"9. Push to CI early in the QA phase, batching fixes per round trip."

// GateCheck is the part of baton's one turn-back at a plan's first ExitPlanMode for every plan, beside
// CompetingDirectives and, for a plan that is mostly a chain, ShapeCheck: the gate rules, the plan's QA
// phases, each with how many phases it covers since the one before, and the request to confirm that each
// sits at a real seam, moving or merging any that does not. Where the seams are is the model's judgment,
// never a number baton counts. The QA phases before the last are flagged (ExtraQA): each costs the human
// a round of full checks mid-plan. pl is the plan as baton reads it, or the zero Plan for one that does
// not build, which gets the rules alone.
func GateCheck(pl plan.Plan) string {
	var b strings.Builder
	b.WriteString("[baton] Check this plan's gates too: every check its phases run, and where its QA phases sit. baton's gate rules:\n\n")
	b.WriteString(GateRules + "\n\n")
	b.WriteString(qaSeams(pl))
	b.WriteString("Where a phase's checks or its \"done when\" break a rule, change the plan: in that phase's steps, or in the Standing rules for every phase. " +
		"Where every gate holds, leave it as it is. baton asks this once per plan too.")
	return b.String()
}

// ExtraQA lists the plan's QA phases before its last one: each a round of full checks mid-plan, which
// only a seam later work builds on is worth.
func ExtraQA(pl plan.Plan) []string {
	var qa []string
	for _, ph := range pl.Phases {
		if ph.QA() {
			qa = append(qa, ph.ID)
		}
	}
	if len(qa) < 2 {
		return nil
	}
	return qa[:len(qa)-1]
}

// qaSeams is GateCheck's account of where the plan's QA phases sit: each with the phases it covers since
// the one before (plan.SinceQA) and any of those it is not `After:`, and what to confirm of them; for a
// plan with none, where its full checks would go. A plan baton could not read gets nothing.
func qaSeams(pl plan.Plan) string {
	if len(pl.Phases) == 0 {
		return ""
	}
	var b strings.Builder
	prev, n := "the plan's start", 0
	for _, ph := range pl.Phases {
		if !ph.QA() {
			continue
		}
		if n++; n == 1 {
			b.WriteString("Its QA phases, each with the phases it covers since the one before:\n")
		}
		since := pl.SinceQA(ph.ID)
		fmt.Fprintf(&b, "- %s (%s): %s since %s", ph.ID, ph.Title, phaseCount(len(since)), prev)
		if missed := notAfter(pl, ph.ID, since); len(missed) > 0 {
			fmt.Fprintf(&b, "; it is not `After:` %s, which it checks", andList(missed))
		}
		b.WriteString("\n")
		prev = ph.ID
	}
	if n == 0 {
		return "This plan has no QA phase (a line `Type: qa` under a phase's heading), so each phase checks itself in full as it ends. " +
			"A short plan may need none; otherwise add one at the end, `After:` everything it checks, and keep every other phase's checks scoped.\n\n"
	}
	b.WriteString("\nConfirm that each sits at a real seam: the end of the plan, or the end of a group of related phases that later work builds on, " +
		"`After:` everything it checks.")
	switch extra := ExtraQA(pl); len(extra) {
	case 0:
	case 1:
		fmt.Fprintf(&b, " %s comes before the last QA phase: a round of full checks mid-plan costs the human time, worth it only where such a group ends.", extra[0])
	default:
		fmt.Fprintf(&b, " %s come before the last QA phase: each is a round of full checks mid-plan that costs the human time, worth it only where such a group ends.", andList(extra))
	}
	b.WriteString(" Move any that does not sit at a seam to one, or merge it into the next QA phase: that is your judgment, not a number.\n\n")
	return b.String()
}

// notAfter lists the phases of since that QA phase id does not wait for, directly or through another.
func notAfter(pl plan.Plan, id string, since []string) []string {
	before := pl.Ancestors(id)
	var out []string
	for _, s := range since {
		if !slices.Contains(before, s) {
			out = append(out, s)
		}
	}
	return out
}

// phaseCount is "no phase", "1 phase" or "4 phases".
func phaseCount(n int) string {
	if n == 0 {
		return "no phase"
	}
	return fmt.Sprintf("%d %s", n, plural(n, "phase", "phases"))
}

// GateHabits is the gate rules in short, for every phase's brief, after QAFocus or QAPhase where the plan
// has a QA phase: a gate stops at its first failure and only what failed runs again; a long gate is a
// committed script of baton stages, run through baton verify; and a gate's environment is recorded where
// every phase sees it. Without git (git false) there is no lane, so no verify and no stages: long checks
// go through baton await.
func GateHabits(git bool) string {
	env := "When a gate fails on its environment rather than the change (a port in use, a daemon down), " +
		"record what it needs with `baton env \"<what it needs>\"`, so every phase sees it."
	if !git {
		return "Gates: a check stops at its first failure; then rerun only what failed, never the whole suite again. " +
			"Long checks go through `baton await`, never a background shell. " + env
	}
	return "Gates: a gate stops at its first failure. When one fails, stop it (`baton stop <job>`) unless every failure is worth the wait, " +
		"then rerun only what failed (`baton verify --failed`), never the whole suite again. " +
		"A long gate is a script committed in the repo, built from `baton stage <name> -- <command>` steps so it streams every stage, " +
		"and runs through `baton verify`, never a background shell. " + env
}
