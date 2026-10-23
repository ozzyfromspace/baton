package lane

import "testing"

// A failed test's file is read from its id, as each runner names it; a go test's and a fail line's id name
// none.
func TestAFailedTestsFile(t *testing.T) {
	for _, c := range []struct {
		f    FailedTest
		want string
	}{
		{FailedTest{Runner: RunPytest, ID: "tests/test_x.py::TestA::test_a[p]"}, "tests/test_x.py"},
		{FailedTest{Runner: RunVitest, ID: "src/a.test.ts > suite > name"}, "src/a.test.ts"},
		{FailedTest{Runner: RunVitest, ID: "src/b.test.ts", Whole: true}, "src/b.test.ts"},
		{FailedTest{Runner: RunJest, ID: "src/c.test.js > name"}, "src/c.test.js"},
		{FailedTest{Runner: RunPlaywright, ID: "tests/login.spec.ts:12", Scope: "chromium"}, "tests/login.spec.ts"},
		{FailedTest{Runner: RunGo, ID: "TestParse", Scope: "example.com/m/internal/parse"}, ""},
		{FailedTest{Runner: RunGo, ID: "example.com/m/internal/parse", Scope: "example.com/m/internal/parse", Whole: true}, ""},
		{FailedTest{ID: "browser/login"}, ""},
	} {
		if got := c.f.File(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.f, got, c.want)
		}
	}
}
