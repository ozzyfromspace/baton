package decide

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The risk ledger (package risk, loop/risk.go): each phase's points, from facts baton has of it, add up
// until a full check passes. Crossing a line hands over to an assessment; with none, the lead is told the
// score and its factors, and nothing is held. Only an assessment's "full now" verdict holds new phases, and
// only until the lead queues the full check, or notes why not, or escalation_timeout passes.

// DoneCall is how a phase reports itself done: what later phases need to know, and how it departed from
// the plan, which the risk ledger reads (baton done --deviation).
const DoneCall = "baton done <phase> --notes \"<what later phases need to know>\" --deviation \"<how it departed from the plan, or none>\""

// DeviationHelp is baton done's help for --deviation.
const DeviationHelp = "--deviation \"<how the phase departed from the plan, or none>\""

// RiskLines is the detail of the lines a phase changed.
func RiskLines(n int) string { return fmt.Sprintf("%d %s changed", n, plural(n, "line", "lines")) }

// RiskAreas is the detail of the areas of the project a phase touched.
func RiskAreas(areas []string) string {
	return fmt.Sprintf("%d %s: %s", len(areas), plural(len(areas), "area", "areas"), firstFew(areas, 4))
}

// RiskShared is the detail of the files a phase changed that other phases' sections name.
func RiskShared(paths []string) string {
	return fmt.Sprintf("%d %s other phases name: %s", len(paths), plural(len(paths), "file", "files"), firstFew(paths, 3))
}

// RiskFiles is the detail of the files behind a factor (manifests, migrations, tests deleted).
func RiskFiles(paths []string) string { return firstFew(paths, 3) }

// RiskUntested is the detail of code changed with no test changed.
func RiskUntested(lines int) string {
	return fmt.Sprintf("%d %s of code, no test changed", lines, plural(lines, "line", "lines"))
}

// RiskCount is the detail of a factor counted (decisions, landings returned).
func RiskCount(n int, one, many string) string { return fmt.Sprintf("%d %s", n, plural(n, one, many)) }

// RiskDeviationUnknown is the detail of a phase that did not say how it departed from the plan.
const RiskDeviationUnknown = "no --deviation given"

// RiskDeviation is the detail of a phase's own words about how it departed from the plan.
func RiskDeviation(d string) string { return clipText(d, 120) }

// RiskFactors is a phase's factors in a line: "lines 3 (612 lines changed), no check 4".
func RiskFactors(factors []state.RiskFactor) string {
	var parts []string
	for _, f := range factors {
		s := fmt.Sprintf("%s %d", strings.ReplaceAll(f.Name, "_", " "), f.Points)
		if f.Detail != "" {
			s += " (" + f.Detail + ")"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// riskPhases is the phases scored since the last full check, those with the most points first, at most
// most of them: "P3 14 points: lines 3 (…), …".
func riskPhases(phases []state.PhaseRisk, most int) string {
	sorted := slices.Clone(phases)
	slices.SortStableFunc(sorted, func(a, b state.PhaseRisk) int { return b.Points - a.Points })
	var lines []string
	for i, p := range sorted {
		if i == most {
			lines = append(lines, fmt.Sprintf("  and %d more", len(sorted)-most))
			break
		}
		lines = append(lines, fmt.Sprintf("  · %s, %d %s: %s", p.Phase, p.Points, plural(p.Points, "point", "points"), RiskFactors(p.Factors)))
	}
	return strings.Join(lines, "\n")
}

// riskSince says since when the ledger counts: the last full check, or the start of the plan.
func riskSince(r state.Risk) string {
	if r.Since.IsZero() {
		return "since the plan started, with no full check passed yet"
	}
	return "since the last full check passed (" + r.Since.Local().Format("15:04") + ")"
}

// lineName is a line as the words say it.
func lineName(line string) string {
	if line == state.RiskFull {
		return "hard line"
	}
	return "review line"
}

// RiskLookIn is the lead's look-in when the ledger crosses a line (line, at points) and no assessment
// answers: the score and its factors, for information. Nothing is held.
func RiskLookIn(r state.Risk, line string, points int) string {
	return fmt.Sprintf("[baton] Risk look-in: the work %s scores %d points, past the risk ledger's %s (%d). By phase:\n%s\n"+
		"This is information, and nothing is held. If building further on unchecked work worries you, queue the full check now: "+
		"`baton verify --qa \"<why now>\" --notes \"<where the phase stands>\" -- <the full check>`, in a lane rather than --here. Otherwise carry on.",
		riskSince(r), r.Total, lineName(line), points, riskPhases(r.Phases, 6))
}

// RiskCrossedSay tells the human the ledger crossed a line (line, at points), and that the lead is told.
func RiskCrossedSay(total int, line string, points int) string {
	return fmt.Sprintf("baton: risk ledger at %d points since the last full check, past its %s (%d) → the lead is told the score and its factors", total, lineName(line), points)
}

// RiskHoldsAgents is why no new phase agent starts while new phases are held for a full check.
const RiskHoldsAgents = "new phases are held for a full check, until the lead queues it or notes why not (risk ledger)"

// RiskHeld is the lead's message while new phases are held for a full check: why, how to lift it, the
// landings a check in the checkout (--here) would hold (landing: waiting to land; working: agents at work,
// whose work would wait too), and when baton lifts it itself (deadline).
func RiskHeld(r state.Risk, landing, working []string, deadline time.Time) string {
	why := "the risk assessment says a full check is due now"
	if r.Verdict != state.VerdictFull {
		why = "a full check is due now"
	}
	here := "A check --here would hold every landing while it runs."
	switch {
	case len(landing) > 0 && len(working) > 0:
		here = fmt.Sprintf("A check --here would hold the landings of %s, which %s to land, and of %s, whose %s at work.",
			andList(landing), plural(len(landing), "waits", "wait"), andList(working), plural(len(working), "agent is", "agents are"))
	case len(landing) > 0:
		here = fmt.Sprintf("A check --here would hold the landings of %s, which %s to land.", andList(landing), plural(len(landing), "waits", "wait"))
	case len(working) > 0:
		here = fmt.Sprintf("A check --here would hold the landings of %s, whose %s at work.", andList(working), plural(len(working), "agent is", "agents are"))
	}
	return fmt.Sprintf("[baton] New phases are held for a full check: %s, with the work %s at %d points. By phase:\n%s\n"+
		"Before you go on, queue the full check: `baton verify --qa \"<why now>\" --notes \"<where the phase stands>\" -- <the full check>`, "+
		"in a lane rather than --here where the project allows. %s "+
		"If a full check now is wrong, say why instead: `baton note \"<why not now>\"`. Either lifts the hold, and work goes on beside the check; "+
		"baton lifts it itself at %s.",
		why, riskSince(r), r.Total, riskPhases(r.Phases, 6), here, deadline.Local().Format("15:04"))
}

// RiskHeldSay tells the human new phases are held for a full check, until deadline at the latest.
func RiskHeldSay(total int, deadline time.Time) string {
	return fmt.Sprintf("baton: risk ledger at %d points, and the assessment says a full check now → new phases are held until the lead queues it or notes why not (at %s at the latest)",
		total, deadline.Local().Format("15:04"))
}

// Ways a hold on new phases is lifted (risk_released's by).
const (
	RiskByVerify   = "verify"   // the lead queued the full check
	RiskByNote     = "note"     // the lead noted why not
	RiskByDeadline = "deadline" // escalation_timeout passed
	RiskByCleared  = "cleared"  // a full check passed meanwhile
)

// RiskReleasedSay tells the human new phases may start again, and why (by, with what: a job's id, a
// decision's).
func RiskReleasedSay(by, what string) string {
	switch by {
	case RiskByVerify:
		return fmt.Sprintf("baton: the full check is queued (job %s) → new phases may start again, beside it", what)
	case RiskByNote:
		return fmt.Sprintf("baton: the lead noted why not a full check now (%s) → new phases may start again", what)
	case RiskByCleared:
		return "baton: a full check passed → new phases may start again"
	}
	return "baton: nobody queued the full check by the deadline → new phases may start again"
}

// RiskLine is the ledger as baton status and the board show it.
func RiskLine(total int, held bool) string {
	line := fmt.Sprintf("risk: %d %s since the last full check", total, plural(total, "point", "points"))
	if held {
		line += " · new phases held for a full check"
	}
	return line
}

// firstFew lists items, the first n of them and how many more.
func firstFew(items []string, n int) string {
	if len(items) > n {
		return strings.Join(items[:n], ", ") + fmt.Sprintf(" and %d more", len(items)-n)
	}
	return strings.Join(items, ", ")
}

// clipText is s on one line, cut to n characters.
func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
