package lane

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The tests a red log says failed are read whatever runner printed it, colour and Windows line ends
// included, each once, in the order first named, with the command that reruns only them.
func TestFailureIDs(t *testing.T) {
	type ft = FailedTest
	cases := []struct {
		log    string
		crlf   bool
		want   []FailedTest
		reruns []string
	}{
		{"go.log", false, []ft{
			{Runner: RunGo, ID: "TestRanking", Scope: "example.com/m/internal/rank"},
			{Runner: RunGo, ID: "TestParse", Scope: "example.com/m/internal/rank"},
			{Runner: RunGo, ID: "example.com/m/internal/broken", Scope: "example.com/m/internal/broken", Whole: true},
			{Runner: RunGo, ID: "example.com/m/internal/slow", Scope: "example.com/m/internal/slow", Whole: true},
			{Runner: RunGo, ID: "TestParse", Scope: "example.com/m/internal/other"},
		}, []string{"go test -run '^(TestRanking|TestParse)$' example.com/m/internal/rank example.com/m/internal/other" +
			" && go test example.com/m/internal/broken example.com/m/internal/slow"}},
		{"pytest.log", true, []ft{
			{Runner: RunPytest, ID: "tests/test_api.py::test_logout"},
			{Runner: RunPytest, ID: "tests/test_calc.py::test_add[a b-c]"},
			{Runner: RunPytest, ID: "tests/test_calc.py::TestDiv::test_zero"},
		}, []string{"pytest tests/test_api.py::test_logout 'tests/test_calc.py::test_add[a b-c]' tests/test_calc.py::TestDiv::test_zero"}},
		{"pytest-xdist.log", false, []ft{
			{Runner: RunPytest, ID: "tests/test_api.py::test_logout"},
			{Runner: RunPytest, ID: "tests/test_calc.py::test_add[a b-c]"},
		}, []string{"pytest tests/test_api.py::test_logout 'tests/test_calc.py::test_add[a b-c]'"}},
		{"vitest.log", false, []ft{
			{Runner: RunVitest, ID: "src/board.test.ts > Board > moves > rejects a move off the grid"},
			{Runner: RunVitest, ID: "src/board.test.ts > Board > keeps score"},
			{Runner: RunVitest, ID: "src/view.test.tsx > View > draws the band"},
			{Runner: RunVitest, ID: "src/broken.test.ts", Whole: true},
		}, []string{"vitest run src/board.test.ts src/view.test.tsx -t 'rejects a move off the grid|keeps score|draws the band'" +
			" && vitest run src/broken.test.ts"}},
		{"jest.log", true, []ft{
			{Runner: RunJest, ID: "src/cart.test.js > Cart > totals > applies the discount"},
			{Runner: RunJest, ID: "src/cart.test.js > empties"},
			{Runner: RunJest, ID: "src/legacy.test.js", Whole: true},
		}, []string{"jest src/cart.test.js -t 'applies the discount|empties' && jest src/legacy.test.js"}},
		{"playwright.log", false, []ft{
			{Runner: RunPlaywright, ID: "tests/home.spec.ts:12", Scope: "chromium"},
			{Runner: RunPlaywright, ID: "tests/cart.spec.ts:8", Scope: "firefox"},
		}, []string{"playwright test tests/home.spec.ts:12 tests/cart.spec.ts:8 --project=chromium --project=firefox"}},
		{"mixed.log", true, []ft{
			{Runner: RunPytest, ID: "tests/test_api.py::test_logout"},
			{ID: "lint:unused-import"},
			{Runner: RunPlaywright, ID: "e2e/login.spec.ts:4"},
		}, []string{"pytest tests/test_api.py::test_logout", "playwright test e2e/login.spec.ts:4"}},
	}
	for _, c := range cases {
		t.Run(c.log, func(t *testing.T) {
			path := filepath.Join("testdata", c.log)
			if c.crlf {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(t.TempDir(), c.log)
				if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "\n", "\r\n")), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, more, err := FailureIDs(path, 0)
			if err != nil || more != 0 {
				t.Fatalf("more %d, err %v", more, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("read\n%+v\nwant\n%+v", got, c.want)
			}
			if r := Reruns(got); !slices.Equal(r, c.reruns) {
				t.Errorf("reruns\n%q\nwant\n%q", r, c.reruns)
			}
		})
	}
	t.Run("capped", func(t *testing.T) {
		got, more, err := FailureIDs(filepath.Join("testdata", "go.log"), 2)
		if err != nil || more != 3 || len(got) != 2 || got[1].ID != "TestParse" {
			t.Errorf("%+v, more %d, err %v", got, more, err)
		}
	})
	t.Run("a log of passes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "j1.log")
		os.WriteFile(path, []byte("ok  \texample.com/m\t0.1s\n"), 0o644)
		if got, more, err := FailureIDs(path, 0); len(got) != 0 || more != 0 || err != nil || Reruns(got) != nil {
			t.Errorf("%+v, more %d, err %v", got, more, err)
		}
	})
	if _, _, err := FailureIDs(filepath.Join(t.TempDir(), "missing.log"), 0); err == nil {
		t.Error("a missing log read")
	}
}

// read is what a Reading of preset makes of lines.
func read(p Preset, lines ...string) []FailedTest {
	r := NewReading(MaxFailures, p)
	for _, l := range lines {
		r.Line(l)
	}
	fs, _ := r.Failed()
	return fs
}

// The tests a go run names are kept in the order first named, in the package its FAIL line names, and a
// gate stopped mid-package keeps them with no package; a package's ok line drops what its tests printed.
func TestAGoReadingKeepsTheOrderAndThePackage(t *testing.T) {
	go_, _ := PresetFor(RunGo)
	r := NewReading(MaxFailures, go_)
	for _, l := range []string{"--- FAIL: TestA (0s)", "::baton fail mine", "--- FAIL: TestA/sub (0s)", "--- FAIL: TestB (0s)",
		"FAIL\tex.com/a\t0.1s", "    --- FAIL: TestPrinted (0s)", "ok  \tex.com/b\t0.1s", "--- FAIL: TestC (0s)"} {
		r.Line(l)
	}
	got, _ := r.Failed()
	want := []FailedTest{{Runner: RunGo, ID: "TestA", Scope: "ex.com/a"}, {ID: "mine"}, {Runner: RunGo, ID: "TestB", Scope: "ex.com/a"},
		{Runner: RunGo, ID: "TestC"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read %+v, want %+v", got, want)
	}
	if cmd := go_.Rerun(got[2:]); cmd != "go test -run '^(TestB|TestC)$' ./..." {
		t.Errorf("rerun %q", cmd)
	}
}

func TestPlainLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain\r\n":                                    "plain",
		"\x1b[31m×\x1b[39m name":                       "× name",
		"\x1b[1;31mFAIL\x1b[0m x":                      "FAIL x",
		"\x1b]8;;file:///a.ts\x1b\\a.ts\x1b]8;;\x1b\\": "a.ts",
		"\x1b]0;title\x07after":                        "after",
		"\x1b(Bkept":                                   "kept",
		"cut\x1b":                                      "cut",
		"cut\x1b[3":                                    "cut",
	} {
		if got := plainLine(in); got != want {
			t.Errorf("plainLine(%q) = %q, want %q", in, got, want)
		}
	}
}
