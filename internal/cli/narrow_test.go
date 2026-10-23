package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red gate built from no stages is rerun narrowed: a command that names every test its log says failed,
// in its runner's own terms or by a fail line's id, runs in place of the gate's and stands in for it; its
// pass clears the red gate, its result says so, and done is accepted.
func TestANarrowedRerunClearsARedGate(t *testing.T) {
	const notes = "--notes=the parser is fixed"
	for _, c := range []struct {
		runner, log string
		narrow      []string // the narrowed command's words
		ids         string
	}{
		{"go", goRed, []string{"go", "test", "-run", "^(TestParse|TestLex)$", "ex.com/m/src"}, "TestParse and TestLex"},
		{"pytest", "FAILED tests/test_x.py::test_a - AssertionError\n", []string{"pytest", "tests/test_x.py::test_a"}, "tests/test_x.py::test_a"},
		{"vitest", " FAIL  src/a.test.ts > Sum > adds\n", []string{"vitest", "run", "src/a.test.ts", "-t", "adds"}, "src/a.test.ts > Sum > adds"},
		{"jest", "FAIL src/b.test.js\n  ● Parser › parses\n", []string{"jest", "src/b.test.js", "-t", "parses"}, "src/b.test.js > Parser > parses"},
		{"playwright", "  1) [chromium] › e2e/login.spec.ts:3:5 › logs in ───\n", []string{"playwright", "test", "e2e/login.spec.ts:3"}, "e2e/login.spec.ts:3"},
		{"a fail line", "::baton fail lint:unused\n", []string{"sh", "lint.sh", "lint:unused"}, "lint:unused"},
	} {
		t.Run(c.runner, func(t *testing.T) {
			s, _ := gitSession(t)
			s.must("", "verify", "make test", notes)
			s.ranAt("j1", state.JobFailed, 40*time.Minute, 12*time.Minute, c.log)
			command := lane.Join(c.narrow)
			out := s.must("", append([]string{"verify", "--failed", notes, "--"}, c.narrow...)...)
			for _, want := range []string{"baton: queued j2 — `" + command + "` (P0)",
				"baton: it reruns only what failed in job j1 (" + c.ids + ") and stands in for it: if it passes, it clears j1; if it fails, it is P0's red gate from then on. " +
					"If `make test` stops at its first failing command, what came after it never ran, and this rerun does not run it either."} {
				if !strings.Contains(out, want) {
					t.Errorf("verify --failed says:\n%s\nwant %q", out, want)
				}
			}
			if strings.Contains(out, "does not clear") || strings.Contains(out, "questions") {
				t.Errorf("verify --failed says:\n%s", out)
			}
			j := s.state().Job("j2")
			if j == nil || j.Command != command || j.Reruns != "j1" || len(j.Narrows) == 0 || j.Replaces != "" {
				t.Fatalf("j2 %+v", j)
			}
			narrows, _ := json.Marshal(j.Narrows)
			if !s.logged("job_queued", map[string]any{"job": "j2", "reruns": "j1", "command": command}) || s.logged("job_queued", map[string]any{"job": "j2", "red_gate": "j1"}) ||
				!strings.Contains(s.events(), `"narrows":`+string(narrows)) {
				t.Errorf("events:\n%s", s.events())
			}

			s.ended("j2", state.JobPassed, 0)
			st := s.state()
			j2 := *st.Job("j2")
			got := decide.JobResult(j2, hooks.JobLog(s.store(), j2), st.Red(), "P7 (QA)")
			if want := "It reran only what failed in job j1 (verify --failed): " + c.ids + ".\nAs it passed, it stands in for j1 and clears it. P7 (QA) runs the whole set.\n"; !strings.Contains(got, want) ||
				strings.Contains(got, "does not clear") || strings.Contains(got, "every stage ran whole") {
				t.Errorf("result:\n%s\nwant %q", got, want)
			}
			if g := st.RedGate("P0"); g != nil {
				t.Errorf("red gate %+v", g)
			}
			s.must("", "done", "P0", "--notes", "parsed")
			if st := s.state(); st.Phases["P0"].Status != state.PhaseDone || len(st.Decisions) != 0 {
				t.Fatalf("P0 %+v, decisions %+v", st.Phases["P0"], st.Decisions)
			}
		})
	}
}

// A narrowed rerun that fails is the red gate from then on, and is rerun narrowed in its turn.
func TestARedNarrowedRerunIsTheRedGate(t *testing.T) {
	const notes = "--notes=the parser is fixed"
	s, _ := gitSession(t)
	s.must("", "verify", "make test", notes)
	s.ranAt("j1", state.JobFailed, 40*time.Minute, 12*time.Minute, goRed)
	s.must("", "verify", "--failed", notes, "--", "go", "test", "-run", "^(TestParse|TestLex)$", "ex.com/m/src")
	s.ranAt("j2", state.JobFailed, 5*time.Minute, time.Minute, "--- FAIL: TestLex (0.00s)\nFAIL\tex.com/m/src\t0.1s\n")
	if g := s.state().RedGate("P0"); g == nil || g.ID != "j2" {
		t.Fatalf("red gate %+v", g)
	}
	out := s.must("", "verify", "--failed", notes, "--", "go", "test", "-run", "^TestLex$", "ex.com/m/src")
	if !strings.Contains(out, "baton: it reruns only what failed in job j2 (TestLex) and stands in for it") {
		t.Errorf("verify --failed says:\n%s", out)
	}
	if j := s.state().Job("j3"); j == nil || j.Reruns != "j2" {
		t.Fatalf("j3 %+v", j)
	}
}

// --failed on a red gate built from no stages is refused, saying why, when the command given does not name
// every failure, or no command can stand in for the gate.
func TestANarrowedRerunIsRefused(t *testing.T) {
	const notes = "--notes=the parser is fixed"
	var many strings.Builder
	for i := range lane.MaxFailures + 1 {
		fmt.Fprintf(&many, "--- FAIL: Test%d (0.00s)\n", i)
	}
	many.WriteString("FAIL\tex.com/m/src\t0.1s\n")
	const offer = "`baton verify --failed --notes \"…\" -- go test -run '^(TestParse|TestLex)$' ex.com/m/src`"
	for _, c := range []struct {
		name, status, log string
		args              []string // after verify --failed
		want              []string
		why               string
	}{
		{"a test left out", state.JobFailed, goRed, []string{"--", "go", "test", "-run", "^TestParse$", "./src"},
			[]string{"not queued — `" + lane.Join([]string{"go", "test", "-run", "^TestParse$", "./src"}) + "` does not name TestLex, which failed in job j1, so it cannot stand in for j1: " +
				"a narrowed rerun names every test that failed, by its id, its test name or its file:line. This one does: " + offer + "."}, "a failure unnamed"},
		{"a longer name", state.JobFailed, goRed, []string{"--", "go", "test", "-run", "TestParseAll|TestLex", "./src"},
			[]string{"does not name TestParse, which failed in job j1"}, "a failure unnamed"},
		{"no command", state.JobFailed, goRed, nil,
			[]string{"not queued — job j1 (`make test` on ", ") recorded no stages, so --failed reruns what failed in it by a command you give that names each test that failed (TestParse and TestLex), " +
				"and that command stands in for j1: " + offer + ", inside the gate's own way of running it (make, docker compose) where it needs one."}, "no command"},
		{"no command, a fail line's id", state.JobFailed, "::baton fail lint:unused\n", nil,
			[]string{"names each test that failed (lint:unused), and that command stands in for j1: `baton verify --failed --notes \"…\" -- <command>`."}, "no command"},
		{"the whole gate", state.JobFailed, goRed, []string{"--", "make", "test"},
			[]string{"not queued — `make test` is job j1's whole command, and --failed with a command reruns only what failed in it (TestParse and TestLex). " +
				"To run the whole gate again, verify it without --failed (baton questions a long repeat once). To rerun only what failed: " + offer + "."}, "the whole gate"},
		{"ran out of time", state.JobTimeout, goRed, []string{"--", "go", "test", "-run", "^(TestParse|TestLex)$", "ex.com/m/src"},
			[]string{") ran out of time, so the tests it never reached are unchecked, and a rerun of what failed in it cannot stand in for it.", "verify it with --replace"}, "timed out"},
		{"no failure read", state.JobFailed, "make: *** [test] Error 1\n", []string{"--", "go", "test", "./src"},
			[]string{"recorded no stages, and baton reads no failed test in its log, so it cannot tell that a narrower command reruns what failed. " +
				"Rerun the failures by name and make that P0's gate: `baton verify --replace --notes \"…\" -- <command>`"}, "no stages"},
		{"more than baton reads", state.JobFailed, many.String(), []string{"--", "go", "test", "./src"},
			[]string{fmt.Sprintf("its log names more failed tests than baton reads by name (over %d)", lane.MaxFailures)}, "too many failures"},
		{"--stages", state.JobFailed, goRed, []string{"--stages", "one", "--", "go", "test", "-run", "^(TestParse|TestLex)$", "ex.com/m/src"},
			[]string{"not queued — --stages names `one`, and job j1 recorded no stages: a rerun of it runs the command you give with --failed, whole."}, "unknown stages"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := gitSession(t)
			s.must("", "verify", "make test", notes)
			s.ranAt("j1", c.status, 40*time.Minute, 12*time.Minute, c.log)
			errs := s.fails(append([]string{"verify", "--failed", notes}, c.args...)...)
			for _, want := range c.want {
				if !strings.Contains(errs, want) {
					t.Errorf("refused with:\n%s\nwant %q", errs, want)
				}
			}
			if !s.logged("refused", map[string]any{"command": "verify", "why": c.why}) {
				t.Errorf("no refused event for %q:\n%s", c.why, s.events())
			}
			if n := len(s.state().Jobs); n != 1 {
				t.Errorf("a refused verify queued a job: %d jobs", n)
			}
		})
	}
}

// A gate built from no stages that showed a failure and has no verdict yet, running or stopped, is rerun
// narrowed too: the rerun replaces it if it runs, and says that what it never reached is unchecked.
func TestANarrowedRerunOfAGateWithNoVerdict(t *testing.T) {
	const notes = "--notes=the parser is fixed"
	for _, status := range []string{state.JobRunning, state.JobInterrupted} {
		t.Run(status, func(t *testing.T) {
			s, _ := gitSession(t)
			s.must("", "verify", "make test", notes)
			s.ranAt("j1", state.JobFailed, 10*time.Minute, 5*time.Minute, goRed)
			s.set(func(x *state.State) {
				j := x.Job("j1")
				j.Status, j.Exit, j.Failing = status, 0, &state.Failing{At: s.now, Line: "--- FAIL: TestParse (0.01s)"}
				if status == state.JobRunning {
					j.Ended = time.Time{}
				}
			})
			out := s.must("", "verify", "--failed", notes, "--", "go", "test", "-run", "^(TestParse|TestLex)$", "ex.com/m/src")
			if want := "baton: it reruns only what failed in job j1 (TestParse and TestLex) and stands in for it: if it passes, it clears j1; if it fails, it is P0's red gate from then on. " +
				"Job j1 did not run to its end, so the tests it never reached are unchecked, and this rerun does not run them."; !strings.Contains(out, want) {
				t.Errorf("verify --failed says:\n%s\nwant %q", out, want)
			}
			st := s.state()
			if j := st.Job("j2"); j == nil || j.Reruns != "j1" {
				t.Fatalf("j2 %+v", j)
			}
			if status == state.JobRunning && st.Job("j1").Status != state.JobInterrupted {
				t.Errorf("j1 %+v, not replaced", st.Job("j1"))
			}
		})
	}
}
