package lane

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFailure(t *testing.T) {
	for line, want := range map[string]bool{
		"--- FAIL: TestRanking (0.02s)":                   true,
		"    --- FAIL: TestRanking/ties (0.00s)":          true,
		"FAIL\tgithub.com/x/y\t1.2s":                      true,
		"FAIL github.com/x/y [build failed]":              true,
		"FAIL":                                            true,
		"FAIL\r":                                          true,
		"panic: runtime error: index out of range":        true,
		"FAILED tests/test_api.py::test_login - assert 1": true,
		"test parser::tests::empty ... FAILED":            true,
		"  ✗ renders the board (12 ms)":                   true,
		"✘ 2 failing":                                     true,
		"--- PASS: TestRanking (0.02s)":                   false,
		"ok  \tgithub.com/x/y\t1.2s":                      false,
		"=== RUN   TestFAILEDLogin":                       false,
		"the FAILEDness of it":                            false,
		"FAILURES are counted below":                      false,
		"    scenario_test.go:88: FAIL? no":               false,
		"":                                                false,
	} {
		if got := Failure(line); got != want {
			t.Errorf("Failure(%q) = %v, want %v", line, got, want)
		}
	}
}

// A result lists the lines that report a failure from before the end of the log it shows, in order, at
// most as many as asked, each clipped, and counts the rest: a failure early in a long log is never lost
// behind its end. A log whose end is all of it lists none.
func TestReadExcerpt(t *testing.T) {
	write := func(t *testing.T, lines []string) string {
		path := filepath.Join(t.TempDir(), "j1.log")
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	var long []string
	long = append(long, "=== RUN   TestEarly", "--- FAIL: TestEarly (0.01s)", "    early_test.go:9: want 1, got 2")
	for i := range 500 {
		long = append(long, fmt.Sprintf("=== RUN   TestOK%d", i), fmt.Sprintf("--- PASS: TestOK%d (0.00s)", i))
	}
	long = append(long, "--- FAIL: TestLate (0.01s)", "FAIL", "FAIL\tgithub.com/x/y\t3.2s")

	t.Run("an early failure in a long log", func(t *testing.T) {
		e, err := ReadExcerpt(write(t, long), 10, 6<<10, 20, 200)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(e.Failures, []string{"--- FAIL: TestEarly (0.01s)"}) || e.More != 0 {
			t.Errorf("failures %q, more %d", e.Failures, e.More)
		}
		if !strings.HasSuffix(e.Tail, "--- FAIL: TestLate (0.01s)\nFAIL\nFAIL\tgithub.com/x/y\t3.2s") || strings.Count(e.Tail, "\n") != 9 {
			t.Errorf("tail %q", e.Tail)
		}
	})
	t.Run("a short log is all its end", func(t *testing.T) {
		e, err := ReadExcerpt(write(t, []string{"--- FAIL: TestA (0.01s)", "FAIL"}), 80, 6<<10, 20, 200)
		if err != nil || len(e.Failures) != 0 || e.Tail != "--- FAIL: TestA (0.01s)\nFAIL" {
			t.Errorf("%+v %v", e, err)
		}
	})
	t.Run("capped and clipped", func(t *testing.T) {
		var lines []string
		for i := range 25 {
			lines = append(lines, fmt.Sprintf("--- FAIL: Test%02d %s", i, strings.Repeat("é", 200)))
		}
		lines = append(lines, "the end", "really")
		e, err := ReadExcerpt(write(t, lines), 2, 6<<10, 20, 40)
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Failures) != 20 || e.More != 5 || !strings.HasPrefix(e.Failures[0], "--- FAIL: Test00 ") || !strings.HasPrefix(e.Failures[19], "--- FAIL: Test19 ") {
			t.Fatalf("failures %d, more %d: %q", len(e.Failures), e.More, e.Failures)
		}
		for _, f := range e.Failures {
			if len(f) > 40+len("…") || !strings.HasSuffix(f, "é…") {
				t.Errorf("clipped badly: %q", f)
			}
		}
	})
	t.Run("a line longer than the reader's buffer", func(t *testing.T) {
		e, err := ReadExcerpt(write(t, []string{strings.Repeat("x", 200<<10), "--- FAIL: TestAfter (0s)", "end"}), 1, 6<<10, 20, 200)
		if err != nil || !slices.Equal(e.Failures, []string{"--- FAIL: TestAfter (0s)"}) {
			t.Errorf("%q %v", e.Failures, err)
		}
	})
	if _, err := ReadExcerpt(filepath.Join(t.TempDir(), "missing.log"), 80, 6<<10, 20, 200); err == nil {
		t.Error("a missing log read")
	}
}
