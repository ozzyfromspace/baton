package risk

import (
	"reflect"
	"testing"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The factor table: each fact adds its points, capped, and a factor that adds nothing is left out.
func TestScoreFactors(t *testing.T) {
	w := DefaultWeights
	clean := Facts{GitKnown: true, ChecksKnown: true, Checks: 1, Deviation: "none",
		Files: []gitx.FileStat{{Path: "internal/a/a.go", Added: 40}, {Path: "internal/a/a_test.go", Added: 20}}}
	with := func(fn func(*Facts)) Facts {
		f := clean
		f.Files = append([]gitx.FileStat(nil), clean.Files...)
		fn(&f)
		return f
	}
	for _, c := range []struct {
		name string
		f    Facts
		want map[string]int
	}{
		{"a small tested change that ran a check and followed the plan", clean, map[string]int{}},
		{"lines changed, a point per 200", with(func(f *Facts) { f.Files[0].Added = 1000 }), map[string]int{FactorLines: 5}},
		{"lines capped", with(func(f *Facts) { f.Files[0].Added = 90000 }), map[string]int{FactorLines: w.LinesMost}},
		{"a lockfile's lines are the manifest's, not the change's", with(func(f *Facts) {
			f.Files = append(f.Files, gitx.FileStat{Path: "pnpm-lock.yaml", Added: 5000})
		}), map[string]int{FactorManifests: w.Manifests}},
		{"areas past two", with(func(f *Facts) {
			f.Files = append(f.Files, gitx.FileStat{Path: "internal/b/b.go"}, gitx.FileStat{Path: "cmd/x/main.go"}, gitx.FileStat{Path: "docs/x.md"}, gitx.FileStat{Path: "README.md"})
		}), map[string]int{FactorAreas: 3}},
		{"files other phases name", with(func(f *Facts) { f.Named = []string{"a/a.go", "internal/a/a_test.go", "other.go"} }), map[string]int{FactorShared: 2}},
		{"a migration", with(func(f *Facts) {
			f.Files = append(f.Files, gitx.FileStat{Path: "db/migrations/0042_add.sql", Added: 10})
		}), map[string]int{FactorMigrations: w.Migrations}},
		{"code with no test changed", with(func(f *Facts) { f.Files = f.Files[:1] }), map[string]int{FactorUntested: w.Untested}},
		{"a little code with no test changed", with(func(f *Facts) { f.Files = []gitx.FileStat{{Path: "x.go", Added: 3}} }), map[string]int{}},
		{"docs only", with(func(f *Facts) { f.Files = []gitx.FileStat{{Path: "docs/a.md", Added: 300}} }), map[string]int{FactorLines: 1}},
		{"a test deleted", with(func(f *Facts) { f.Files[1].Removed = true }), map[string]int{FactorTestsDeleted: w.TestsDeleted}},
		{"git unknown leaves the git factors out", with(func(f *Facts) { f.GitKnown = false; f.Files = f.Files[:1] }), map[string]int{}},
		{"finished despite red", with(func(f *Facts) { f.DespiteRed = true }), map[string]int{FactorDespiteRed: w.DespiteRed}},
		{"decisions, capped", with(func(f *Facts) { f.Decisions = 9 }), map[string]int{FactorDecisions: w.DecisionMost}},
		{"conflicts", with(func(f *Facts) { f.Conflicts = 2 }), map[string]int{FactorConflicts: 4}},
		{"no check run and no job", with(func(f *Facts) { f.Checks = 0 }), map[string]int{FactorNoCheck: w.NoCheck}},
		{"a job is a check", with(func(f *Facts) { f.Checks, f.Jobs = 0, 1 }), map[string]int{}},
		{"checks unknown in an older record", with(func(f *Facts) { f.Checks, f.ChecksKnown = 0, false }), map[string]int{}},
		{"no deviation given", with(func(f *Facts) { f.Deviation = "" }), map[string]int{FactorDeviation: w.DeviationUnknown}},
		{"a deviation", with(func(f *Facts) { f.Deviation = "split the store in two" }), map[string]int{FactorDeviation: w.Deviated}},
		{"none, in more words", with(func(f *Facts) { f.Deviation = "None — as planned" }), map[string]int{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			factors := Score(c.f, w)
			got := map[string]int{}
			sum := 0
			for _, f := range factors {
				got[f.Name] = f.Points
				sum += f.Points
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Score = %+v, want %v", factors, c.want)
			}
			if Points(factors) != sum {
				t.Errorf("Points = %d, want %d", Points(factors), sum)
			}
		})
	}
}

// Each factor says what it found.
func TestScoreDetails(t *testing.T) {
	f := Facts{GitKnown: true, ChecksKnown: true, Deviation: "split it", Files: []gitx.FileStat{{Path: "internal/a/a.go", Added: 450}}}
	got := map[string]string{}
	for _, x := range Score(f, DefaultWeights) {
		got[x.Name] = x.Detail
	}
	want := map[string]string{FactorLines: "450 lines changed", FactorUntested: "450 lines of code, no test changed", FactorNoCheck: "", FactorDeviation: "split it"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("details = %q, want %q", got, want)
	}
}

// The review line hands over once per crossing, the hard line once more whatever came before; off, none.
func TestLinesCrossed(t *testing.T) {
	l := Lines{On: true, Review: 20, Full: 40}
	for _, c := range []struct {
		prev  string
		total int
		want  string
	}{
		{"", 19, ""},
		{"", 20, state.RiskReview},
		{state.RiskReview, 30, ""},
		{state.RiskReview, 40, state.RiskFull},
		{"", 55, state.RiskFull},
		{state.RiskFull, 90, ""},
	} {
		if got := l.Crossed(c.prev, c.total); got != c.want {
			t.Errorf("Crossed(%q, %d) = %q, want %q", c.prev, c.total, got, c.want)
		}
	}
	if got := (Lines{Review: 1, Full: 2}).Crossed("", 50); got != "" {
		t.Errorf("an off ledger crossed %q", got)
	}
	if l.Line(state.RiskFull) != 40 || l.Line(state.RiskReview) != 20 {
		t.Error("Line")
	}
}

// What kind of file a path is.
func TestFileKinds(t *testing.T) {
	for _, c := range []struct {
		path                            string
		test, code, manifest, migration bool
		area                            string
	}{
		{"internal/loop/risk.go", false, true, false, false, "internal/loop"},
		{"internal/loop/risk_test.go", true, false, false, false, "internal/loop"},
		{"test/e2e/harness_test.go", true, false, false, false, "test"},
		{"src/app/page.spec.tsx", true, false, false, false, "src/app"},
		{"tests/test_api.py", true, false, false, false, "tests"},
		{"app/models/user.rb", false, true, false, false, "app/models"},
		{"go.mod", false, false, true, false, "."},
		{"web/package.json", false, false, true, false, "web"},
		{"requirements-dev.txt", false, false, true, false, "."},
		{"db/migrations/001_init.sql", false, false, false, true, "db"},
		{"alembic/versions/abc.py", false, false, false, true, "alembic"},
		{"docs/design.md", false, false, false, false, "docs"},
		{"vendor/x/y.go", false, false, false, false, "vendor"},
	} {
		if IsTest(c.path) != c.test || IsCode(c.path) != c.code || IsManifest(c.path) != c.manifest || IsMigration(c.path) != c.migration || Area(c.path) != c.area {
			t.Errorf("%s: test %v code %v manifest %v migration %v area %q", c.path, IsTest(c.path), IsCode(c.path), IsManifest(c.path), IsMigration(c.path), Area(c.path))
		}
	}
	for _, c := range []struct {
		named, path string
		want        bool
	}{
		{"internal/state/state.go", "internal/state/state.go", true},
		{"cli/plancmds.go", "internal/cli/plancmds.go", true},
		{"internal/risk", "internal/risk/risk.go", true},
		{"internal/risk/", "internal/risk/risk.go", true},
		{"lan.go", "internal/plan.go", false},
		{"", "x.go", false},
	} {
		if got := Names(c.named, c.path); got != c.want {
			t.Errorf("Names(%q, %q) = %v", c.named, c.path, got)
		}
	}
}

// A deviation that says the phase followed the plan.
func TestNoDeviation(t *testing.T) {
	for d, want := range map[string]bool{"none": true, "None.": true, "n/a": true, "no": true, "none — as planned": true, "No deviations": true,
		"nonetheless split it": false, "notable: moved a file": false, "renamed the flag": false} {
		if got := NoDeviation(d); got != want {
			t.Errorf("NoDeviation(%q) = %v", d, got)
		}
	}
}
