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
			[]string{"failed (exit 1) after 12m", "Fix what failed, commit, and verify again", "`baton done P3` is refused while its latest verification is red"}, []string{"verify_timeout"}},
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
		got := JobResult(c.job, "/x/j4.log", "line 1\nFAIL thing\n", red, "")
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
	if got := JobResult(job(state.JobAwait, state.JobPassed, 0, ""), "/x/j4.log", "", nil, ""); !strings.Contains(got, "Its log: /x/j4.log") || strings.Contains(got, "Red now") {
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
		got := JobResult(j, "/x/j4.log", "FAIL", nil, c.qa)
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
	got := RedRefusal(state.Job{ID: "j4", Phase: "P3", Command: "make test", Commit: "3f2a9c1b04de55aa", Status: state.JobFailed, Exit: 1})
	for _, w := range []string{"not done — P3's latest verification, job j4 (`make test` on 3f2a9c1b04de), failed (exit 1)", "verify again", "`baton done P3 --despite-red \"<why>\"`"} {
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
