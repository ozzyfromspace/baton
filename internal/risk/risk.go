// Package risk is the risk ledger's arithmetic: the factors that make a phase's work risky to build on
// before a full check has passed, their points, and the lines a run's total crosses. Scoped testing trades
// early detection for speed; the ledger says when that trade stops paying.
//
// A phase is scored once, as it is done (a lead phase) or lands (a phase agent's), from facts baton already
// has, never from the phase's own rating of itself, since a model finishing a phase wants to be done:
//
//   - its commits (gitx.Stats): the lines changed; the areas of the project touched; files other phases'
//     sections name (plan.Paths); manifests, lockfiles and migrations; code changed with no test changed, or
//     tests deleted;
//   - its record: finished over a red verification (--despite-red); the decisions it made; its landings that
//     came back for conflicts; whether it ran any check at all (lane.IsCheck on its shell commands, and its
//     jobs); and how it departed from the plan, in its own words (baton done --deviation), unknown when it
//     did not say.
//
// The loop adds each phase's points to the run's ledger (state.Risk), which a passing full check resets, and
// hands over to an assessment when the total crosses a line. A score alone never holds anything.
package risk

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Facts are what baton knows of a phase as it scores it.
type Facts struct {
	Phase string
	// Files are what the phase's own commits changed, path by path; GitKnown is false when git could not say
	// (no git, no commit to start from), and the git factors are left out.
	Files    []gitx.FileStat
	GitKnown bool
	// Named are the paths other phases' sections name (plan.Paths).
	Named []string
	// The record. Checks are the check commands its shell ran, and Jobs the jobs it queued (verify, await);
	// ChecksKnown is false for a record from before baton counted checks (a replay of an older run), and the
	// no-check factor is left out.
	DespiteRed  bool
	Decisions   int
	Conflicts   int
	Checks      int
	Jobs        int
	ChecksKnown bool
	Deviation   string // as the phase gave it (baton done --deviation); "" when it did not say
}

// Weights are the points each factor is worth, and their caps.
type Weights struct {
	LinesPer, LinesMost        int // a point for each LinesPer lines changed (lockfiles and generated files aside), at most LinesMost
	AreasFree, AreasMost       int // a point for each area touched past AreasFree, at most AreasMost
	SharedEach, SharedMost     int // for each changed file other phases' sections name
	Manifests, Migrations      int // once, for any manifest or lockfile, and for any migration
	Untested, UntestedLines    int // code changed by at least UntestedLines lines with no test file changed
	TestsDeleted               int // once, for any test file deleted
	DespiteRed                 int
	DecisionEach, DecisionMost int
	ConflictEach, ConflictMost int
	NoCheck                    int // no check command run and no job queued
	DeviationUnknown, Deviated int // no --deviation given; a departure from the plan
}

// DefaultWeights are the points baton scores with, calibrated by a replay of two real runs (replay_test.go):
// the lines changed and the files other phases name told apart best the phases whose files the next full
// check had to fix (6 and 4 points on average, against 4 and 2 for the rest), and nearly every phase
// touches two or three areas, so only those past three count.
var DefaultWeights = Weights{
	LinesPer: 200, LinesMost: 6,
	AreasFree: 3, AreasMost: 4,
	SharedEach: 1, SharedMost: 4,
	Manifests: 2, Migrations: 3,
	Untested: 3, UntestedLines: 30,
	TestsDeleted: 3,
	DespiteRed:   5,
	DecisionEach: 1, DecisionMost: 3,
	ConflictEach: 2, ConflictMost: 6,
	NoCheck:          4,
	DeviationUnknown: 1, Deviated: 2,
}

// Factor names, as risk_scored logs them.
const (
	FactorLines        = "lines"
	FactorAreas        = "areas"
	FactorShared       = "shared"
	FactorManifests    = "manifests"
	FactorMigrations   = "migrations"
	FactorUntested     = "untested"
	FactorTestsDeleted = "tests_deleted"
	FactorDespiteRed   = "despite_red"
	FactorDecisions    = "decisions"
	FactorConflicts    = "conflicts"
	FactorNoCheck      = "no_check"
	FactorDeviation    = "deviation"
)

// Score is the factors of a phase with these facts that add to its score, worth w's points, in the order
// above. A factor that adds nothing is left out.
func Score(f Facts, w Weights) []state.RiskFactor {
	var out []state.RiskFactor
	add := func(name string, points int, detail string) {
		if points > 0 {
			out = append(out, state.RiskFactor{Name: name, Points: points, Detail: detail})
		}
	}
	if f.GitKnown {
		var lines, codeLines int
		var areas, shared, manifests, migrations, tests, deleted []string
		for _, fs := range f.Files {
			p := fs.Path
			n := fs.Added + fs.Deleted
			if !IsLockfile(p) && !Generated(p) {
				lines += n
			}
			if a := Area(p); !slices.Contains(areas, a) {
				areas = append(areas, a)
			}
			if slices.ContainsFunc(f.Named, func(named string) bool { return Names(named, p) }) {
				shared = append(shared, p)
			}
			switch {
			case IsManifest(p):
				manifests = append(manifests, p)
			case IsMigration(p):
				migrations = append(migrations, p)
			case IsTest(p):
				tests = append(tests, p)
				if fs.Removed {
					deleted = append(deleted, p)
				}
			case IsCode(p):
				codeLines += n
			}
		}
		add(FactorLines, capped(lines/max(w.LinesPer, 1), w.LinesMost), decide.RiskLines(lines))
		add(FactorAreas, capped(len(areas)-w.AreasFree, w.AreasMost), decide.RiskAreas(areas))
		add(FactorShared, capped(len(shared)*w.SharedEach, w.SharedMost), decide.RiskShared(shared))
		if len(manifests) > 0 {
			add(FactorManifests, w.Manifests, decide.RiskFiles(manifests))
		}
		if len(migrations) > 0 {
			add(FactorMigrations, w.Migrations, decide.RiskFiles(migrations))
		}
		if codeLines >= max(w.UntestedLines, 1) && len(tests) == 0 {
			add(FactorUntested, w.Untested, decide.RiskUntested(codeLines))
		}
		if len(deleted) > 0 {
			add(FactorTestsDeleted, w.TestsDeleted, decide.RiskFiles(deleted))
		}
	}
	if f.DespiteRed {
		add(FactorDespiteRed, w.DespiteRed, "")
	}
	add(FactorDecisions, capped(f.Decisions*w.DecisionEach, w.DecisionMost), decide.RiskCount(f.Decisions, "decision", "decisions"))
	add(FactorConflicts, capped(f.Conflicts*w.ConflictEach, w.ConflictMost), decide.RiskCount(f.Conflicts, "landing returned", "landings returned"))
	if f.ChecksKnown && f.Checks == 0 && f.Jobs == 0 {
		add(FactorNoCheck, w.NoCheck, "")
	}
	switch d := strings.TrimSpace(f.Deviation); {
	case d == "":
		add(FactorDeviation, w.DeviationUnknown, decide.RiskDeviationUnknown)
	case !NoDeviation(d):
		add(FactorDeviation, w.Deviated, decide.RiskDeviation(d))
	}
	return out
}

// Points is the sum of factors' points.
func Points(factors []state.RiskFactor) int {
	n := 0
	for _, f := range factors {
		n += f.Points
	}
	return n
}

// capped is n, at least 0 and at most most.
func capped(n, most int) int { return min(max(n, 0), most) }

// NoDeviation reports a deviation that says the phase followed the plan: "none", "no", "n/a", or one of
// those followed by more words ("none — as planned").
func NoDeviation(d string) bool {
	d = strings.ToLower(strings.TrimSpace(d))
	for _, w := range []string{"none", "no deviation", "no deviations", "n/a", "na", "no", "nothing", "-"} {
		if rest, ok := strings.CutPrefix(d, w); ok && (rest == "" || !unicode.IsLetter([]rune(rest)[0])) {
			return true
		}
	}
	return false
}

// Lines are the plan's lines for its ledger (config.Risk: risk, risk_review and risk_full, calibrated by a
// replay of two real runs, replay_test.go): crossing either hands over to an assessment by Model
// (risk_model; "" for none: the lead is told the score alone), whose answer is waited for Wait at most
// (risk_wait; 0 for loop.RiskWait), and On false turns the ledger off.
type Lines struct {
	On     bool
	Review int
	Full   int
	Model  string
	Wait   time.Duration
}

// Crossed is the line a ledger's total crosses, given the highest it crossed since its last reset (prev:
// state.RiskReview, RiskFull, or ""): RiskFull at the hard line once, RiskReview at the review line once,
// "" for none.
func (l Lines) Crossed(prev string, total int) string {
	switch {
	case !l.On:
		return ""
	case l.Full > 0 && total >= l.Full && prev != state.RiskFull:
		return state.RiskFull
	case l.Review > 0 && total >= l.Review && prev == "":
		return state.RiskReview
	}
	return ""
}

// Line is the points of the named line.
func (l Lines) Line(name string) int {
	if name == state.RiskFull {
		return l.Full
	}
	return l.Review
}

// String is the lines as baton setup and the logs show them.
func (l Lines) String() string {
	if !l.On {
		return "off"
	}
	return fmt.Sprintf("review at %d points, full at %d", l.Review, l.Full)
}
