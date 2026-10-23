package cli

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// qaPhase makes phase id of the session's plan a QA phase (Type: qa).
func (s *session) qaPhase(id string) {
	s.t.Helper()
	pl, err := s.store().LoadPlan()
	if err != nil {
		s.t.Fatal(err)
	}
	pl.Phases[pl.Index(id)].Type = plan.TypeQA
	if err := s.store().WritePlan(pl); err != nil {
		s.t.Fatal(err)
	}
}

// In a QA phase, verify of its red gate's own command reruns what failed in it, as --failed does, saying so
// first, then what it carries and narrows; outside a QA phase the same command runs whole, as it did.
// --whole with --why runs it whole, the human told; --whole alone is refused, saying why.
func TestSlimRunsInAQAPhase(t *testing.T) {
	const notes = "--notes=the parser is fixed"
	const slim = "baton: P0's red gate is `make e2e` (job j1): in a QA phase it reruns what failed until all of it passes. " +
		"One full run follows at `baton done` only if files beyond the failing tests changed since the full run. " +
		"To run it whole now, add `--whole --why \"…\"`: baton tells the human your reason.\n"
	for _, c := range []struct {
		name  string
		qa    bool
		args  []string
		check func(t *testing.T, s *session, out string)
	}{
		{name: "the same command reruns what failed", qa: true, args: []string{"verify", "make", "e2e", notes},
			check: func(t *testing.T, s *session, out string) {
				at, queued := strings.Index(out, slim), strings.Index(out, "baton: queued j2 — `make e2e` (P0)")
				if at < 0 || queued < at || !strings.Contains(out, "baton: it reruns what failed in job j1:\n") ||
					!strings.Contains(out, "- narrowed to what failed there: `two` (TestA)\n") {
					t.Errorf("verify says:\n%s", out)
				}
				j := s.state().Job("j2")
				if j == nil || j.Reruns != "j1" || j.Command != "make e2e" || !j.QAPhase || j.Why != "" {
					t.Fatalf("j2 %+v", j)
				}
				if _, err := os.Stat(s.store().JobRerun("j2")); err != nil {
					t.Errorf("the record j2 runs from: %v", err)
				}
				if !s.logged("job_queued", map[string]any{"job": "j2", "reruns": "j1", "slim": true, "qa_phase": true}) || s.logged("gate_whole", nil) {
					t.Errorf("events:\n%s", s.events())
				}
			}},
		{name: "outside a QA phase, the same command runs whole", args: []string{"verify", "make", "e2e", notes},
			check: func(t *testing.T, s *session, out string) {
				if strings.Contains(out, "in a QA phase") || strings.Contains(out, "it reruns what failed") {
					t.Errorf("verify says:\n%s", out)
				}
				if j := s.state().Job("j2"); j == nil || j.Reruns != "" || j.QAPhase {
					t.Fatalf("j2 %+v", j)
				}
				if s.logged("job_queued", map[string]any{"job": "j2", "slim": true}) {
					t.Errorf("events:\n%s", s.events())
				}
			}},
		{name: "--whole with --why runs it whole, told to the human", qa: true,
			args: []string{"verify", "--whole", "--why", "the fix touches every page", "make", "e2e", notes},
			check: func(t *testing.T, s *session, out string) {
				const line = "P0 runs `make e2e` whole, not rerunning what failed in job j1: the fix touches every page"
				if strings.Contains(out, slim) || strings.Contains(out, "it reruns what failed") || !strings.Contains(out, "baton told the human: "+line+".") {
					t.Errorf("verify says:\n%s", out)
				}
				st := s.state()
				j := st.Job("j2")
				if j == nil || j.Reruns != "" || j.Why != "the fix touches every page" || j.Unexplained != "" || !j.QAPhase {
					t.Fatalf("j2 %+v", j)
				}
				if !slices.Contains(st.Run.Announce, "baton: "+line) {
					t.Errorf("announced %q", st.Run.Announce)
				}
				if !s.logged("gate_whole", map[string]any{"job": "j2", "phase": "P0", "command": "make e2e", "red_gate": "j1", "why": "the fix touches every page"}) ||
					s.logged("job_queued", map[string]any{"job": "j2", "slim": true}) {
					t.Errorf("events:\n%s", s.events())
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := redStagedGate(t)
			if c.qa {
				s.qaPhase("P0")
			}
			c.check(t, s, s.must("", c.args...))
		})
	}

	t.Run("--whole alone is refused", func(t *testing.T) {
		s, _, _ := redStagedGate(t)
		s.qaPhase("P0")
		errs := s.fails("verify", "--whole", "make", "e2e", notes)
		if !strings.Contains(errs, "not queued — P0's red gate is `make e2e` (job j1): in a QA phase the same command reruns what failed in it until all of it passes. "+
			"--whole runs it whole instead only with `--why \"<what a whole run can show that a rerun of what failed cannot>\"`") {
			t.Errorf("refused with:\n%s", errs)
		}
		if s.state().Job("j2") != nil || !s.logged("refused", map[string]any{"command": "verify", "why": "whole with no why"}) {
			t.Errorf("queued %+v; events:\n%s", s.state().Job("j2"), s.events())
		}
		if errs := s.fails("verify", "--failed", "--whole", notes); !strings.Contains(errs, "usage: baton verify") {
			t.Errorf("--failed with --whole: %s", errs)
		}
	})
}

// A QA phase's red gate that recorded no stages keeps today's path: the same command again is a repeat,
// and baton's question offers the narrowed rerun of what failed.
func TestSlimRunsLeaveAnUnstagedGateAsItWas(t *testing.T) {
	s, _ := gitSession(t)
	s.must("", "verify", "make test", "--notes", "the parser is done")
	s.ranAt("j1", state.JobFailed, 40*time.Minute, 12*time.Minute, goRed)
	s.qaPhase("P0")
	errs := s.fails("verify", "make test", "--notes", "the parser is fixed")
	if !strings.Contains(errs, "rerun only what failed in j1 (TestParse, TestLex): `baton verify --failed --notes \"…\" -- go test -run '^(TestParse|TestLex)$' ex.com/m/src`") ||
		strings.Contains(errs, "in a QA phase") {
		t.Errorf("refused with:\n%s", errs)
	}
	if s.state().Job("j2") != nil {
		t.Errorf("queued %+v", s.state().Job("j2"))
	}
}
