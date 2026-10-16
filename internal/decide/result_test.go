package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestJobResult(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	job := func(kind, status string, exit int, reason string) state.Job {
		j := state.Job{ID: "j4", Phase: "P3", Kind: kind, Command: "make test", Status: status, Exit: exit, Reason: reason,
			Started: t0, Ended: t0.Add(12*time.Minute + 5*time.Second)}
		if kind == state.JobVerify {
			j.Commit = "3f2a9c1b04de55aa"
		}
		return j
	}
	red := []state.Job{
		{ID: "j2", Phase: "P1", Command: "go test ./...", Commit: "aaaabbbbccccdddd", Status: state.JobFailed, Exit: 2, Started: t0, Ended: t0.Add(time.Minute)},
		job(state.JobVerify, state.JobFailed, 1, ""), // the phase's own: not named among the others
	}
	for _, c := range []struct {
		name string
		job  state.Job
		want []string
		not  []string
	}{
		{"passed", job(state.JobVerify, state.JobPassed, 0, ""),
			[]string{"[baton] Job j4 (P3) passed after 12m: `make test` on 3f2a9c1b04de.", "It checked 3f2a9c1b04de: what you committed after it"}, []string{"Fix"}},
		{"await passed", job(state.JobAwait, state.JobPassed, 0, ""), []string{"passed after 12m: `make test`.", "Carry on."}, []string{"checked"}},
		{"failed", job(state.JobVerify, state.JobFailed, 1, ""),
			[]string{"failed (exit 1) after 12m", "Fix what failed (quick checks in place help), commit, and verify again with the same command, `make test`: a red gate stands until the same command passes",
				"`baton done P3` is refused until then"}, []string{"verify_timeout"}},
		{"timeout", job(state.JobVerify, state.JobTimeout, -1, ""), []string{"ran out of time after 12m", "ran past verify_timeout", "counts as a failure"}, nil},
		{"lane error", job(state.JobVerify, state.JobLaneError, 127, "the command did not run (exit 127)"),
			[]string{"could not run: the command did not run (exit 127)", "This is not a test result", "do not look for a bug in P3's code", "Run the gate in place instead, and `baton note`", ".baton-lane"},
			[]string{"Fix what failed"}},
		{"interrupted", job(state.JobVerify, state.JobInterrupted, -1, "its runner (pid 7) is gone"),
			[]string{"was interrupted after 12m: its runner (pid 7) is gone", "Nothing was checked"}, []string{"hung"}},
		{"hung", job(state.JobVerify, state.JobInterrupted, -1, HungReason(20*time.Minute)),
			[]string{"was interrupted after 12m: hung — its log had not grown, and its processes had used no CPU, for 20m: `make test`",
				"baton stopped it as hung, so nothing was checked: find what it waits on"}, []string{"Hand it to baton again, or run it in place."}},
		{"await failed", job(state.JobAwait, state.JobFailed, 1, ""), []string{"What it waited on did not succeed"}, []string{"verify again"}},
	} {
		got := JobResult(c.job, Log{Path: "/x/j4.log", Tail: "line 1\nFAIL thing\n"}, red, "")
		for _, w := range append(c.want, "The end of its log (/x/j4.log):\n\nline 1\nFAIL thing\n\n",
			"Red now: P1 (job j2, `go test ./...` on aaaabbbbcccc failed (exit 2) after 1m). Failures in their area are theirs, not this phase's: leave them to it.") {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s has %q:\n%s", c.name, w, got)
			}
		}
		if strings.Contains(got, "P3 (job j4") {
			t.Errorf("%s names its own phase as red:\n%s", c.name, got)
		}
		if strings.Contains(got, "--despite-red") {
			t.Errorf("%s, with no QA phase ahead, says other phases' work may be why:\n%s", c.name, got)
		}
	}
	if got := JobResult(job(state.JobAwait, state.JobPassed, 0, ""), Log{Path: "/x/j4.log"}, nil, ""); !strings.Contains(got, "Its log: /x/j4.log") || strings.Contains(got, "Red now") {
		t.Errorf("no tail, nothing red:\n%s", got)
	}
}

// While the plan leaves its heavy checks to a QA phase ahead, the phases before it may leave work in
// progress: a red result says other phases' work may be why, and how to finish over it. Only a red one.
func TestARedResultBeforeAQAPhaseMayBeOthers(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	const hint = "This plan leaves its full checks to P10 (QA), and other phases' work in progress may be why this is red: " +
		"fix what is P3's, and if the rest is not, finish with `baton done P3 --despite-red \"<why>\"` (the human is told) and leave it to P10 (QA)."
	for _, c := range []struct {
		name, kind, status, qa string
		hints                  bool
	}{
		{"verify failed", state.JobVerify, state.JobFailed, "P10 (QA)", true},
		{"verify ran out of time", state.JobVerify, state.JobTimeout, "P10 (QA)", true},
		{"await failed", state.JobAwait, state.JobFailed, "P10 (QA)", true},
		{"passed", state.JobVerify, state.JobPassed, "P10 (QA)", false},
		{"could not run", state.JobVerify, state.JobLaneError, "P10 (QA)", false},
		{"interrupted", state.JobVerify, state.JobInterrupted, "P10 (QA)", false},
		{"no QA phase ahead, or aggregate_qa off", state.JobVerify, state.JobFailed, "", false},
	} {
		j := state.Job{ID: "j4", Phase: "P3", Kind: c.kind, Command: "make test", Status: c.status, Exit: 1, Started: t0, Ended: t0.Add(time.Minute)}
		got := JobResult(j, Log{Path: "/x/j4.log", Tail: "FAIL"}, nil, c.qa)
		if strings.Contains(got, hint) != c.hints {
			t.Errorf("%s: hints %v, want %v:\n%s", c.name, !c.hints, c.hints, got)
		}
	}
}

func TestQAAsk(t *testing.T) {
	verify, await := QAAsk(state.JobVerify, "P3", "P10 (QA)"), QAAsk(state.JobAwait, "P3", "P10 (QA)")
	for _, c := range []struct {
		got       string
		want, not []string
	}{
		{verify, []string{"Is this a focused check of what P3 changed, or a full suite, lint or end-to-end run that P10 (QA) runs anyway?",
			"leave such runs to P10 (QA)", "baton verify \"<command>\" --qa \"<why now>\""}, []string{"baton await", "CI"}},
		{await, []string{"Is what this waits on a focused check of what P3 changed, or a full check in disguise (a CI run of every suite",
			"that P10 (QA) runs anyway?", "baton await \"<command>\" --qa \"<why now>\""}, []string{"baton verify"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("lacks %q:\n%s", w, c.got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(c.got, w) {
				t.Errorf("has %q:\n%s", w, c.got)
			}
		}
	}
}

func TestJobsFinished(t *testing.T) {
	if got := JobsFinished([]state.Job{{ID: "j4", Phase: "P3"}}); got != "[baton] Job j4 (P3) finished; its result is attached." {
		t.Errorf("one: %q", got)
	}
	if got := JobsFinished([]state.Job{{ID: "j4", Phase: "P3"}, {ID: "j5", Phase: "P3"}, {ID: "j6", Phase: "P1"}}); got != "[baton] Jobs j4, j5, j6 (P3, P1) finished; their results are attached." {
		t.Errorf("three: %q", got)
	}
	// A phase agent started again is given nothing as it starts: the result comes after.
	if got := AgentJobsFinished([]state.Job{{ID: "P3.j4", Phase: "P3"}}); got != "[baton] Job P3.j4 (P3) finished. End this turn, and baton gives you its result then (or with your next tool call, if you make one first)." {
		t.Errorf("an agent's: %q", got)
	}
}

func TestFinishedAside(t *testing.T) {
	failed := state.Job{ID: "j4", Phase: "P3", Command: "make test", Status: state.JobFailed, Exit: 1, Started: time.Unix(0, 0), Ended: time.Unix(720, 0)}
	parked := func(reason, job string) *state.PhaseState {
		return &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: reason, Job: job}}
	}
	for _, c := range []struct {
		name    string
		current string
		phase   *state.PhaseState
		job     func(state.Job) state.Job
		want    string
	}{
		{"set aside while it ran", "P5", parked(state.ParkJob, "j4"), nil, "baton: job j4 (`make test`, P3) failed (exit 1) after 12m → P3 comes back once P5 is done or set aside"},
		{"set aside at a checkpoint", "P5", parked(state.ParkCheckpoint, "j4"), nil, "baton: job j4 (`make test`, P3) failed (exit 1) after 12m → P3 comes back once P5 is done or set aside"},
		{"no phase in hand: it comes back now", "", parked(state.ParkJob, "j4"), nil, "baton: job j4 (`make test`, P3) failed (exit 1) after 12m"},
		{"another job's park", "P5", parked(state.ParkJob, "j2"), nil, ""},
		{"the phase in hand", "P3", &state.PhaseState{Status: state.PhaseActive}, nil, ""},
		{"verifying", "P5", &state.PhaseState{Status: state.PhaseVerifying}, nil, ""},
		{"cancelled", "P5", parked(state.ParkJob, "j4"), func(j state.Job) state.Job { j.Delivered = time.Unix(720, 0); return j }, ""},
		{"still running", "P5", parked(state.ParkJob, "j4"), func(j state.Job) state.Job { j.Status = state.JobRunning; return j }, ""},
	} {
		st := state.State{Mode: state.ModeRunning, Current: c.current, Phases: map[string]*state.PhaseState{"P3": c.phase}}
		j := failed
		if c.job != nil {
			j = c.job(j)
		}
		if got := FinishedAside(st, j); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRedRefusal(t *testing.T) {
	got := RedRefusal(state.Job{ID: "j4", Phase: "P3", Command: "make test", Commit: "3f2a9c1b04de55aa", Status: state.JobFailed, Exit: 1}, nil)
	for _, w := range []string{"not done — P3's gate, job j4 (`make test` on 3f2a9c1b04de), failed (exit 1)", "verify again with `make test`", "`baton done P3 --despite-red \"<why>\"`"} {
		if !strings.Contains(got, w) {
			t.Errorf("lacks %q:\n%s", w, got)
		}
	}
}

// A phase agent brought back to its phase by its verification: the human hears why, and the agent's
// brief says what to do.
func TestReopenedWords(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	j := state.Job{ID: "P2.j1", Phase: "P2", Command: "make test", Status: state.JobFailed, Exit: 1, Started: t0, Ended: t0.Add(3 * time.Minute)}
	lane := j
	lane.Status, lane.Reason = state.JobLaneError, "no lane"
	for _, c := range []struct{ got, want string }{
		{AgentRetakes("P2", j), "baton: P2's verification failed (exit 1) after 3m (job P2.j1) → its agent takes P2 up again"},
		{AgentRetakes("P2", lane), "baton: P2's verification could not run: no lane (job P2.j1), with no verdict → its agent takes P2 up again"},
		{AgentRetakes("P2", state.Job{}), "baton: P2's verification is gone from the record → its agent takes P2 up again"},
		{ReopenedNow("P2 — Two", state.ParkRed), "Now: continue P2 — Two. It was finished, but its verification failed (its result is below): fix what failed, commit, and finish it again."},
		{ReopenedNow("P2 — Two", state.ParkCheckpoint), ""},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
	if got := ReopenedNow("P2 — Two", state.ParkUnverified); !strings.Contains(got, "ended without a verdict") {
		t.Errorf("unverified: %q", got)
	}
}

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{45 * time.Second: "45s", 12 * time.Minute: "12m", 125 * time.Minute: "2h05m"} {
		if got := Span(d); got != want {
			t.Errorf("%s: %q", d, got)
		}
	}
}

// A result lists the lines that report a failure from before the end of its log, so the first failure of
// a long log is in front of the model; a passing job's result lists none.
func TestJobResultListsEarlierFailures(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	j := state.Job{ID: "j4", Phase: "P3", Kind: state.JobVerify, Command: "make e2e", Commit: "3f2a9c1b04de55aa", Status: state.JobFailed, Exit: 1, Started: t0, Ended: t0.Add(time.Hour)}
	log := Log{Path: "/x/j4.log", Tail: "--- FAIL: TestLate (3s)\nFAIL", Failures: []string{"--- FAIL: TestEarly (1s)", "    --- FAIL: TestEarly/sub (0s)"}, More: 3}
	got := JobResult(j, log, nil, "")
	want := "[baton] Job j4 (P3) failed (exit 1) after 1h00m: `make e2e` on 3f2a9c1b04de.\n" +
		"Lines that report a failure earlier in its log, before its end below:\n\n--- FAIL: TestEarly (1s)\n    --- FAIL: TestEarly/sub (0s)\n" +
		"(and 3 more such lines: search the log for them)\n\n" +
		"The end of its log (/x/j4.log):\n\n--- FAIL: TestLate (3s)\nFAIL\n\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("got:\n%s\nwant it to begin:\n%s", got, want)
	}
	j.Status = state.JobPassed
	if got := JobResult(j, log, nil, ""); strings.Contains(got, "TestEarly") || strings.Contains(got, "earlier in its log") {
		t.Errorf("a passing job lists failures:\n%s", got)
	}
}

// A narrower gate that passes is told that it did not clear its phase's red gate, and how one is cleared.
func TestANarrowerGateIsToldTheRedOneStands(t *testing.T) {
	red := state.Job{ID: "j4", Phase: "P3", Kind: state.JobVerify, Command: "make e2e", Status: state.JobFailed, Exit: 1}
	narrow := state.Job{ID: "j5", Phase: "P3", Kind: state.JobVerify, Command: "go test -run TestX ./e2e", Commit: "3f2a9c1b04de55aa", Status: state.JobPassed}
	got := JobResult(narrow, Log{}, []state.Job{red}, "")
	for _, w := range []string{"It checked 3f2a9c1b04de", "It does not clear P3's gate, job j4 (`make e2e`), which is red: a red gate stands until the same command passes",
		"verify it with --replace: a decision without the human, and they are told"} {
		if !strings.Contains(got, w) {
			t.Errorf("lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "Red now: P3") {
		t.Errorf("names its own phase among the others:\n%s", got)
	}
	for name, others := range map[string][]state.Job{
		"itself":          {{ID: "j5", Phase: "P3", Kind: state.JobVerify, Status: state.JobFailed}},
		"another phase's": {{ID: "j2", Phase: "P1", Kind: state.JobVerify, Command: "make lint", Status: state.JobFailed}},
		"a wait outside":  {{ID: "j3", Phase: "P3", Kind: state.JobAwait, Command: "gh run watch 7", Status: state.JobFailed}},
	} {
		if got := JobResult(narrow, Log{}, others, ""); strings.Contains(got, "does not clear") {
			t.Errorf("%s:\n%s", name, got)
		}
	}
}

// Refused as red, a phase is told which command must pass, and what ran since without clearing it.
func TestRedRefusalNamesTheCommandThatMustPass(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	red := state.Job{ID: "j4", Phase: "P3", Command: "make e2e", Commit: "3f2a9c1b04de55aa", Status: state.JobFailed, Exit: 1, Started: t0, Ended: t0.Add(time.Hour)}
	after := []state.Job{
		{ID: "j5", Phase: "P3", Command: "go test -run TestX ./e2e", Commit: "4f2a9c1b04de55aa", Status: state.JobPassed, Started: t0, Ended: t0.Add(time.Minute)},
		{ID: "j6", Phase: "P3", Command: "make e2e", Commit: "5f2a9c1b04de55aa", Status: state.JobLaneError, Reason: "no lane"},
		{ID: "j7", Phase: "P3", Command: "make lint", Status: state.JobRunning},
	}
	got := RedRefusal(red, after)
	want := "not done — P3's gate, job j4 (`make e2e` on 3f2a9c1b04de), failed (exit 1) after 1h00m. " +
		"Since then, job j5 (`go test -run TestX ./e2e` on 4f2a9c1b04de) passed after 1m; job j6 (`make e2e` on 5f2a9c1b04de) could not run: no lane; " +
		"job j7 (`make lint`) runs now; that does not clear it. " +
		"A red gate stands until the same command passes, and a narrower check that passes does not clear it " +
		"(to make another command P3's gate instead, verify it with --replace: a decision without the human, and they are told). " +
		"Fix what failed, commit, and verify again with `make e2e`; `baton done P3` then waits for it. " +
		"If the failure is not P3's to fix, finish it anyway with `baton done P3 --despite-red \"<why>\"`: that is a decision without the human, and they are told."
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestGateWords(t *testing.T) {
	red := state.Job{ID: "j4", Phase: "P3", Command: "make e2e", Status: state.JobFailed}
	j := state.Job{ID: "j7", Phase: "P3", Command: "make e2e-fast", Replaces: "j4"}
	what, undo := GateReplaced(red, j)
	for _, c := range []struct{ got, want string }{
		{what, "made `make e2e-fast` (job j7) P3's gate in place of job j4 (`make e2e`), which is red"},
		{undo, "verify P3 again with `make e2e` and fix what it finds: that is P3's gate again"},
		{GateReplacedSay("d2", red, j), "noted (d2): `make e2e-fast` is P3's gate now, in place of job j4 (`make e2e`), which is red; the human will be told."},
		{GateReplacedSay("", red, j), "`make e2e-fast` is P3's gate now, in place of job j4 (`make e2e`), which is red."},
		{RedGateStands(red, j), "note — P3's gate, job j4 (`make e2e`), is red, and j7 runs another command, so it does not clear it, whatever its result: " +
			"a red gate stands until the same command passes, and a narrower check that passes does not clear it " +
			"(to make another command P3's gate instead, verify it with --replace: a decision without the human, and they are told)."},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
}
