package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/hostcheck"
	"github.com/ozzyfromspace/baton/internal/lane"
)

// The test binary stands in for a stage's command: `<test binary> stage-child <step>…`, each step one of
// out:<line>, err:<line> (to stderr), part:<text> (no newline), long:<n>:<text> (n x's, then text), env:<name>
// (prints name=value), file:<name> (prints the file the variable names), exit:<code>.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "stage-child" {
		os.Exit(stageChild(os.Args[2:]))
	}
}

func stageChild(steps []string) int {
	for _, step := range steps {
		kind, arg, _ := strings.Cut(step, ":")
		switch kind {
		case "out":
			fmt.Println(arg)
		case "err":
			fmt.Fprintln(os.Stderr, arg)
		case "part":
			fmt.Print(arg)
		case "long":
			n, text, _ := strings.Cut(arg, ":")
			count, _ := strconv.Atoi(n)
			fmt.Println(strings.Repeat("x", count) + text)
		case "env":
			fmt.Printf("%s=%s\n", arg, os.Getenv(arg))
		case "file":
			b, err := os.ReadFile(os.Getenv(arg))
			fmt.Printf("%s holds %q (%v)\n", arg, b, err)
		case "exit":
			code, _ := strconv.Atoi(arg)
			return code
		}
	}
	return 0
}

// stageAt is the time each stage starts in these tests; each reading of the clock is 67 seconds after
// the one before.
var stageAt = time.Date(2026, 10, 17, 14, 2, 11, 0, time.Local)

// runStage runs `baton stage <args…> -- <the test binary> stage-child <steps…>` with env, and returns its exit
// code and output.
func runStage(t *testing.T, env map[string]string, args []string, steps ...string) (int, string, string) {
	t.Helper()
	return runStageIn(t, "", env, args, steps...)
}

// runStageIn is runStage in the directory dir ("": the test's).
func runStageIn(t *testing.T, dir string, env map[string]string, args []string, steps ...string) (int, string, string) {
	t.Helper()
	io, out, errb := testIO("", env)
	io.Dir = dir
	n := 0
	io.Now = func() time.Time {
		n++
		return stageAt.Add(time.Duration(n-1) * 67 * time.Second)
	}
	argv := append(append([]string{"stage"}, args...), "--", os.Args[0], "stage-child")
	code := Main(append(argv, steps...), io)
	return code, out.String(), errb.String()
}

func readStages(t *testing.T, path string) []lane.Stage {
	t.Helper()
	got, err := lane.ReadStages(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A stage prints where it starts, passes its command's output and errors through a line at a time, and says
// how it ended, each of its own lines on a line of its own; in a job it records the stage.
func TestAStageMarksItsCommandAndPassesItsOutput(t *testing.T) {
	stages := filepath.Join(t.TempDir(), "jobs", "P3.j81.stages")
	env := map[string]string{lane.EnvJob: "P3.j81", lane.EnvStages: stages, "BATON_COMMIT": "72b7740aaa"}
	code, out, errOut := runStage(t, env, []string{"unit"}, "out:hello", "err:oops", "part:no newline")
	want := "▶ unit 14:02:11\n::baton stage unit start\nhello\noops\nno newline\n" +
		"■ unit passed in 1m07s\n::baton stage unit passed exit=0 seconds=67\n"
	if code != 0 || out != want || errOut != "" {
		t.Errorf("code %d, output\n%s\nwant\n%s\nerrors: %s", code, out, want, errOut)
	}
	got := readStages(t, stages)
	if len(got) != 1 {
		t.Fatalf("recorded %+v, want one stage", got)
	}
	s := got[0]
	if s.Name != "unit" || s.Status != lane.StagePassed || s.Exit != 0 || s.Commit != "72b7740aaa" || s.Failures != nil ||
		!s.Started.Equal(stageAt) || s.Took() != 67*time.Second {
		t.Errorf("recorded %+v", s)
	}
}

// A failed stage records what failed: the command's ::baton fail lines and its runner's own failure lines,
// each once, in order; a line read in pieces is not read for one. A failed stage that records none says why
// its rerun runs whole (Unnarrowed).
func TestAFailedStageRecordsWhatFailed(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		steps      []string
		exit       int
		failed     []string
		runner     string
		unnarrowed string
	}{
		{"go", []string{"unit", "--runner", "go"}, []string{"out:=== RUN   TestA", "out:    --- FAIL: TestA/sub (0.00s)",
			"out:--- FAIL: TestA (0.00s)", "out:--- FAIL: TestB (0.01s)", "out:::baton fail custom one", "out:FAIL", "exit:1"},
			1, []string{"TestA", "TestB", "custom one"}, "", ""},
		{"pytest", []string{"unit", "--runner=pytest"}, []string{"out:FAILED tests/test_x.py::test_a[a b] - AssertionError: x",
			"out:FAILED tests/test_x.py::TestC::test_m", "out:ERROR tests/test_y.py - ImportError", "exit:2"},
			2, []string{"tests/test_x.py::test_a[a b]", "tests/test_x.py::TestC::test_m"}, "pytest", ""},
		{"vitest", []string{"unit", "--runner", "vitest"}, []string{"out: FAIL  src/a.test.ts > Suite > adds", "out: FAIL  src/a.test.ts > Suite > adds", "exit:1"},
			1, []string{"src/a.test.ts > Suite > adds"}, "vitest", ""},
		{"a package that failed whole runs whole", []string{"unit", "--runner", "go"}, []string{"out:--- FAIL: TestA (0.00s)",
			"out:FAIL\tex.com/a\t0.1s", "out:FAIL\tex.com/b [build failed]", "exit:1"}, 1, nil, "", lane.UnnarrowedWhole},
		{"with no runner, fail lines come first", []string{"unit"}, []string{"out:--- FAIL: TestA (0.00s)", "err:  ::baton fail TestZ", "exit:1"},
			1, []string{"TestZ"}, "", ""},
		{"with no runner, a runner's own lines", []string{"e2e"}, []string{"out:  ✘  3 [chromium] › test/a.spec.ts:36:3 › logs in (1.2s)",
			"out:  1) [chromium] › test/a.spec.ts:36:3 › logs in ───", "out:  2) [chromium] › test/b.spec.ts:116:1 › searches ───", "exit:1"},
			1, []string{"test/a.spec.ts:36", "test/b.spec.ts:116"}, "playwright", ""},
		{"with no runner, go's", []string{"unit"}, []string{"out:--- FAIL: TestA (0.00s)", "out:FAIL", "exit:1"}, 1, []string{"TestA"}, "go", ""},
		{"two runners, run whole", []string{"unit"}, []string{"out:--- FAIL: TestA (0.00s)", "out:FAILED tests/test_x.py::test_a - x", "exit:1"},
			1, nil, "", lane.UnnarrowedRunners},
		{"fail lines beside a runner's narrow by it", []string{"unit", "--runner", "go"}, []string{"out:--- FAIL: TestA (0.00s)", "out:::baton fail lint:x", "exit:1"},
			1, []string{"TestA", "lint:x"}, "", ""},
		{"a fail line mid-line", []string{"unit"}, []string{"long:70000:::baton fail hidden", "exit:1"}, 1, nil, "", lane.UnnarrowedNoIDs},
		{"no failure named", []string{"unit"}, []string{"out:Error: something broke", "exit:1"}, 1, nil, "", lane.UnnarrowedNoIDs},
		{"a passed stage records none", []string{"unit"}, []string{"out:::baton fail flaky"}, 0, nil, "", ""},
		{"over the limit, none", []string{"unit", "--runner", "go"}, append(failLines(lane.MaxFailures+1), "exit:1"), 1, nil, "", lane.UnnarrowedMany},
		{"at the limit, all", []string{"unit", "--runner", "go"}, append(failLines(lane.MaxFailures), "exit:1"), 1, testNames(lane.MaxFailures), "go", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stages := filepath.Join(t.TempDir(), "P3.j81.stages")
			code, out, _ := runStage(t, map[string]string{lane.EnvStages: stages}, c.args, c.steps...)
			if code != c.exit {
				t.Errorf("exit %d, want %d", code, c.exit)
			}
			got := readStages(t, stages)
			if len(got) != 1 || !reflect.DeepEqual(got[0].Failures, c.failed) || got[0].Runner != c.runner || got[0].Unnarrowed != c.unnarrowed {
				t.Errorf("recorded %+v, want failures %q of runner %q, unnarrowed %q", got, c.failed, c.runner, c.unnarrowed)
			}
			status := lane.StagePassed
			if c.exit != 0 {
				status = lane.StageFailed
			}
			if end := lane.EndMarker(c.args[0], status, c.exit, 67*time.Second); !strings.HasSuffix(out, "\n"+end+"\n") {
				t.Errorf("output does not end with %q:\n%s", end, tail(out))
			}
		})
	}
}

func failLines(n int) []string {
	var lines []string
	for _, name := range testNames(n) {
		lines = append(lines, "out:--- FAIL: "+name+" (0.00s)")
	}
	return lines
}

func testNames(n int) []string {
	var names []string
	for i := range n {
		names = append(names, fmt.Sprintf("Test%d", i))
	}
	return names
}

func tail(s string) string {
	if len(s) > 400 {
		return "…" + s[len(s)-400:]
	}
	return s
}

// A gate stops at its first failed stage, across the separate commands of its script: a later stage prints
// why, is recorded skipped and exits 1, without running its command. A stage run with --keep-going lets the
// stages after it run. In a QA phase's gate every stage keeps going, so one run collects every red, but one
// run with --halts, which the later stages need, stops them; elsewhere --halts is how a stage is anyway.
func TestAGateStopsAtItsFirstFailedStage(t *testing.T) {
	type step struct {
		args  []string
		steps []string
		exit  int
		ran   bool
	}
	cases := []struct {
		name  string
		qa    bool
		steps []step
		want  []string // each stage recorded, as name:status:keep-going
		why   string   // what a stage skipped says
	}{
		{"stops", false, []step{
			{[]string{"lint"}, []string{"out:ran", "exit:1"}, 1, true},
			{[]string{"unit"}, []string{"out:ran"}, 1, false},
			{[]string{"e2e", "--keep-going"}, []string{"out:ran"}, 1, false},
		}, []string{"lint:failed:false", "unit:skipped:false", "e2e:skipped:true"},
			"skipped: stage lint failed (exit 1) earlier in this gate, and a gate stops at its first failure"},
		{"keeps going", false, []step{
			{[]string{"lint", "--keep-going"}, []string{"out:ran", "exit:1"}, 1, true},
			{[]string{"unit"}, []string{"out:ran"}, 0, true},
			{[]string{"e2e"}, []string{"out:ran", "exit:3"}, 3, true},
			{[]string{"perf"}, []string{"out:ran"}, 1, false},
		}, []string{"lint:failed:true", "unit:passed:false", "e2e:failed:false", "perf:skipped:false"},
			"skipped: stage e2e failed (exit 3) earlier in this gate, and a gate stops at its first failure"},
		{"--halts outside a QA phase stops as any stage does", false, []step{
			{[]string{"build", "--halts"}, []string{"out:ran", "exit:2"}, 2, true},
			{[]string{"unit"}, []string{"out:ran"}, 1, false},
		}, []string{"build:failed:false", "unit:skipped:false"}, "skipped: stage build failed (exit 2) earlier in this gate, and a gate stops"},
		{"a QA phase runs every stage", true, []step{
			{[]string{"lint"}, []string{"out:ran", "exit:1"}, 1, true},
			{[]string{"unit"}, []string{"out:ran", "exit:1"}, 1, true},
			{[]string{"e2e", "--keep-going"}, []string{"out:ran"}, 0, true},
			{[]string{"perf"}, []string{"out:ran", "exit:3"}, 3, true},
		}, []string{"lint:failed:true", "unit:failed:true", "e2e:passed:true", "perf:failed:true"}, ""},
		{"a QA phase stops at a stage that halts", true, []step{
			{[]string{"lint"}, []string{"out:ran", "exit:1"}, 1, true},
			{[]string{"build", "--halts"}, []string{"out:ran", "exit:2"}, 2, true},
			{[]string{"unit"}, []string{"out:ran"}, 1, false},
			{[]string{"e2e", "--keep-going"}, []string{"out:ran"}, 1, false},
		}, []string{"lint:failed:true", "build:failed:false", "unit:skipped:true", "e2e:skipped:true"},
			"skipped: stage build failed (exit 2) earlier in this gate, and the stages after it need it (baton stage --halts)"},
		{"a QA phase goes on past a stage that halts and passed", true, []step{
			{[]string{"build", "--halts"}, []string{"out:ran"}, 0, true},
			{[]string{"unit"}, []string{"out:ran", "exit:1"}, 1, true},
			{[]string{"e2e"}, []string{"out:ran"}, 0, true},
		}, []string{"build:passed:false", "unit:failed:true", "e2e:passed:true"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stages := filepath.Join(t.TempDir(), "jobs", "j4.stages")
			env := map[string]string{lane.EnvStages: stages}
			if c.qa {
				env[lane.EnvQAPhase] = "1"
			}
			for _, s := range c.steps {
				code, out, _ := runStage(t, env, s.args, s.steps...)
				if code != s.exit || strings.Contains(out, "\nran\n") != s.ran {
					t.Errorf("%v: exit %d (want %d), output\n%s", s.args, code, s.exit, out)
				}
				if !s.ran && !strings.Contains(out, "■ "+s.args[0]+" "+c.why) {
					t.Errorf("%v does not say why it was skipped (%q):\n%s", s.args, c.why, out)
				}
				if !s.ran && !strings.HasSuffix(out, lane.EndMarker(s.args[0], lane.StageSkipped, 1, 0)+"\n") {
					t.Errorf("%v writes no skipped marker:\n%s", s.args, out)
				}
			}
			var got []string
			for _, s := range readStages(t, stages) {
				got = append(got, fmt.Sprintf("%s:%s:%v", s.Name, s.Status, s.KeepGoing))
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("recorded %v, want %v", got, c.want)
			}
		})
	}
}

// A failed stage whose command writes its output to a file of its own (--log, relative to where the stage
// runs) reads what failed from that file too, as from its output: its runners' lines, and its fail lines
// first. A file that is not there is said, and the stage reads its output alone; a stage that passed reads
// no file.
func TestAStageReadsWhatFailedFromItsLog(t *testing.T) {
	side := strings.Join([]string{"Running 390 tests using 4 workers", "", "  2 failed",
		"    [chromium] › tests/a.spec.ts:12:7 › home › opens ───", "    tests/b.spec.ts:3:1 › b ───", "  1 flaky",
		"    tests/c.spec.ts:8:1 › c ───", "  387 passed (1.3h)"}, "\n") + "\n"
	cases := []struct {
		name    string
		args    []string
		log     string // what the side log holds; "-" for no file
		steps   []string
		failed  []string
		runner  string
		missing bool // the stage says it found no log
	}{
		{"a red stage", []string{"e2e", "--log", "results/e2e.log"}, side, []string{"out:see results/e2e.log", "exit:1"},
			[]string{"tests/a.spec.ts:12", "tests/b.spec.ts:3"}, lane.RunPlaywright, false},
		{"with its output's own", []string{"e2e", "--log=results/e2e.log"}, side, []string{"out:  ✘  1 tests/z.spec.ts:1:1 › z (1s)", "exit:1"},
			[]string{"tests/z.spec.ts:1", "tests/a.spec.ts:12", "tests/b.spec.ts:3"}, lane.RunPlaywright, false},
		{"fail lines in the file", []string{"e2e", "--log", "results/e2e.log"}, side + "::baton fail checkout\n", []string{"exit:1"},
			[]string{"checkout"}, "", false},
		{"a missing file", []string{"unit", "--log", "results/e2e.log"}, "-", []string{"out:--- FAIL: TestA (0.00s)", "exit:1"},
			[]string{"TestA"}, lane.RunGo, true},
		{"a passed stage reads none", []string{"e2e", "--log", "results/e2e.log"}, "-", nil, nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.log != "-" {
				os.MkdirAll(filepath.Join(dir, "results"), 0o755)
				if err := os.WriteFile(filepath.Join(dir, "results", "e2e.log"), []byte(c.log), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			stages := filepath.Join(dir, "jobs", "j9.stages")
			code, out, errOut := runStageIn(t, dir, map[string]string{lane.EnvStages: stages}, c.args, c.steps...)
			got := readStages(t, stages)
			if len(got) != 1 || !reflect.DeepEqual(got[0].Failures, c.failed) || got[0].Runner != c.runner {
				t.Errorf("exit %d, recorded %+v, want failures %q of runner %q\n%s%s", code, got, c.failed, c.runner, out, errOut)
			}
			said := "baton: stage " + c.args[0] + "'s log results/e2e.log does not exist, so what failed in it is read from its output alone\n"
			if strings.Contains(out, said) != c.missing || strings.Contains(out, "in it is read from its output alone") != c.missing {
				t.Errorf("says it found no log: %v, want %v:\n%s", !c.missing, c.missing, out)
			}
			if c.missing && !strings.HasSuffix(out, said+"■ unit failed (exit 1) in 1m07s\n::baton stage unit failed exit=1 seconds=67\n") {
				t.Errorf("the missing log is not said before the stage's end:\n%s", out)
			}
			if strings.Contains(out, "387 passed") {
				t.Errorf("the side log was copied into the stage's output:\n%s", out)
			}
		})
	}
	// An absolute path is the file itself, wherever the stage runs.
	dir := t.TempDir()
	log := filepath.Join(dir, "side.log")
	os.WriteFile(log, []byte("--- FAIL: TestQ (0.00s)\n"), 0o644)
	stages := filepath.Join(dir, "j10.stages")
	if code, out, _ := runStageIn(t, t.TempDir(), map[string]string{lane.EnvStages: stages}, []string{"unit", "--log", log}, "exit:1"); code != 1 {
		t.Errorf("exit %d\n%s", code, out)
	}
	if got := readStages(t, stages); len(got) != 1 || !reflect.DeepEqual(got[0].Failures, []string{"TestQ"}) {
		t.Errorf("recorded %+v", got)
	}
}

// On a rerun of a red gate, a stage that passed there is carried, with the commit it passed on; one that
// failed with ids runs only those, its runner's way and through BATON_FAILED and BATON_FAILED_RE; any
// other runs whole.
func TestARerunCarriesNarrowsOrRunsEachStage(t *testing.T) {
	dir := t.TempDir()
	red := filepath.Join(dir, "jobs", "P3.j80.stages")
	for _, s := range []lane.Stage{
		{Name: "lint", Status: lane.StagePassed, Commit: "72b7740aaaaa"},
		{Name: "build", Status: lane.StageCarried, Commit: "5d1e2aa0000", From: "P3.j70"},
		{Name: "unit", Status: lane.StageFailed, Exit: 1, Commit: "72b7740aaaaa", Failures: []string{"TestA", "TestB"}},
		{Name: "py", Status: lane.StageFailed, Exit: 1, Failures: []string{"tests/x.py::test_a[1]", "tests/x.py::test_a[2]", "tests/y.py::TestC::test_m"}},
		{Name: "custom", Status: lane.StageFailed, Exit: 1, Failures: []string{"one", "two+"}},
		{Name: "e2e", Status: lane.StageFailed, Exit: 1},
		{Name: "pw", Status: lane.StageFailed, Exit: 1, Failures: []string{"test/a.spec.ts:36", "test/b.spec.ts:116"}, Runner: lane.RunPlaywright},
		{Name: "vt", Status: lane.StageFailed, Exit: 1, Failures: []string{"src/a.test.ts > Suite > adds one", "src/b.test.ts > b"}, Runner: lane.RunVitest},
		{Name: "gounit", Status: lane.StageFailed, Exit: 1, Failures: []string{"TestC"}, Runner: lane.RunGo},
	} {
		if err := lane.AppendStage(red, s); err != nil {
			t.Fatal(err)
		}
	}
	stages := filepath.Join(dir, "jobs", "P3.j81.stages")
	env := map[string]string{lane.EnvStages: stages, lane.EnvRerun: red, "BATON_COMMIT": "9c0ffee1234", "GOFLAGS": "-count=1"}
	show := []string{"env:GOFLAGS", "env:PYTEST_ADDOPTS", "env:" + lane.EnvFailedRE, "env:" + lane.EnvFailedArgs, "file:" + lane.EnvFailed, "out:ran"}

	cases := []struct {
		args []string
		has  []string
		not  []string
	}{
		{[]string{"lint"}, []string{"■ lint passed in P3.j80 on 72b7740, so this rerun does not run it again\n::baton stage lint carried exit=0 seconds=0\n"},
			[]string{"\nran\n", "▶ lint"}},
		{[]string{"build"}, []string{"■ build passed in P3.j70 on 5d1e2aa,"}, []string{"\nran\n"}},
		{[]string{"unit", "--runner", "go"}, []string{"▶ unit 14:02:11 — only the 2 that failed in P3.j80: TestA, TestB\n",
			"GOFLAGS=-count=1 -run=^(TestA|TestB)$\n", "BATON_FAILED_RE=^(TestA|TestB)$\n", `BATON_FAILED holds "TestA\nTestB\n"`, "\nran\n"},
			[]string{"PYTEST_ADDOPTS=-k", "BATON_FAILED_ARGS=-"}},
		{[]string{"py", "--runner", "pytest"}, []string{`PYTEST_ADDOPTS=-k "test_a or test_m"` + "\n"}, []string{"-run="}},
		{[]string{"custom"}, []string{`BATON_FAILED_RE=^(one|two\+)$` + "\n", `BATON_FAILED holds "one\ntwo+\n"`, "\nran\n"}, []string{"-run=", "PYTEST_ADDOPTS=-k"}},
		{[]string{"e2e", "--runner", "go"}, []string{"▶ e2e 14:02:11\n", "BATON_FAILED_RE=\n", "\nran\n"}, []string{"-run="}},
		{[]string{"docs"}, []string{"▶ docs 14:02:11\n", "BATON_FAILED_RE=\n", "\nran\n"}, nil},
		// The runner that named a stage's failures narrows its rerun, --runner or not: by its variable where
		// it reads one, and by its own arguments for a command that passes them on.
		{[]string{"pw"}, []string{"BATON_FAILED_ARGS=test/a.spec.ts:36 test/b.spec.ts:116\n", "\nran\n"}, []string{"-run=", "PYTEST_ADDOPTS=-k"}},
		{[]string{"vt"}, []string{`BATON_FAILED_ARGS=src/a.test.ts src/b.test.ts -t adds\s+one|b` + "\n"}, []string{"-run="}},
		{[]string{"gounit"}, []string{"GOFLAGS=-count=1 -run=^(TestC)$\n", "BATON_FAILED_ARGS=-run=^(TestC)$\n"}, nil},
	}
	for _, c := range cases {
		code, out, errOut := runStage(t, env, c.args, show...)
		if code != 0 {
			t.Errorf("%v: exit %d\n%s%s", c.args, code, out, errOut)
		}
		for _, want := range c.has {
			if !strings.Contains(out, want) {
				t.Errorf("%v: output lacks %q:\n%s", c.args, want, out)
			}
		}
		for _, not := range c.not {
			if strings.Contains(out, not) {
				t.Errorf("%v: output has %q:\n%s", c.args, not, out)
			}
		}
	}
	var got []string
	for _, s := range readStages(t, stages) {
		got = append(got, fmt.Sprintf("%s:%s:%s:%s", s.Name, s.Status, s.Commit, s.From))
	}
	want := []string{"lint:carried:72b7740aaaaa:P3.j80", "build:carried:5d1e2aa0000:P3.j70", "unit:passed:9c0ffee1234:",
		"py:passed:9c0ffee1234:", "custom:passed:9c0ffee1234:", "e2e:passed:9c0ffee1234:", "docs:passed:9c0ffee1234:",
		"pw:passed:9c0ffee1234:", "vt:passed:9c0ffee1234:", "gounit:passed:9c0ffee1234:"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "baton-failed-*"))
	for _, f := range left {
		if b, _ := os.ReadFile(f); strings.Contains(string(b), "two+") {
			t.Errorf("the file of ids %s outlived its stage", f)
		}
	}
}

// The record a rerun runs from (verify --failed) is a file of the rerun's own, so each of its stages names
// the job its record comes from, and that is the job a stage is carried from, or narrowed to what failed in.
func TestARerunsRecordNamesWhereEachStageCameFrom(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "jobs", "j81.rerun")
	for _, s := range []lane.Stage{
		{Name: "lint", Status: lane.StagePassed, Commit: "72b7740aaaaa", From: "j80"},
		{Name: "unit", Status: lane.StageFailed, Exit: 1, Commit: "5d1e2aa0000", From: "j79", Failures: []string{"TestA"}},
	} {
		if err := lane.AppendStage(record, s); err != nil {
			t.Fatal(err)
		}
	}
	stages := filepath.Join(dir, "jobs", "j81.stages")
	env := map[string]string{lane.EnvStages: stages, lane.EnvRerun: record, "BATON_COMMIT": "9c0ffee1234"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"lint"}, "■ lint passed in j80 on 72b7740, so this rerun does not run it again\n"},
		{[]string{"unit", "--runner", "go"}, "— only the 1 that failed in j79: TestA\n"},
	} {
		if code, out, errOut := runStage(t, env, c.args, "out:ran"); code != 0 || !strings.Contains(out, c.want) {
			t.Errorf("%v: exit %d, output lacks %q:\n%s%s", c.args, code, c.want, out, errOut)
		}
	}
	if got := readStages(t, stages); len(got) != 2 || got[0].From != "j80" || got[0].Status != lane.StageCarried || got[1].Status != lane.StagePassed {
		t.Errorf("recorded %+v", got)
	}
}

// A stage exits with its command's code; one whose command cannot start fails with 127; a call baton cannot
// read is a usage error, 2, and runs nothing.
func TestStageExitCodes(t *testing.T) {
	stages := filepath.Join(t.TempDir(), "jobs", "j9.stages")
	env := map[string]string{lane.EnvStages: stages}
	for _, c := range []struct {
		steps []string
		exit  int
	}{{nil, 0}, {[]string{"exit:3"}, 3}, {[]string{"exit:0"}, 0}} {
		os.Remove(stages) // each its own gate, so a failure stops nothing after it
		if code, _, _ := runStage(t, env, []string{"unit"}, c.steps...); code != c.exit {
			t.Errorf("%v: exit %d, want %d", c.steps, code, c.exit)
		}
	}

	os.Remove(stages)
	io, out, _ := testIO("", env)
	if code := Main([]string{"stage", "unit", "--", filepath.Join(t.TempDir(), "no-such-command")}, io); code != 127 {
		t.Errorf("a command that cannot start: exit %d, want 127", code)
	}
	if !strings.Contains(out.String(), "baton: stage unit could not run its command") ||
		!strings.HasSuffix(out.String(), "::baton stage unit failed exit=127 seconds=0\n") {
		t.Errorf("output:\n%s", out)
	}
	if got := readStages(t, stages); len(got) != 1 || got[0].Status != lane.StageFailed || got[0].Exit != 127 {
		t.Errorf("recorded %+v", got)
	}

	child := []string{"--", os.Args[0], "stage-child", "out:ran"}
	for _, args := range [][]string{
		nil,
		{"unit"},
		{"unit", "--"},
		{"--", os.Args[0]},
		{"unit", "extra", "--", os.Args[0]},
		append([]string{"a b"}, child...),
		append([]string{strings.Repeat("x", lane.MaxStageName+1)}, child...),
		append([]string{"unit", "--runner", "mocha"}, child...),
		append([]string{"unit", "--runner"}, "--", os.Args[0]),
		append([]string{"unit", "--fast"}, child...),
		append([]string{"unit", "--keep-going", "--halts"}, child...),
		append([]string{"unit", "--halts=yes"}, child...),
		append([]string{"unit", "--log="}, child...),
		append([]string{"unit", "--log", " "}, child...),
		{"unit", "--log", "--", os.Args[0]},
	} {
		io, out, errb := testIO("", env)
		if code := Main(append([]string{"stage"}, args...), io); code != 2 || out.Len() > 0 || errb.Len() == 0 {
			t.Errorf("%q: exit %d, output %q, errors %q; want a usage error", args, code, out, errb)
		}
	}
}

// Outside a job, and in a session no baton host is above, a stage prints and runs, and records nothing.
func TestAStageRunsUnhosted(t *testing.T) {
	for _, standing := range []hostcheck.Status{hostcheck.Plain, hostcheck.Inherited} {
		dir := t.TempDir()
		io, out, errb := testIO("", nil)
		io.Dir = dir
		io.Hosted = func() hostcheck.Status { return standing }
		code := Main([]string{"stage", "unit", "--", os.Args[0], "stage-child", "out:ran", "exit:4"}, io)
		if code != 4 || !strings.Contains(out.String(), "::baton stage unit start\nran\n■ unit failed (exit 4) in 0s\n") || errb.Len() > 0 {
			t.Errorf("%v: exit %d, output\n%s\nerrors: %s", standing, code, out, errb)
		}
		if entries, _ := os.ReadDir(dir); len(entries) > 0 {
			t.Errorf("%v: it wrote %v", standing, entries)
		}
	}
}
