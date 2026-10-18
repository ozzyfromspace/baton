package lane

import (
	"slices"
	"strings"
	"testing"
)

// A command names a test by its id, the name its runner selects it by, or the file or package that failed
// whole, never as part of a longer name; each runner's own rerun names every test it reruns.
func TestUnnamed(t *testing.T) {
	goA := FailedTest{Runner: RunGo, ID: "TestParse", Scope: "ex.com/m/src"}
	goB := FailedTest{Runner: RunGo, ID: "TestLex", Scope: "ex.com/m/src"}
	goPkg := FailedTest{Runner: RunGo, ID: "ex.com/m/internal/lane", Whole: true}
	py := FailedTest{Runner: RunPytest, ID: "tests/test_x.py::test_a[1-2]"}
	vi := FailedTest{Runner: RunVitest, ID: "src/a.test.ts > Sum > adds 1 + 2"}
	je := FailedTest{Runner: RunJest, ID: "src/b.test.js > parses"}
	pw := FailedTest{Runner: RunPlaywright, ID: "e2e/login.spec.ts:3", Scope: "chromium"}
	bare := FailedTest{ID: "lint:unused"}
	for _, c := range []struct {
		name    string
		command string
		fs      []FailedTest
		want    []string // the ids left out
	}{
		{"go, its own rerun", "go test -run '^(TestParse|TestLex)$' ex.com/m/src", []FailedTest{goA, goB}, nil},
		{"go, one left out", "go test -run '^TestParse$' ./src", []FailedTest{goA, goB}, []string{"TestLex"}},
		{"go, a longer name is another test", "go test -run TestParseAll ./src", []FailedTest{goA}, []string{"TestParse"}},
		{"go, through make", "make test GOFLAGS=-run=TestParse", []FailedTest{goA}, nil},
		{"go, a package by its end", "go test ./internal/lane", []FailedTest{goPkg}, nil},
		{"go, not another package", "go test ./internal/lanes", []FailedTest{goPkg}, []string{"ex.com/m/internal/lane"}},
		{"pytest, by node id", "pytest 'tests/test_x.py::test_a[1-2]'", []FailedTest{py}, nil},
		{"pytest, by name", "pytest tests -k test_a", []FailedTest{py}, nil},
		{"pytest, not a longer name", "pytest tests -k test_ab", []FailedTest{py}, []string{py.ID}},
		{"vitest, a pattern's escapes", "vitest run src/a.test.ts -t 'adds 1 \\+ 2'", []FailedTest{vi}, nil},
		{"jest, by name", "docker compose run app jest src/b.test.js -t parses", []FailedTest{je}, nil},
		{"playwright, by file:line", "playwright test e2e/login.spec.ts:3", []FailedTest{pw}, nil},
		{"playwright, not another line", "playwright test e2e/login.spec.ts:30", []FailedTest{pw}, []string{pw.ID}},
		{"a fail line's id", "sh lint.sh lint:unused", []FailedTest{bare}, nil},
		{"the whole gate names none", "make test", []FailedTest{goA, bare}, []string{"TestParse", "lint:unused"}},
		{"two commands", "go test -run TestParse ./src && pytest -k test_a", []FailedTest{goA, py}, nil},
	} {
		var got []string
		for _, f := range Unnamed(c.command, c.fs) {
			got = append(got, f.ID)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: `%s` leaves out %q, want %q", c.name, c.command, got, c.want)
		}
	}
	all := []FailedTest{goA, goB, goPkg, py, vi, je, pw}
	if left := Unnamed(strings.Join(Reruns(all), " && "), all); len(left) > 0 {
		t.Errorf("the runners' own reruns leave out %+v", left)
	}
}
