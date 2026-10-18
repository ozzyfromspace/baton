package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What the human is told of a gate queued over baton's question, with a reason and without.
func TestARepeatsLines(t *testing.T) {
	m := time.Minute
	c := state.Cost{Runs: 9, Gates: 90 * m, Phase: 2 * time.Hour, Of: "make test-degraded", Command: 7, CommandTime: 80 * m}
	other := state.Cost{Runs: 2, Gates: 5 * m, Of: "make test-degraded"} // its last run was another phase's
	for _, x := range []struct{ got, want string }{
		{RepeatSaid("P53", 12*m, c, "the fix touches the shared pool"), "P53 reruns `make test-degraded` (12m, run 8): the fix touches the shared pool"},
		{RepeatSaid("P53", 12*m, other, "a new commit"), "P53 reruns `make test-degraded` (12m): a new commit"},
		{RepeatUnexplained("P53", 12*m, c), "P53 reran `make test-degraded` (12m, run 8, 1h20m so far) without saying why"},
		{RepeatUnexplained("P53", 12*m, other), "P53 reran `make test-degraded` (12m) without saying why"},
		{GateAnswered("P3", "sh /tmp/g.sh", []string{GateOutside, GateUnstaged, GateBeforeQA, GateQAAsk}, "kept outside on purpose"),
			"P3 runs `sh /tmp/g.sh` over baton's question (it runs a script in no commit; it is not built from stages; a full check before QA): kept outside on purpose"},
		{HumanTold("P3 reran `x` (12m) without saying why"), "baton told the human: P3 reran `x` (12m) without saying why."},
		{GateLead("P53", 12*m, c), "This gate spends 12m of the human's time; to you it is one tool call. " +
			"P53 so far: 9 gate runs, 1h30m of gates in its 2h00m; `make test-degraded` 7 of them, 1h20m."},
		{GateLead("P53", 0, c), "P53 so far: 9 gate runs, 1h30m of gates in its 2h00m; `make test-degraded` 7 of them, 1h20m."},
		{GateLead("P53", 12*m, state.Cost{Runs: 1}), "This gate spends 12m of the human's time; to you it is one tool call."},
		{GateLead("P53", 0, state.Cost{Runs: 1}), ""},
	} {
		if x.got != x.want {
			t.Errorf("got\n%s\nwant\n%s", x.got, x.want)
		}
	}
}

// The refusal leads with what the gate costs when baton knows it, and ends by asking for --why, never for
// the same command again.
func TestGateQuestionedAsksWhy(t *testing.T) {
	const end = "Queue the narrower check if one proves the change. To run the whole gate anyway, queue it again with `--why \"…\"`: baton tells the human your reason."
	for _, c := range []struct{ lead, want string }{
		{"This gate spends 12m of the human's time; to you it is one tool call.",
			"not queued, this once. This gate spends 12m of the human's time; to you it is one tool call.\nbaton questions `make e2e` before it runs:\n- q1\n- q2\n" + end},
		{"", "not queued, this once — baton questions `make e2e` before it runs, as a long gate spends the human's time:\n- q1\n- q2\n" + end},
	} {
		if got := GateQuestioned("make e2e", c.lead, []string{"q1", "q2"}); got != c.want {
			t.Errorf("got\n%s\nwant\n%s", got, c.want)
		}
	}
}

// A red repeat names what failed and the narrower rerun in its runner's own command; a gate built from
// stages keeps --failed alone, and ids no command can name, or more than are listed, are rerun by name.
func TestARedRepeatNamesWhatFailed(t *testing.T) {
	m := time.Minute
	prev := state.Job{ID: "j7", Command: "make test", Commit: "abcdef0123", Status: state.JobFailed}
	goIDs := []lane.FailedTest{{Runner: lane.RunGo, ID: "TestParse", Scope: "ex.com/m/src"}, {Runner: lane.RunGo, ID: "TestLex", Scope: "ex.com/m/src"}}
	const narrow = "only what failed in j7 (TestParse, TestLex): `baton verify --failed --notes \"…\" -- go test -run '^(TestParse|TestLex)$' ex.com/m/src`, and a pass clears the red gate"
	for _, c := range []struct {
		name string
		r    Repeat
		want []string
		not  []string
	}{
		{"same code", Repeat{Prev: prev, Took: 12 * m, Same: true, Failures: goIDs},
			[]string{"to learn what its result already says: rerun " + narrow + "."}, nil},
		{"changed", Repeat{Prev: prev, Took: 12 * m, Changed: []string{"src/a.go"}, Failures: goIDs},
			[]string{"Does a narrower check prove the change: the tests of that file, or " + narrow + "?"}, nil},
		{"staged", Repeat{Prev: prev, Took: 12 * m, Same: true, Failed: true, Failures: goIDs},
			[]string{"`baton verify --failed --notes \"…\"` reruns only what failed, carrying the stages that passed."}, []string{"TestParse"}},
		{"a fail line's id", Repeat{Prev: prev, Took: 12 * m, Same: true, Failures: append(goIDs[:1:1], lane.FailedTest{ID: "lint:unused"})},
			[]string{"rerun only what failed in j7 (TestParse, lint:unused), by name."}, []string{"-- go test"}},
		{"more than listed", Repeat{Prev: prev, Took: 12 * m, Same: true, Failures: goIDs, MoreFailures: 30},
			[]string{"rerun only what failed in j7 (TestParse, TestLex and 30 more), by name."}, []string{"-- go test"}},
		{"none read", Repeat{Prev: prev, Took: 12 * m, Same: true},
			[]string{"rerun only what failed, by name (its result lists the failures), and the gate once the fix is in."}, nil},
	} {
		got := c.r.Question("P3")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: asked\n%s\nwant %q", c.name, got, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: asked\n%s\nwith %q", c.name, got, n)
			}
		}
	}
}

// The result of a repeat queued again with no reason opens with what the human was told; any other result
// is as it was.
func TestAnUnexplainedRepeatsResultOpensWithIt(t *testing.T) {
	j := state.Job{ID: "j8", Phase: "P53", Kind: state.JobVerify, Command: "make test", Commit: "abcdef0123", Status: state.JobPassed}
	plain := JobResult(j, Log{}, nil, "")
	if !strings.HasPrefix(plain, "[baton] Job j8 (P53) ") {
		t.Errorf("result:\n%s", plain)
	}
	j.Unexplained = "P53 reran `make test` (12m, run 8, 1h20m so far) without saying why"
	want := "[baton] P53 reran `make test` (12m, run 8, 1h20m so far) without saying why; the human was told. " +
		"A rerun queued with --why says what it can show that the last could not.\n"
	if got := JobResult(j, Log{}, nil, ""); got != want+plain {
		t.Errorf("result:\n%s\nwant\n%s", got, want+plain)
	}
}
