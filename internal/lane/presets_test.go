package lane

import (
	"slices"
	"testing"
)

// Each runner's lines that name a failed test, and those that do not.
func TestPresetFailures(t *testing.T) {
	cases := []struct {
		runner string
		lines  []string
		want   string // the id read, of the last line
	}{
		{RunGo, []string{"--- FAIL: TestA (0.01s)"}, "TestA"},
		{RunGo, []string{"        --- FAIL: TestA/sub/deeper (0.00s)"}, "TestA"},
		{RunGo, []string{"--- FAIL: TestA"}, "TestA"},
		{RunGo, []string{"--- PASS: TestA (0.01s)"}, ""},
		{RunGo, []string{"FAIL\tgithub.com/x/y\t0.4s"}, "github.com/x/y"}, // whole: no test of it failed
		{RunGo, []string{"FAIL: TestA"}, ""},
		{RunGo, []string{"--- FAIL: "}, ""},
		{RunPytest, []string{"FAILED tests/test_x.py::test_a - assert 1 == 2"}, "tests/test_x.py::test_a"},
		{RunPytest, []string{"FAILED tests/test_x.py::test_a[a b - c] - assert 0"}, "tests/test_x.py::test_a[a b - c]"},
		{RunPytest, []string{"FAILED tests/test_x.py::test_a - assert x[0] - 1"}, "tests/test_x.py::test_a"},
		{RunPytest, []string{"FAILED tests/test_x.py::TestC::test_m"}, "tests/test_x.py::TestC::test_m"},
		{RunPytest, []string{"FAILED tests/test_x.py - collection error"}, ""},
		{RunPytest, []string{"ERROR tests/test_x.py::test_a - fixture"}, ""},
		{RunPytest, []string{"tests/test_x.py::test_a FAILED [ 50%]"}, "tests/test_x.py::test_a"},
		{RunPytest, []string{"tests/test_x.py::test_a[a b] FAILED"}, "tests/test_x.py::test_a[a b]"},
		{RunPytest, []string{"[gw2] [ 50%] FAILED tests/test_x.py::test_a "}, "tests/test_x.py::test_a"},
		{RunPytest, []string{"[gw2] FAILED tests/test_x.py::test_b"}, "tests/test_x.py::test_b"},
		{RunPytest, []string{"E   AssertionError: tests/x.py::foo FAILED"}, ""},
		{RunPytest, []string{"tests/test_x.py::test_a FAILED to start"}, ""},
		{RunPytest, []string{"  FAILED tests/test_x.py::test_a"}, ""},
		{RunVitest, []string{" FAIL  src/a.test.ts > Suite > nested > adds"}, "src/a.test.ts > Suite > nested > adds"},
		{RunVitest, []string{" FAIL  |unit| src/a.test.ts > adds"}, "src/a.test.ts > adds"},
		{RunVitest, []string{" FAIL  src/a.test.ts [ src/a.test.ts ]"}, "src/a.test.ts"},
		{RunVitest, []string{" FAIL  src/a.test.ts [ src/b.test.ts ]"}, ""},
		{RunVitest, []string{" ❯ src/a.test.ts (3 tests | 1 failed) 4ms", "   × Suite > adds 2ms"}, "src/a.test.ts > Suite > adds"},
		{RunVitest, []string{" ❯ src/a.test.ts (3 tests | 1 failed) 4ms", "   ✕ adds"}, "src/a.test.ts > adds"},
		{RunVitest, []string{"   × adds 2ms"}, ""}, // no file yet
		{RunVitest, []string{" ❯ src/a.test.ts (1)", " ❯ src/lib.ts:3:9", "   × adds 1.2s"}, "src/a.test.ts > adds"},
		{RunJest, []string{"FAIL src/a.test.js", "  ● Suite › nested › adds"}, "src/a.test.js > Suite > nested > adds"},
		{RunJest, []string{"FAIL  unit  src/a.test.tsx (6.1 s)", "  ● adds"}, "src/a.test.tsx > adds"},
		{RunJest, []string{"FAIL src/a.test.js", "  ● Test suite failed to run"}, "src/a.test.js"},
		{RunJest, []string{"FAIL src/a.test.js", "  ● Console"}, ""},
		{RunJest, []string{"FAIL src/a.test.js", "● Validation Warning:"}, ""},
		{RunJest, []string{"FAIL src/a.test.js", "PASS src/b.test.js", "  ● adds"}, ""},
		{RunJest, []string{"  ● adds"}, ""},
		{RunPlaywright, []string{"  ✘  2 [chromium] › tests/a.spec.ts:12:7 › home › opens (5.2s)"}, "tests/a.spec.ts:12"},
		{RunPlaywright, []string{"  ✘  2 tests/a.spec.ts:12:7 › opens (5.2s)"}, "tests/a.spec.ts:12"},
		{RunPlaywright, []string{"  x  2 [webkit] › a.spec.js:3:1 › opens"}, "a.spec.js:3"},
		{RunPlaywright, []string{"  1) [chromium] › tests/a.spec.ts:12:7 › home › opens ─────"}, "tests/a.spec.ts:12"},
		{RunPlaywright, []string{"  ✓  1 [chromium] › tests/a.spec.ts:5:7 › has a title (1.1s)"}, ""},
		{RunPlaywright, []string{"  1) Suite name:"}, ""},
		{RunPlaywright, []string{"    [chromium] › tests/a.spec.ts:12:7 › home › opens ───"}, ""},
	}
	for _, c := range cases {
		p, ok := PresetFor(c.runner)
		if !ok {
			t.Fatalf("no preset %s", c.runner)
		}
		got := ""
		if fs := read(p, c.lines...); len(fs) > 0 {
			got = fs[len(fs)-1].ID
		}
		if got != c.want {
			t.Errorf("%s reads %q as %q, want %q", c.runner, c.lines, got, c.want)
		}
	}
	if _, ok := PresetFor("mocha"); ok {
		t.Error("a preset for mocha")
	}
}

func TestPresetNarrow(t *testing.T) {
	cases := []struct {
		runner      string
		ids         []string
		name, value string
	}{
		{RunGo, []string{"TestA"}, "GOFLAGS", "-run=^(TestA)$"},
		{RunGo, []string{"TestA", "TestB"}, "GOFLAGS", "-run=^(TestA|TestB)$"},
		{RunPytest, []string{"tests/x.py::test_a[1]", "tests/x.py::test_a[2]", "tests/y.py::TestC::test_m"}, "PYTEST_ADDOPTS", `-k "test_a or test_m"`},
	}
	for _, c := range cases {
		p, _ := PresetFor(c.runner)
		if name, value := p.Narrow(c.ids); name != c.name || value != c.value {
			t.Errorf("%s narrows %q by %s=%s, want %s=%s", c.runner, c.ids, name, value, c.name, c.value)
		}
	}
	for _, r := range []string{RunVitest, RunJest, RunPlaywright} {
		if p, _ := PresetFor(r); p.Narrow != nil {
			t.Errorf("%s narrows through its environment", r)
		}
	}
}

// A rerun's command quotes what the shell would read otherwise, and is empty for no tests.
func TestPresetRerunQuoting(t *testing.T) {
	cases := []struct {
		runner string
		fs     []FailedTest
		want   string
	}{
		{RunPytest, []FailedTest{{ID: "tests/x.py::test_a[it's]"}}, `pytest 'tests/x.py::test_a[it'\''s]'`},
		{RunVitest, []FailedTest{{ID: "src/a.test.ts > adds (1+1)"}, {ID: "src/a.test.ts > it's"}}, `vitest run src/a.test.ts -t 'adds \(1\+1\)|it'\''s'`},
		{RunPlaywright, []FailedTest{{ID: "my tests/a.spec.ts:3", Scope: "Mobile Safari"}}, `playwright test 'my tests/a.spec.ts:3' '--project=Mobile Safari'`},
		{RunPlaywright, []FailedTest{{ID: "a.spec.ts:3", Scope: "chromium"}, {ID: "b.spec.ts:4"}}, `playwright test a.spec.ts:3 b.spec.ts:4`},
		{RunGo, []FailedTest{{ID: "TestA", Scope: "ex.com/a"}}, `go test -run '^(TestA)$' ex.com/a`},
	}
	for _, c := range cases {
		p, _ := PresetFor(c.runner)
		if got := p.Rerun(c.fs); got != c.want {
			t.Errorf("%s reruns %+v as\n%s\nwant\n%s", c.runner, c.fs, got, c.want)
		}
	}
	for _, p := range Presets {
		if got := p.Rerun(nil); got != "" {
			t.Errorf("%s reruns nothing as %q", p.Name, got)
		}
	}
}

// The ways a command can name a test: its id, the name its runner selects it by, the file or package that
// failed whole.
func TestPresetNames(t *testing.T) {
	cases := []struct {
		runner string
		f      FailedTest
		want   []string
	}{
		{RunGo, FailedTest{ID: "TestA", Scope: "ex.com/m/a"}, []string{"TestA"}},
		{RunGo, FailedTest{ID: "ex.com/m/internal/lane", Whole: true}, []string{"ex.com/m/internal/lane", "m/internal/lane", "internal/lane"}},
		{RunPytest, FailedTest{ID: "tests/x.py::TestC::test_a[1-2]"}, []string{"tests/x.py::TestC::test_a[1-2]", "tests/x.py::TestC::test_a", "test_a"}},
		{RunPytest, FailedTest{ID: "tests/x.py::test_a"}, []string{"tests/x.py::test_a", "test_a"}},
		{RunVitest, FailedTest{ID: "src/a.test.ts > Suite > adds"}, []string{"src/a.test.ts > Suite > adds", "adds"}},
		{RunJest, FailedTest{ID: "src/a.test.js", Whole: true}, []string{"src/a.test.js"}},
		{RunPlaywright, FailedTest{ID: "a.spec.ts:3", Scope: "chromium"}, []string{"a.spec.ts:3"}},
	}
	for _, c := range cases {
		p, _ := PresetFor(c.runner)
		if got := p.Names(c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s names %+v %q, want %q", c.runner, c.f, got, c.want)
		}
	}
}
