package lane

import "testing"

// Each runner's own arguments run only the tests a stage failed in, as words with no space inside one, so an
// unquoted $BATON_FAILED_ARGS passes them; ids they cannot carry give none.
func TestArgsRunOnlyWhatFailed(t *testing.T) {
	cases := []struct {
		runner string
		ids    []string
		want   string
	}{
		{RunGo, []string{"TestA", "TestB/sub"}, "-run=^(TestA|TestB/sub)$"},
		{RunPytest, []string{"tests/x.py::test_a[1]", "tests/y.py::TestC::test_m"}, "tests/x.py::test_a[1] tests/y.py::TestC::test_m"},
		{RunPytest, []string{"tests/x.py::test_a[a b]"}, ""},
		{RunVitest, []string{"src/a.test.ts > Suite > adds one", "src/a.test.ts > Suite > adds  two", "src/b.test.ts > b (x)"},
			`src/a.test.ts src/b.test.ts -t adds\s+one|adds\s+two|b\s+\(x\)`},
		{RunJest, []string{"src/a.test.js > works"}, "src/a.test.js -t works"},
		{RunJest, []string{"src/a.test.js > works", "src/c.test.js"}, "src/a.test.js src/c.test.js"},
		{RunPlaywright, []string{"test/a.spec.ts:36", "test/b.spec.ts:116"}, "test/a.spec.ts:36 test/b.spec.ts:116"},
	}
	for _, c := range cases {
		p, ok := PresetFor(c.runner)
		if !ok || p.Args == nil {
			t.Fatalf("%s: no Args", c.runner)
		}
		if got := p.Args(c.ids); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.runner, c.ids, got, c.want)
		}
	}
}
