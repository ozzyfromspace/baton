package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
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
	}
	s.must("", "verify", "make", "e2e", "--notes", "again")
	st := s.state()
	if st.Job("j1") != nil || st.Job("j2") == nil || len(st.KeptLogs) != 1 || st.KeptLogs[0] != "j1" {
		t.Fatalf("jobs %d, kept logs %v", len(st.Jobs), st.KeptLogs)
	}
	if _, err := os.Stat(filepath.Join(logs, "j1.log")); err != nil {
		t.Errorf("the failed job's log went: %v", err)
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
