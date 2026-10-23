package lane

import (
	"testing"
	"time"
)

// A failed test's time is read from its runner's failure line, without changing the id read: go's own line
// over its subtests', Playwright's longest attempt, vitest's milliseconds or seconds; none where the line
// gives none.
func TestFailedTestsTook(t *testing.T) {
	s, ms := time.Second, time.Millisecond
	cases := []struct {
		name   string
		runner string
		lines  []string
		id     string
		took   time.Duration
	}{
		{"go", RunGo, []string{"--- FAIL: TestA (300.00s)", "FAIL\tex.com/a\t300.1s"}, "TestA", 300 * s},
		{"go, its subtests after it", RunGo, []string{"--- FAIL: TestA (61.50s)", "    --- FAIL: TestA/one (30.00s)", "    --- FAIL: TestA/two (31.50s)"},
			"TestA", 61500 * ms},
		{"go, a subtest alone", RunGo, []string{"    --- FAIL: TestA/one (2.00s)"}, "TestA", 2 * s},
		{"go, no time", RunGo, []string{"--- FAIL: TestA"}, "TestA", 0},
		{"go, not a time", RunGo, []string{"--- FAIL: TestA (soon)"}, "TestA", 0},
		{"Playwright, minutes", RunPlaywright, []string{"  ✘  7 [chromium] › tests/a.spec.ts:12:7 › opens (1.2m)"}, "tests/a.spec.ts:12", 72 * s},
		{"Playwright, its retry longer", RunPlaywright, []string{"  ✘  7 tests/a.spec.ts:12:7 › opens (30.0s)",
			"  ✘  8 tests/a.spec.ts:12:7 › opens (retry #1) (45.0s)"}, "tests/a.spec.ts:12", 45 * s},
		{"Playwright, milliseconds", RunPlaywright, []string{"  x  2 a.spec.js:3:1 › opens (850ms)"}, "a.spec.js:3", 850 * ms},
		{"Playwright, hours", RunPlaywright, []string{"  ✘  2 a.spec.js:3:1 › opens (1.5h)"}, "a.spec.js:3", 90 * time.Minute},
		{"Playwright, its details", RunPlaywright, []string{"  1) [chromium] › tests/a.spec.ts:12:7 › opens ─────"}, "tests/a.spec.ts:12", 0},
		{"vitest, milliseconds", RunVitest, []string{" ❯ src/a.test.ts (3 tests | 1 failed)", "   × Suite > adds 30001ms"}, "src/a.test.ts > Suite > adds", 30001 * ms},
		{"vitest, seconds", RunVitest, []string{" ❯ src/a.test.ts (3 tests | 1 failed)", "   × adds 31.20s"}, "src/a.test.ts > adds", 31200 * ms},
		{"vitest, a name that ends in minutes", RunVitest, []string{" ❯ src/a.test.ts (1)", "   × waits 5m"}, "src/a.test.ts > waits 5m", 0},
		{"vitest, its summary", RunVitest, []string{" FAIL  src/a.test.ts > adds"}, "src/a.test.ts > adds", 0},
		{"jest gives none", RunJest, []string{"FAIL src/a.test.js (40.1 s)", "  ● adds"}, "src/a.test.js > adds", 0},
	}
	for _, c := range cases {
		p, _ := PresetFor(c.runner)
		fs := read(p, c.lines...)
		if len(fs) != 1 || fs[0].ID != c.id || fs[0].Took != c.took {
			t.Errorf("%s: read %+v, want %q in %s", c.name, fs, c.id, c.took)
		}
	}
}

// A test named twice is one test, whatever each line says it took, and keeps the longest.
func TestAReadingKeepsATestOnceWithItsLongestTime(t *testing.T) {
	pw, _ := PresetFor(RunPlaywright)
	fs := read(pw, "  ✘  2 [chromium] › a.spec.ts:3:5 › fails (40.0s)", "  ✘  3 [chromium] › a.spec.ts:3:5 › fails (retry #1) (39.0s)",
		"  1) [chromium] › a.spec.ts:3:5 › fails ───", "  1 failed", "    [chromium] › a.spec.ts:3:5 › fails ───")
	if len(fs) != 1 || fs[0].Took != 40*time.Second {
		t.Errorf("read %+v", fs)
	}
	a := FailedTest{Runner: RunGo, ID: "TestA", Scope: "ex.com/a", Took: time.Second}
	b := a
	b.Took = 0
	if !a.Same(b) {
		t.Error("one test with two times is two tests")
	}
	if b.Scope = "ex.com/b"; a.Same(b) {
		t.Error("tests of two packages are one")
	}
}
