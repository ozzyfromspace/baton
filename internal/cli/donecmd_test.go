package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// afterSession is a hosted session in a git repository running testdata/after.md: P0 and P1 need
// nothing, P2 follows P1, P3 needs P0 and P2, and P4 needs every phase.
func afterSession(t *testing.T) *session {
	s, planFile, _ := newRepoSession(t)
	doc, err := os.ReadFile(filepath.Join("..", "plan", "testdata", "after.md"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(planFile, doc, 0o644)
	s.env["BATON_HOME"] = t.TempDir()
	s.attach(planFile)
	s.set(func(x *state.State) { state.Claim(x, "inst", 1, s.now) })
	return s
}

// job puts P0's job on record as baton verify and its runner leave it, with a log.
func (s *session) job(status string, exit int) state.Job {
	s.t.Helper()
	st := s.store()
	j := state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Commit: gitx.Head(st.Root), Status: status, Exit: exit,
		Notes: "the survey is written; make test checks the links", Log: st.JobLog("j1"), Started: s.now.Add(-12 * time.Minute)}
	if j.Finished() {
		j.Ended = s.now
	}
	os.MkdirAll(filepath.Dir(j.Log), 0o755)
	os.WriteFile(j.Log, []byte("--- FAIL: TestLinks\nFAIL\n"), 0o644)
	s.set(func(x *state.State) { x.Jobs, x.JobSeq = []state.Job{j}, 1 })
	return j
}

// Done with the phase's gate still running: the phase is verifying, and the run moves on to what does not
// need it.
func TestDoneWhileItsGateRuns(t *testing.T) {
	s := afterSession(t)
	j := s.job(state.JobRunning, 0)
	out := s.must("", "done", "P0", "--notes", "the survey is in docs/")
	for _, want := range []string{"baton: P0 is finished, and verifying: job j1 (`make test` on " + j.Commit[:12] + ") is still running. baton marks P0 done when it passes",
		"Next phase: P1 (The schema)"} {
		if !strings.Contains(out, want) {
			t.Errorf("done lacks %q:\n%s", want, out)
		}
	}
	st, _ := s.store().Load()
	if st.Phases["P0"].Status != state.PhaseVerifying || st.Current != "P1" || !st.BoundaryOwed {
		t.Fatalf("after done: P0 %s, in hand %q", st.Phases["P0"].Status, st.Current)
	}
	if !s.logged("phase_verifying", map[string]any{"phase": "P0", "job": "j1", "next": "P1"}) || s.logged("phase_done", map[string]any{"phase": "P0"}) {
		t.Errorf("events:\n%s", s.events())
	}
	if want := "baton: P0 verifying — job j1 (`make test`) runs on; P0 is done once it passes, and reopens if it fails"; len(st.Run.Announce) != 1 || st.Run.Announce[0] != want {
		t.Errorf("announced %q, want %q", st.Run.Announce, want)
	}
}

// Done while the phase's gate runs in the checkout (--here): the phase is verifying, and no other phase
// starts there until the gate ends.
func TestDoneWhileItsGateRunsHere(t *testing.T) {
	s := afterSession(t)
	s.job(state.JobRunning, 0)
	s.set(func(x *state.State) { x.Jobs[0].Here = true })
	out := s.must("", "done", "P0", "--notes", "the survey is in docs/")
	if want := "is still running. baton marks P0 done when it passes; if it fails, P0 reopens, and is taken up again from your notes on that job. " +
		"Job j1 runs in this working tree, so no other phase starts here until it ends. End your turn now; baton wakes you when there is something to do."; !strings.Contains(out, want) {
		t.Errorf("done says:\n%s\nwant %q", out, want)
	}
	st, _ := s.store().Load()
	if st.Phases["P0"].Status != state.PhaseVerifying || st.Current != "" || st.BoundaryOwed {
		t.Fatalf("after done: P0 %s, in hand %q, boundary owed %v", st.Phases["P0"].Status, st.Current, st.BoundaryOwed)
	}
	if !s.logged("phase_verifying", map[string]any{"phase": "P0", "job": "j1", "next": ""}) {
		t.Errorf("events:\n%s", s.events())
	}
}

// A red verification refuses done, with the result the first time; --despite-red finishes the phase, as a
// decision without the human.
func TestDoneRefusesARedVerification(t *testing.T) {
	s := afterSession(t)
	j := s.job(state.JobFailed, 1)
	errs := s.fails("done", "P0")
	for _, want := range []string{"[baton] Job j1 (P0) failed (exit 1) after 12m: `make test` on " + j.Commit[:12], "--- FAIL: TestLinks",
		"baton: not done — P0's latest verification, job j1", "`baton done P0 --despite-red \"<why>\"`"} {
		if !strings.Contains(errs, want) {
			t.Errorf("refusal lacks %q:\n%s", want, errs)
		}
	}
	if st, _ := s.store().Load(); st.Job("j1").Delivered.IsZero() || st.Phases["P0"].Status != state.PhaseActive {
		t.Errorf("after the refusal: %+v, P0 %+v", st.Job("j1"), st.Phases["P0"])
	}
	if !s.logged("refused", map[string]any{"command": "done", "why": "red", "job": "j1"}) {
		t.Errorf("events:\n%s", s.events())
	}
	if errs := s.fails("done", "P0"); strings.Contains(errs, "--- FAIL") || !strings.Contains(errs, "not done — P0's latest verification") {
		t.Errorf("the second refusal:\n%s", errs)
	}

	out := s.must("", "done", "P0", "--despite-red", "the link checker needs the network, which this machine lacks")
	if !strings.Contains(out, "baton: noted (d1): P0 finished despite its red verification; the human will be told.") || !strings.Contains(out, "baton: P0 done. Next phase: P1") {
		t.Fatalf("despite red:\n%s", out)
	}
	st, _ := s.store().Load()
	if len(st.Decisions) != 1 || st.Decisions[0].Phase != "P0" || !strings.Contains(st.Decisions[0].What, "finished P0 despite its red verification (job j1, `make test` on") ||
		!strings.Contains(st.Decisions[0].What, "which this machine lacks") || st.Phases["P0"].Status != state.PhaseDone || st.Unattended("P0") != 1 {
		t.Fatalf("decisions %+v, P0 %+v", st.Decisions, st.Phases["P0"])
	}
	if !s.logged("note", map[string]any{"id": "d1", "despite_red": "j1"}) {
		t.Errorf("events:\n%s", s.events())
	}
}

// Finishing over a red verification counts toward max_auto_decisions like any note.
func TestDespiteRedCountsTowardTheReview(t *testing.T) {
	s := afterSession(t)
	s.env["BATON_MAX_AUTO_DECISIONS"] = "1"
	s.job(state.JobFailed, 1)
	out := s.must("", "done", "P0", "--despite-red", "flaky")
	if !strings.Contains(out, "end your turn now, and baton stops for the human to review them") {
		t.Fatalf("done:\n%s", out)
	}
	if st, _ := s.store().Load(); st.ReviewDue != "P0" || !s.logged("review_due", map[string]any{"phase": "P0"}) {
		t.Fatalf("review due %q; events:\n%s", st.ReviewDue, s.events())
	}
}

// --despite-red records nothing when nothing is red, and a job with no verdict lets done through with a
// reminder that nothing was checked.
func TestDoneOverAJobWithoutAVerdict(t *testing.T) {
	s := afterSession(t)
	s.job(state.JobLaneError, 127)
	out := s.must("", "done", "P0", "--despite-red", "x")
	for _, want := range []string{"--despite-red recorded nothing", "baton: note — P0's latest job, j1 (`make test` on", "could not run, so it checked nothing"} {
		if !strings.Contains(out, want) {
			t.Errorf("done lacks %q:\n%s", want, out)
		}
	}
	if st, _ := s.store().Load(); len(st.Decisions) != 0 || st.Phases["P0"].Status != state.PhaseDone {
		t.Errorf("decisions %+v, P0 %+v", st.Decisions, st.Phases["P0"])
	}
}
