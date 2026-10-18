package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// ended records job id's end, as its runner would.
func (s *session) ended(id, status string, exit int) {
	s.set(func(x *state.State) {
		j := x.Job(id)
		j.Status, j.Exit, j.Started, j.Ended = status, exit, s.now, s.now.Add(time.Minute)
	})
}

// A red gate stands until the same command passes: a narrower gate that passes leaves the phase red, and
// the model is told so as it queues it, and when done is refused; --replace makes another command the
// phase's gate, as a decision the human is told of; and the same command passing clears it.
func TestARedGateStandsUntilTheSameCommandPasses(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	s.must("", "verify", "make", "e2e", "--notes", "the parser is done")
	s.ended("j1", state.JobFailed, 1)

	// A narrower gate is queued, and told it does not clear the red one.
	out := s.must("", "verify", "go test -run TestParse ./e2e", "--notes", "the failing test alone")
	if !strings.Contains(out, "baton: note — P0's gate, job j1 (`make e2e`), is red, and j2 runs another command, so it does not clear it, whatever its result") {
		t.Errorf("verify says:\n%s", out)
	}
	if !s.logged("job_queued", map[string]any{"job": "j2", "red_gate": "j1"}) {
		t.Errorf("events:\n%s", s.events())
	}
	s.ended("j2", state.JobPassed, 0)
	errs := s.fails("done", "P0")
	for _, want := range []string{"not done — P0's gate, job j1 (`make e2e` on", "Since then, job j2 (`go test -run TestParse ./e2e` on",
		") passed after 1m; that does not clear it.", "verify again with `make e2e`"} {
		if !strings.Contains(errs, want) {
			t.Errorf("done refused with:\n%s\nwant %q", errs, want)
		}
	}
	if st := s.state(); st.Phases["P0"].Status != state.PhaseActive || len(st.Decisions) != 0 {
		t.Fatalf("P0 %+v, decisions %+v", st.Phases["P0"], st.Decisions)
	}

	// --replace makes another command the gate: a decision, told to the human.
	out = s.must("", "verify", "--replace", "make", "e2e-fast", "--notes", "the slow suite needs the network")
	if !strings.Contains(out, "baton: noted (d1): `make e2e-fast` is P0's gate now, in place of job j1 (`make e2e`), which is red; the human will be told.") ||
		strings.Contains(out, "does not clear it") {
		t.Errorf("verify --replace says:\n%s", out)
	}
	st := s.state()
	if j := st.Job("j3"); j == nil || j.Replaces != "j1" {
		t.Fatalf("job %+v", j)
	}
	if len(st.Decisions) != 1 || st.Decisions[0].Kind != state.Note || st.Decisions[0].Phase != "P0" ||
		st.Decisions[0].What != "made `make e2e-fast` (job j3) P0's gate in place of job j1 (`make e2e`), which is red" || st.Unattended("P0") != 1 {
		t.Fatalf("decisions %+v", st.Decisions)
	}
	if n := st.Run.Notices; len(n) != 1 || n[0].Kind != "note" || n[0].Text != st.Decisions[0].What {
		t.Errorf("notices %+v", n)
	}
	if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "it is P0's gate now, in place of j1 (`make e2e`), which is red") {
		t.Errorf("announced %q", st.Run.Announce)
	}
	if !s.logged("job_queued", map[string]any{"job": "j3", "replaces_gate": "j1"}) || !s.logged("note", map[string]any{"id": "d1", "replaces_gate": "j1"}) {
		t.Errorf("events:\n%s", s.events())
	}
	// While it runs, done waits for it; once it passes, the phase is done.
	s.must("", "done", "P0", "--notes", "parsed")
	if st := s.state(); st.Phases["P0"].Status != state.PhaseVerifying {
		t.Fatalf("P0 %+v", st.Phases["P0"])
	}
}

// The same command passing clears a red gate by itself, with nothing to decide.
func TestTheSameCommandClearsARedGate(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	s.must("", "verify", "make", "e2e", "--notes", "the parser is done")
	s.ended("j1", state.JobFailed, 1)
	gittest.Write(t, root, "src/parse.go", "package src // fixed\n")
	gittest.Commit(t, root, "fix", "src/parse.go")
	if out := s.must("", "verify", "--replace", "make", "e2e", "--notes", "fixed"); strings.Contains(out, "red") {
		t.Errorf("verify says:\n%s", out)
	}
	s.ended("j2", state.JobPassed, 0)
	s.must("", "done", "P0", "--notes", "parsed")
	if st := s.state(); st.Phases["P0"].Status != state.PhaseDone || len(st.Decisions) != 0 || st.Job("j2").Replaces != "" {
		t.Fatalf("P0 %+v, decisions %+v, j2 %+v", st.Phases["P0"], st.Decisions, st.Job("j2"))
	}
	if s.logged("job_queued", map[string]any{"job": "j2", "red_gate": "j1"}) {
		t.Errorf("events:\n%s", s.events())
	}
}

// The log of a failed job stays while its plan runs, dropped from the record or not, and goes with the
// plan's record when it is dropped.
func TestAFailedJobsLogIsKept(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make e2e", Status: state.JobFailed, Exit: 1}}
		for i := 2; i <= state.MaxJobs; i++ {
			x.Jobs = append(x.Jobs, state.Job{ID: fmt.Sprintf("j%d", i), Phase: "P0", Command: "make e2e", Status: state.JobPassed})
		}
	})
	logs := filepath.Join(s.store().Dir, "jobs")
	os.MkdirAll(logs, 0o755)
	for _, id := range []string{"j1", "j2"} {
		os.WriteFile(filepath.Join(logs, id+".log"), []byte(id+" output\n"), 0o644)
		os.WriteFile(s.store().JobStages(id), []byte(`{"name":"e2e","status":"failed","exit":1}`+"\n"), 0o644)
	}
	os.WriteFile(s.store().JobStages("j0"), []byte("{}\n"), 0o644) // a job long gone
	s.must("", "verify", "make", "e2e", "--notes", "again")
	st := s.state()
	if st.Job("j1") != nil || st.Job("j2") == nil || len(st.KeptLogs) != 1 || st.KeptLogs[0] != "j1" {
		t.Fatalf("jobs %d, kept logs %v", len(st.Jobs), st.KeptLogs)
	}
	for _, f := range []string{"j1.log", "j1.stages", "j2.stages"} {
		if _, err := os.Stat(filepath.Join(logs, f)); err != nil {
			t.Errorf("%s went: %v", f, err)
		}
	}
	if _, err := os.Stat(s.store().JobStages("j0")); err == nil {
		t.Errorf("the stages of a job no longer on record stayed")
	}
	s.must("", "drop")
	matches, _ := filepath.Glob(filepath.Join(s.store().Dir, "gen-*", "jobs", "j1.log"))
	if len(matches) != 1 {
		t.Fatalf("archived: %v", matches)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != "j1 output\n" {
		t.Errorf("archived log %q", b)
	}
}

// staged records gate id's end, as its runner would, with the stages it recorded: in its stages file, as
// `baton stage` writes it, and as the runner read them from its log.
func (s *session) staged(id, status string, exit int, stages ...lane.Stage) {
	s.t.Helper()
	s.ended(id, status, exit)
	s.set(func(x *state.State) { x.Job(id).Stages = stages })
	for _, st := range stages {
		if err := lane.AppendStage(s.store().JobStages(id), st); err != nil {
			s.t.Fatal(err)
		}
	}
}

// The stages of the test gate: `one` passes, `two` fails on TestA.
var (
	passedOne = lane.Stage{Name: "one", Status: lane.StagePassed}
	failedTwo = lane.Stage{Name: "two", Status: lane.StageFailed, Exit: 1, Failures: []string{"TestA"}}
)

// headOf is the commit checked out at root.
func headOf(t *testing.T, root string) string {
	return gittest.Git(t, root, "rev-parse", "HEAD")
}

// redStagedGate is a session whose P0 gate, j1 (`make e2e`), went red on stage two, on the commit it
// returns, and whose fix is committed since.
func redStagedGate(t *testing.T) (*session, string, string) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	first := headOf(t, root)
	s.must("", "verify", "make", "e2e", "--notes", "the parser is done")
	s.staged("j1", state.JobFailed, 1, passedOne, failedTwo)
	gittest.Write(t, root, "src/parse.go", "package src // fixed\n")
	gittest.Commit(t, root, "fix", "src/parse.go")
	return s, root, first
}

// verify --failed reruns the red gate's command on what failed in it: the stage that passed is carried,
// with the commit it passed on and the files changed since, the failed one is narrowed to its failure,
// and the rerun's pass clears the red gate, so done is accepted.
func TestAPassingRerunClearsTheRedGate(t *testing.T) {
	s, root, first := redStagedGate(t)
	head := headOf(t, root)
	out := s.must("", "verify", "--failed", "--notes", "TestA fixed")
	for _, want := range []string{"baton: queued j2 — `make e2e` (P0), on " + head[:12] + ", in a lane of its own.",
		"baton: it reruns what failed in job j1:\n- carried, not run again: `one`, passed in j1 on " + first[:12] + " (changed since: src/parse.go)\n" +
			"- narrowed to what failed there: `two` (TestA)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("verify --failed says:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "does not clear") || strings.Contains(out, "replaces") {
		t.Errorf("verify --failed says:\n%s", out)
	}
	st := s.state()
	if j := st.Job("j2"); j == nil || j.Command != "make e2e" || j.Reruns != "j1" || j.Commit != head || j.Replaces != "" {
		t.Fatalf("j2 %+v", j)
	}
	record, err := lane.ReadStages(s.store().JobRerun("j2"))
	if err != nil || len(record) != 2 || record[0].Name != "one" || record[0].From != "j1" || record[0].Commit != first ||
		record[1].Name != "two" || len(record[1].Failures) != 1 {
		t.Fatalf("the record j2 runs from: %+v, %v", record, err)
	}
	if !s.logged("job_queued", map[string]any{"job": "j2", "reruns": "j1"}) || s.logged("job_queued", map[string]any{"job": "j2", "red_gate": "j1"}) {
		t.Errorf("events:\n%s", s.events())
	}
	if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "baton: job j2 queued for P0 — `make e2e` on "+head[:12]+", in a lane; it reruns what failed in j1") {
		t.Errorf("announced %q", st.Run.Announce)
	}

	// Its result, as the hooks give it, says what it carried and narrowed, and that it clears j1.
	s.staged("j2", state.JobPassed, 0, lane.Stage{Name: "one", Status: lane.StageCarried, Commit: first, From: "j1"}, lane.Stage{Name: "two", Status: lane.StagePassed})
	st = s.state()
	j2 := *st.Job("j2")
	got := decide.JobResult(j2, hooks.JobLog(s.store(), j2), st.Red(), "")
	if want := "It reran what failed in job j1 (verify --failed):\n- carried, not run again: `one`, passed in j1 on " + first[:12] + " (changed since: src/parse.go)\n" +
		"- narrowed to what failed there: `two` (TestA)\nAs it passed, it clears j1."; !strings.Contains(got, want) {
		t.Errorf("result:\n%s\nwant %q", got, want)
	}
	if st.RedGate("P0") != nil {
		t.Errorf("red gate %+v", st.RedGate("P0"))
	}
	s.must("", "done", "P0", "--notes", "parsed")
	if st := s.state(); st.Phases["P0"].Status != state.PhaseDone || len(st.Decisions) != 0 {
		t.Fatalf("P0 %+v, decisions %+v", st.Phases["P0"], st.Decisions)
	}
}

// A rerun that fails is the red gate from then on: done is refused on it, and --failed reruns what failed
// in it, carrying what it carried.
func TestARedRerunKeepsTheGateRed(t *testing.T) {
	s, _, first := redStagedGate(t)
	s.must("", "verify", "--failed", "--notes", "TestA fixed")
	s.staged("j2", state.JobFailed, 1, lane.Stage{Name: "one", Status: lane.StageCarried, Commit: first, From: "j1"}, failedTwo)
	errs := s.fails("done", "P0")
	for _, want := range []string{"not done — P0's gate, job j2 (`make e2e` on", "rerun what failed with `baton verify --failed --notes \"…\"`"} {
		if !strings.Contains(errs, want) {
			t.Errorf("done refused with:\n%s\nwant %q", errs, want)
		}
	}
	s.must("", "verify", "--failed", "--notes", "TestA again")
	if j := s.state().Job("j3"); j == nil || j.Reruns != "j2" {
		t.Fatalf("j3 %+v", j)
	}
	record, _ := lane.ReadStages(s.store().JobRerun("j3"))
	if len(record) != 2 || record[0].Status != lane.StageCarried || record[0].From != "j1" || record[0].Commit != first {
		t.Errorf("the record j3 runs from: %+v", record)
	}
}

// --stages runs the stages it names whole: a carried one is brought back.
func TestFailedBringsStagesBack(t *testing.T) {
	s, _, _ := redStagedGate(t)
	out := s.must("", "verify", "--failed", "--stages", "one", "--notes", "TestA fixed; one touches the parser too")
	if !strings.Contains(out, "- narrowed to what failed there: `two` (TestA)\n- run whole: `one`\n") || strings.Contains(out, "carried, not run again") {
		t.Errorf("verify --failed --stages says:\n%s", out)
	}
	if record, _ := lane.ReadStages(s.store().JobRerun("j2")); len(record) != 1 || record[0].Name != "two" {
		t.Errorf("the record j2 runs from: %+v", record)
	}
}

// --failed is refused, saying why, with nothing to rerun, or when what was asked does not fit the gate.
func TestFailedRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(s *session)
		args  []string
		want  string
		why   string
	}{
		{"no red gate", nil, []string{"verify", "--failed", "--notes", "x"}, "not queued — --failed reruns what failed in P0's red gate, and P0 has none", "no red gate"},
		{"a gate that runs", func(s *session) { s.must("", "verify", "make", "e2e", "--notes", "x") },
			[]string{"verify", "--failed", "--notes", "x"}, "Its gate, job j1 (`make e2e` on", "no red gate"},
		{"a red gate with no stages", func(s *session) {
			s.must("", "verify", "make", "e2e", "--notes", "x")
			s.ended("j1", state.JobFailed, 1)
		}, []string{"verify", "--failed", "--notes", "x"}, "recorded no stages, and baton reads no failed test in its log", "no stages"},
		{"a stage it did not record", func(s *session) {
			s.must("", "verify", "make", "e2e", "--notes", "x")
			s.staged("j1", state.JobFailed, 1, passedOne, failedTwo)
		}, []string{"verify", "--failed", "--stages", "tow", "--notes", "x"}, "--stages names `tow`, which job j1 did not record: its stages are `one`, `two`.", "unknown stages"},
		{"another command", func(s *session) {
			s.must("", "verify", "make", "e2e", "--notes", "x")
			s.staged("j1", state.JobFailed, 1, passedOne, failedTwo)
		}, []string{"verify", "--failed", "--notes", "x", "--", "make", "test"}, "--failed reruns job j1's own command, `make e2e`, and `make test` is another", "another command"},
		{"--stages alone", nil, []string{"verify", "--stages", "one", "--notes", "x", "make", "e2e"}, "baton verify --failed [--stages <name>,<name>]", ""},
		{"an await", nil, []string{"await", "--failed", "--notes", "x"}, "unknown flag --failed", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := gitSession(t)
			if c.setup != nil {
				c.setup(s)
			}
			before := len(s.state().Jobs)
			if why := s.fails(c.args...); !strings.Contains(why, c.want) {
				t.Errorf("refusal %q, want it to mention %q", why, c.want)
			}
			if c.why != "" && !s.logged("refused", map[string]any{"command": c.args[0], "why": c.why}) {
				t.Errorf("no refused event for %q: %s", c.why, s.events())
			}
			if n := len(s.state().Jobs); n != before {
				t.Errorf("a refused %s queued a job: %d, then %d", c.args[0], before, n)
			}
		})
	}
}

// A narrower command that passes still clears nothing, and done's refusal names --failed for the staged
// gate that stands.
func TestANarrowerCommandStillClearsNothing(t *testing.T) {
	s, _, _ := redStagedGate(t)
	s.must("", "verify", "go test -run TestA ./e2e", "--notes", "the failing test alone")
	s.ended("j2", state.JobPassed, 0)
	errs := s.fails("done", "P0")
	for _, want := range []string{"not done — P0's gate, job j1 (`make e2e` on", ") passed after 1m; that does not clear it.",
		"As it recorded its stages, `baton verify --failed` runs `make e2e` again on what failed alone", "rerun what failed with `baton verify --failed --notes \"…\"`"} {
		if !strings.Contains(errs, want) {
			t.Errorf("done refused with:\n%s\nwant %q", errs, want)
		}
	}
}

// baton stop ends a job of the caller's, queued or running, as interrupted: the red gate stands as it was,
// done is refused on it as before, and the stop is logged and told. A job that ended, or that the run has
// not, is not stopped.
func TestStopLeavesTheRedGateStanding(t *testing.T) {
	s, _, _ := redStagedGate(t)
	s.must("", "verify", "make", "e2e", "--notes", "the whole gate again")
	s.set(func(x *state.State) { j := x.Job("j2"); j.Status, j.Started = state.JobRunning, s.now })
	if st := s.state(); st.Standing("P0")[0].ID != "j2" {
		t.Fatalf("standing %+v", st.Standing("P0"))
	}
	out := s.must("", "stop", "j2", "--why", "two failed again")
	for _, want := range []string{"baton: stopped job j2 (`make e2e` on", ", P0): it counts as interrupted, so it checked nothing",
		"P0's gate is still job j1 (`make e2e`), red, as it was.", "To rerun only what failed, fix it, commit, and `baton verify --failed --notes \"…\"`"} {
		if !strings.Contains(out, want) {
			t.Errorf("stop says:\n%s\nwant %q", out, want)
		}
	}
	st := s.state()
	if j := st.Job("j2"); j.Status != state.JobInterrupted || j.Reason != "stopped with baton stop (two failed again)" || j.Delivered.IsZero() {
		t.Fatalf("j2 %+v", j)
	}
	if g := st.RedGate("P0"); g == nil || g.ID != "j1" {
		t.Fatalf("red gate %+v", g)
	}
	if !s.logged("job_stopped", map[string]any{"job": "j2", "phase": "P0", "was": "running", "why": "two failed again", "red_gate": "j1"}) {
		t.Errorf("events:\n%s", s.events())
	}
	if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "baton: job j2 (`make e2e`, P0) stopped with baton stop (two failed again) → its runner ends it") {
		t.Errorf("announced %q", st.Run.Announce)
	}
	if errs := s.fails("done", "P0"); !strings.Contains(errs, "not done — P0's gate, job j1") ||
		!strings.Contains(errs, "was interrupted after 0s: stopped with baton stop (two failed again); that does not clear it.") {
		t.Errorf("done refused with:\n%s", errs)
	}

	for _, c := range []struct {
		args      []string
		want, why string
	}{
		{[]string{"stop", "j2"}, "not stopped — job j2 (`make e2e`, P0) has ended: it was interrupted", "ended"},
		{[]string{"stop", "j1"}, "has ended: it failed (exit 1) after 1m. To rerun only what failed, fix it, commit, and `baton verify --failed", "ended"},
		{[]string{"stop", "j9"}, "not stopped — this run has no job j9 (a phase agent's jobs are its own to stop). None of its jobs is queued or running.", "no such job"},
		{[]string{"stop"}, "usage: baton stop <job> [--why \"<why you stop it>\"]", ""},
	} {
		if why := s.fails(c.args...); !strings.Contains(why, c.want) {
			t.Errorf("%v refused with %q, want %q", c.args, why, c.want)
		}
		if c.why != "" && !s.logged("refused", map[string]any{"command": "stop", "why": c.why}) {
			t.Errorf("no refused event for %q: %s", c.why, s.events())
		}
	}
}

// Stopped at its first failure, a gate's record is what --failed reruns from, though no gate of the phase
// went red: what passed is carried, and the stage it failed in runs whole, as it never ended.
func TestStopAtTheFirstFailureThenRerunWhatFailed(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	first := headOf(t, root)
	s.must("", "verify", "make", "e2e", "--notes", "the parser is done")
	s.must("", "done", "P0", "--notes", "parsed")
	s.set(func(x *state.State) {
		j := x.Job("j1")
		j.Status, j.Started, j.Stages = state.JobRunning, s.now, []lane.Stage{passedOne}
		j.Failing = &state.Failing{At: s.now, Line: "--- FAIL: TestA", Stage: "two"}
	})
	out := s.must("", "stop", "j1")
	for _, want := range []string{"P0 waited on it after `baton done`, so it reopens as unverified.", "To rerun only what failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stop says:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "red, as it was") {
		t.Errorf("stop says:\n%s", out)
	}
	if j := s.state().Job("j1"); j.Reason != "stopped with baton stop" {
		t.Errorf("j1 %+v", j)
	}

	// Back on P0, the fix committed, --failed carries `one` and runs `two` whole.
	s.set(func(x *state.State) {
		x.Phases["P0"].Status, x.Phases["P0"].On, x.Current = state.PhaseActive, nil, "P0"
	})
	gittest.Write(t, root, "src/parse.go", "package src // fixed\n")
	gittest.Commit(t, root, "fix", "src/parse.go")
	out = s.must("", "verify", "--failed", "--notes", "TestA fixed")
	if !strings.Contains(out, "it reruns what failed in job j1:\n- carried, not run again: `one`, passed in j1 on "+first[:12]+" (changed since: src/parse.go)\nA stage j1's record does not name runs whole too.") {
		t.Errorf("verify --failed says:\n%s", out)
	}
	if j := s.state().Job("j2"); j == nil || j.Reruns != "j1" {
		t.Fatalf("j2 %+v", j)
	}
}

// A phase agent names its jobs with or without its phase.
func TestStopAPhaseAgentsJob(t *testing.T) {
	s, _ := gitSession(t)
	s.set(func(x *state.State) {
		x.IDPrefix = "P0."
		x.Jobs = append(x.Jobs, state.Job{ID: "P0.j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Status: state.JobQueued})
	})
	if out := s.must("", "stop", "j1"); !strings.Contains(out, "stopped job P0.j1") {
		t.Errorf("stop says:\n%s", out)
	}
	if j := s.state().Job("P0.j1"); j.Status != state.JobInterrupted {
		t.Errorf("P0.j1 %+v", j)
	}
}
