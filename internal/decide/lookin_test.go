package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Each look-in says what baton saw and what the model can do, and asks nothing: a failing gate names its
// line, its stage, how long in, that it runs on, how to act now or why to let it finish, and, run in the
// lead's checkout, the landings it holds; a slow gate its time, its history and its log; one near the
// timeout what happens at it; held landings which and since when; a conflicting file why it serializes the
// phases and what to do about it.
func TestLookInWords(t *testing.T) {
	gate := state.Job{ID: "j3", Phase: "P3", Kind: state.JobVerify, Command: "make e2e"}
	here := gate
	here.Here = true
	await := gate
	await.Kind, await.Command = state.JobAwait, "gh run watch 42"
	failing := state.Failing{Line: "--- FAIL: TestLogin (0.20s)", Stage: "e2e"}
	now := time.Date(2026, 10, 17, 14, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		name     string
		got      string
		want, no []string
	}{
		{"first failure", FirstFailure(gate, failing, 8*time.Minute, []string{"P5"}, 0),
			[]string{"[baton] Job j3 (P3) is failing as it runs: `make e2e` printed this 8m in, in its stage e2e:\n\n    --- FAIL: TestLogin (0.20s)\n\n",
				"The gate runs on", "`baton stop j3`", "`baton verify --failed`", "let it finish to collect every failure"},
			[]string{"holds landings", "?"}},
		{"first failure, no stage", FirstFailure(gate, state.Failing{Line: "FAIL"}, 30*time.Second, nil, 0),
			[]string{"printed this 30s in:\n\n    FAIL\n\n"}, []string{"in its stage"}},
		{"first failure here", FirstFailure(here, failing, 8*time.Minute, []string{"P5", "P7"}, 1),
			[]string{"While it runs in the working tree, it holds landings: P5, P7 and 1 phase of another baton session wait to land"}, nil},
		{"first failure here, nothing held", FirstFailure(here, failing, 8*time.Minute, nil, 0), nil, []string{"holds landings"}},
		{"an await's failure", FirstFailure(await, failing, 3*time.Minute, nil, 0),
			[]string{"[baton] Job j3 (P3), waiting on `gh run watch 42`, printed a failure 3m in", "`baton stop j3`"}, []string{"--failed"}},
		{"said", FirstFailureSaid(gate, failing, 8*time.Minute), []string{"baton: job j3 (`make e2e`, P3) printed a failure 8m in, and runs on → told Claude"}, nil},
		{"slow", GateSlow(gate, 47*time.Minute, []time.Duration{22 * time.Minute, 31 * time.Minute}, 12*time.Minute, "/l/j3.log", 3*time.Hour),
			[]string{"[baton] Job j3 (P3, `make e2e`) has run 47m, well past its slowest earlier pass: earlier passing runs of `make e2e` took 22m and 31m.",
				"Its log (/l/j3.log) has not grown for 12m.", "`baton stop j3`", "At 3h00m (verify_timeout) baton stops it"}, nil},
		{"slow, its log growing", GateSlow(gate, 47*time.Minute, []time.Duration{31 * time.Minute}, 5*time.Second, "/l/j3.log", 0),
			[]string{"took 31m.", "Its log (/l/j3.log) is still growing."}, []string{"verify_timeout"}},
		{"slow, its log unread", GateSlow(gate, 47*time.Minute, []time.Duration{31 * time.Minute}, -1, "/l/j3.log", 0), []string{"Its log: /l/j3.log."}, nil},
		{"slow said", GateSlowSaid(gate, 47*time.Minute, 31*time.Minute), []string{"baton: job j3 (`make e2e`, P3) has run 47m, past its slowest earlier pass (31m)"}, nil},
		{"near the timeout", GateNearTimeout(gate, 2*time.Hour+24*time.Minute, 3*time.Hour, 0, "/l/j3.log"),
			[]string{"has run 2h24m of the 3h00m a job may run (verify_timeout): at 3h00m baton stops it, and that counts as a failure", "`baton stop j3`"}, nil},
		{"near the timeout said", GateNearTimeoutSaid(gate, 2*time.Hour+24*time.Minute, 3*time.Hour), []string{"has run 2h24m of the 3h00m it may run"}, nil},
		{"landings held", LandingsWait(here, 41*time.Minute, []HeldLanding{{"P5", now.Add(-22 * time.Minute)}, {"P7", now.Add(-20 * time.Minute)}}, 1, now),
			[]string{"[baton] Your gate in the working tree, job j3 (`make e2e`, 41m in), holds landings: P5's work has waited to land since 14:08 (22m), " +
				"P7's since 14:10 (20m) and 1 phase of another baton session's. They land once it ends.", "`baton verify -- make e2e`, without --here"}, nil},
		{"landings held said", LandingsWaitSaid(here, []HeldLanding{{"P5", now}}, 0), []string{"baton: P5 has waited long to land behind job j3 (`make e2e`), a gate in the checkout"}, nil},
		{"two held said", LandingsWaitSaid(here, []HeldLanding{{"P5", now}}, 2), []string{"P5 and 2 phases of another baton session have waited long"}, nil},
		{"a file conflicting", FileConflicts([]string{"notes.md"}, map[string][]string{"notes.md": {"P3", "P5"}}, map[string][]string{"notes.md": {"P7"}}, true),
			[]string{"[baton] A file conflicted in a second landing of this run: notes.md (P3, then P5; named by P7, not started).", "serializes them",
				"baton opens a replan of the phases not started at your next phase boundary, to give each of them a file of its own: nothing to do about it before then.",
				"the agent merges and resolves it itself: nobody needs to carry anything to it."}, []string{"/baton replan", "ask the human", "auto_replan"}},
		{"files conflicting, no replan", FileConflicts([]string{"a.md", "b.md"}, map[string][]string{"a.md": {"P1", "P2"}, "b.md": {"P2", "P4"}},
			map[string][]string{"a.md": {"P5"}, "b.md": {"P5", "P6"}}, false),
			[]string{"Files conflicted in a second landing of this run: a.md (P1, then P2; named by P5, not started) and b.md (P2, then P4; named by P5 and P6, not started).",
				"auto_replan is off, so baton leaves the plan as it is.", "nobody needs to carry anything to it."}, []string{"opens a replan", "/baton replan"}},
		{"conflicts said", FileConflictsSaid([]string{"a.md", "b.md"}, map[string][]string{"a.md": {"P5"}, "b.md": {"P5", "P6"}}, true),
			[]string{"baton: a.md and b.md conflicted in a second landing of this run, and P5 and P6, not started, name them → a replan of the phases not started opens at the lead's next phase boundary"}, nil},
		{"a conflict said, no replan", FileConflictsSaid([]string{"notes.md"}, map[string][]string{"notes.md": {"P7"}}, false),
			[]string{"baton: notes.md conflicted in a second landing of this run, and P7, not started, names it"}, []string{"→"}},
		{"typed for a failure", LookInTyped([]string{state.LookSlow, state.LookFailing}), []string{"[baton] A gate of yours is failing as it runs: baton's look-in, attached"}, nil},
		{"typed", LookInTyped([]string{state.LookHeld}), []string{"[baton] baton has a look-in for you on a gate or a landing, attached"}, nil},
		{"an agent's for a failure", AgentLookIn([]string{state.LookFailing}), []string{"is failing as it runs", "End this turn, and baton gives it to you then"}, nil},
		{"an agent's", AgentLookIn([]string{state.LookSlow}), []string{"a look-in for you on a gate of yours that runs. End this turn"}, nil},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, c.got)
			}
		}
		for _, w := range c.no {
			if strings.Contains(c.got, w) {
				t.Errorf("%s says %q:\n%s", c.name, w, c.got)
			}
		}
	}
}

// A long line is quoted cut, between characters.
func TestALookInClipsItsLine(t *testing.T) {
	long := strings.Repeat("é", lookClip) // two bytes each
	got := clipLine(long)
	if !strings.HasSuffix(got, "…") || len(got) > lookClip+len("    …") || !strings.HasPrefix(got, "    é") {
		t.Errorf("clipped to %q (%d bytes)", got, len(got))
	}
	if got := clipLine("  short  "); got != "    short" {
		t.Errorf("short: %q", got)
	}
}
