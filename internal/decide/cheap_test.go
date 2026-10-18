package decide

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red gate's result names the tests that failed, at most maxIDs of them, and, where a rerun of only them
// can stand in for the gate, gives that rerun in its runner's form, queued as `baton verify --failed --
// <command>`: not for a gate that ran out of time, one built from stages (its own --failed), nor a log with
// more failures than baton reads by name.
func TestARedGateNamesWhatFailedAndTheRerunOfThem(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	gate := func(command, status string) state.Job {
		return state.Job{ID: "j4", Phase: "P3", Kind: state.JobVerify, Command: command, Commit: "3f2a9c1b04de55aa", Status: status, Exit: 1,
			Started: t0, Ended: t0.Add(5 * time.Minute)}
	}
	goTest := func(id, pkg string) lane.FailedTest { return lane.FailedTest{Runner: lane.RunGo, ID: id, Scope: pkg} }
	two := []lane.FailedTest{goTest("TestA", "example.com/m/internal/x"), goTest("TestB", "example.com/m/internal/x")}
	var many []lane.FailedTest
	var manyNames []string
	for i := 1; i <= 25; i++ {
		many = append(many, goTest(fmt.Sprintf("Test%02d", i), "example.com/m/internal/x"))
		manyNames = append(manyNames, fmt.Sprintf("Test%02d", i))
	}
	staged := gate("make e2e", state.JobFailed)
	staged.Stages = []lane.Stage{{Name: "unit", Status: lane.StagePassed}, {Name: "e2e", Status: lane.StageFailed, Failures: []string{"TestA"}}}

	const (
		as        = ", as `baton verify --failed -- <that command>`, which clears this gate when it passes.\n"
		ownWay    = ", inside the gate's own way of running it (make, docker compose)"
		narrowFix = "Fix what failed (quick checks in place help), commit, and rerun only what failed, as above: " +
			"a red gate stands until the same command passes, or a rerun of what failed in it that names every test above (`--failed`); " +
			"no other narrower check clears it (to make another command P3's gate instead, verify it with --replace: a decision without the human, and they are told). " +
			"`baton done P3` is refused until then."
		sameFix = "Fix what failed (quick checks in place help), commit, and verify again with the same command"
	)
	for _, c := range []struct {
		name string
		job  state.Job
		log  Log
		want []string
		not  []string
	}{
		{"go tests, run by make", gate("make test", state.JobFailed), Log{Failed: two},
			[]string{"`make test` on 3f2a9c1b04de.\nFailed (2): TestA and TestB.\n" +
				"Rerun only these: `go test -run '^(TestA|TestB)$' example.com/m/internal/x`" + ownWay + as + "\n", narrowFix},
			[]string{sameFix, "--failed --notes"}},
		{"the gate is the runner itself", gate("go test ./...", state.JobFailed), Log{Failed: two},
			[]string{"Rerun only these: `go test -run '^(TestA|TestB)$' example.com/m/internal/x`" + as, narrowFix}, []string{ownWay}},
		{"a package that failed whole", gate("make test", state.JobFailed),
			Log{Failed: []lane.FailedTest{goTest("TestA", "example.com/m/a"), {Runner: lane.RunGo, ID: "example.com/m/b", Scope: "example.com/m/b", Whole: true}}},
			[]string{"Failed (2): TestA and example.com/m/b (failed whole).\n",
				"Rerun only these: `go test -run '^(TestA)$' example.com/m/a && go test example.com/m/b`" + ownWay + as}, nil},
		{"one name in two packages", gate("make test", state.JobFailed), Log{Failed: []lane.FailedTest{goTest("TestA", "example.com/m/a"), goTest("TestA", "example.com/m/b")}},
			[]string{"Failed (2): TestA (example.com/m/a) and TestA (example.com/m/b).\n",
				"Rerun only these: `go test -run '^(TestA)$' example.com/m/a example.com/m/b`" + ownWay + as}, nil},
		{"two runners", gate("make check", state.JobFailed),
			Log{Failed: []lane.FailedTest{goTest("TestA", "example.com/m/a"), {Runner: lane.RunPytest, ID: "tests/test_x.py::test_b[1-2]"}}},
			[]string{"Failed (2): TestA and tests/test_x.py::test_b[1-2].\n",
				"Rerun only these: `go test -run '^(TestA)$' example.com/m/a && pytest 'tests/test_x.py::test_b[1-2]'`" + ownWay + as}, nil},
		{"capped", gate("make test", state.JobFailed), Log{Failed: many},
			[]string{"Failed (25): " + strings.Join(manyNames[:20], ", ") + " and 5 more.\n",
				"Rerun only these: `go test -run '^(" + strings.Join(manyNames, "|") + ")$' example.com/m/internal/x`" + ownWay + as, narrowFix},
			[]string{"Test21,"}},
		{"more than baton reads by name", gate("make test", state.JobFailed), Log{Failed: many, FailedMore: 82},
			[]string{"Failed (107): " + strings.Join(manyNames[:20], ", ") + " and 87 more.\n", sameFix + ", `make test`"},
			[]string{"Rerun only these", narrowFix}},
		{"ids no runner named", gate("./check.sh", state.JobFailed),
			Log{Failed: []lane.FailedTest{{ID: "login-flow"}, {Runner: lane.RunPytest, ID: "tests/test_x.py::test_b"}}},
			[]string{"Failed (2): login-flow and tests/test_x.py::test_b.\n",
				"Rerun only these, by a command that names each of them (for those its test runner named: `pytest tests/test_x.py::test_b`)" + ownWay + as, narrowFix}, nil},
		{"only ids no runner named", gate("./check.sh", state.JobFailed), Log{Failed: []lane.FailedTest{{ID: "login-flow"}}},
			[]string{"Rerun only these, by a command that names each of them" + ownWay + as}, []string{"for those"}},
		{"it ran out of time", gate("make test", state.JobTimeout), Log{Failed: two},
			[]string{"Failed (2): TestA and TestB.\n", "It ran past verify_timeout", sameFix}, []string{"Rerun only these", narrowFix}},
		{"a gate built from stages", staged, Log{Failed: two},
			[]string{"Failed (2): TestA and TestB.\n", "rerun what failed with `baton verify --failed --notes \"…\"`"}, []string{"Rerun only these", narrowFix}},
		{"its environment failed", gate("make e2e", state.JobFailed), Log{Failed: two, Env: &Env{Kind: "a port already in use", Line: "port is already allocated"}},
			[]string{"Failed (2): TestA and TestB.\n", "This looks like the gate's environment"}, []string{"Rerun only these", narrowFix}},
		{"no ids read", gate("make test", state.JobFailed), Log{}, []string{sameFix + ", `make test`"}, []string{"Failed (", "Rerun only these"}},
		{"a pass", gate("make test", state.JobPassed), Log{Failed: two}, []string{"It checked 3f2a9c1b04de"}, []string{"Failed (", "Rerun only these"}},
		{"an await", func() state.Job { j := gate("gh run watch 7", state.JobFailed); j.Kind = state.JobAwait; return j }(), Log{Failed: two},
			[]string{"What it waited on did not succeed"}, []string{"Failed (", "Rerun only these"}},
	} {
		got := JobResult(c.job, c.log, nil, "")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s has %q:\n%s", c.name, w, got)
			}
		}
	}
}

// Every gate's result ends with what its phase's gates have cost so far, and a gate of GateLong or more with
// what it cost the human; a passed gate's result is otherwise as it was, and an await's has neither.
func TestEveryGateResultEndsWithItsCost(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	gate := func(status string, ran time.Duration) state.Job {
		return state.Job{ID: "j4", Phase: "P3", Kind: state.JobVerify, Command: "make test", Commit: "3f2a9c1b04de55aa", Status: status, Exit: 1,
			Started: t0, Ran: t0.Add(2 * time.Minute), Ended: t0.Add(2*time.Minute + ran)}
	}
	cost := state.Cost{Runs: 3, Gates: 36 * time.Minute, Phase: 70 * time.Minute, Of: "make test", Command: 3, CommandTime: 36 * time.Minute}
	const line = "P3 so far: 3 gate runs, 36m of gates in its 1h10m; all of them `make test`."
	short := TimeShort(12 * time.Minute)
	red := []state.Job{{ID: "j2", Phase: "P1", Command: "make lint", Status: state.JobFailed, Exit: 2, Started: t0, Ended: t0.Add(time.Minute)}}
	const (
		checked = "It checked 3f2a9c1b04de: what you committed after it is not in this result."
		theirs  = "Failures in their area are theirs, not this phase's: leave them to it."
	)
	for _, c := range []struct {
		name string
		job  state.Job
		cost state.Cost
		red  []state.Job
		last string // how the result ended before it gave the cost
		end  string // what it adds: "" for nothing
	}{
		{"a pass", gate(state.JobPassed, 4*time.Minute), cost, nil, checked, line},
		{"a long pass", gate(state.JobPassed, 12*time.Minute), cost, nil, checked, line + " " + short},
		{"a long one, its phase's first", gate(state.JobPassed, 12*time.Minute), state.Cost{Runs: 1, Gates: 12 * time.Minute}, nil, checked, short},
		{"a short one, its phase's first", gate(state.JobPassed, 4*time.Minute), state.Cost{Runs: 1, Gates: 4 * time.Minute}, nil, checked, ""},
		{"long only with its lane wait", func() state.Job { j := gate(state.JobPassed, 9*time.Minute); j.Started = t0.Add(-time.Hour); return j }(), cost, nil, checked, line},
		{"red, with others red", gate(state.JobFailed, 12*time.Minute), cost, red, theirs, line + " " + short},
		{"could not run", func() state.Job { j := gate(state.JobLaneError, 0); j.Ran = time.Time{}; j.Ended = t0; return j }(), cost, nil, ".baton-lane script.)", line},
		{"an await", func() state.Job { j := gate(state.JobPassed, 30*time.Minute); j.Kind = state.JobAwait; return j }(), cost, nil, "Carry on.", ""},
	} {
		got := JobResult(c.job, Log{Path: "/x/j4.log", Tail: "ok", Cost: c.cost}, c.red, "")
		want := c.last
		if c.end != "" {
			want += "\n" + c.end
		}
		if !strings.HasSuffix(got, want) || strings.Count(got, "so far:") > 1 || strings.Count(got, "of the human's time") > 1 {
			t.Errorf("%s:\n%s\nwant it to end %q", c.name, got, want)
		}
	}
	// A passed gate's result is as it was but for the cost.
	j := gate(state.JobPassed, 12*time.Minute)
	if got, was := JobResult(j, Log{Path: "/x/j4.log", Tail: "ok", Cost: cost}, nil, ""),
		"[baton] Job j4 (P3) passed after 12m, 2m waiting for a lane: `make test` on 3f2a9c1b04de.\nThe end of its log (/x/j4.log):\n\nok\n\n"+checked; got != was+"\n"+line+" "+short {
		t.Errorf("a pass:\n%s", got)
	}
	if got, want := short, "This gate spends 12m of the human's time; to you it is one tool call."; got != want {
		t.Errorf("TimeShort %q, want %q", got, want)
	}
}
