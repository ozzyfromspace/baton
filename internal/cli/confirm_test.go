package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// qaRedGate is a session whose P0 is a QA phase: its full run, j1 (`make e2e`), went red on TestA of
// ex.com/m/src in stage two, and fix (path: content) is committed since. With qa false, P0 is no QA phase.
func qaRedGate(t *testing.T, qa bool, fix map[string]string) (*session, string) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	if qa {
		s.qaPhase("P0")
	}
	s.must("", "verify", "make", "e2e", "--notes", "the parser is done")
	s.staged("j1", state.JobFailed, 1, passedOne, failedTwo)
	log := s.store().JobLog("j1")
	os.MkdirAll(filepath.Dir(log), 0o755)
	if err := os.WriteFile(log, []byte("--- FAIL: TestA (0.01s)\nFAIL\tex.com/m/src\t0.4s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for p, body := range fix {
		gittest.Write(t, root, p, body)
		paths = append(paths, p)
	}
	gittest.Commit(t, root, "fix", paths...)
	return s, root
}

// settle applies finished jobs' verdicts, as the host's loop does (state.Settle).
func (s *session) settle() state.Settled {
	s.t.Helper()
	pl, err := s.store().LoadPlan()
	if err != nil {
		s.t.Fatal(err)
	}
	var out state.Settled
	s.set(func(x *state.State) { out = state.Settle(x, pl, s.now) })
	return out
}

// passedRerun is the record of a rerun of j1 that passed: `one` carried, `two` passed narrowed.
var passedRerun = []lane.Stage{{Name: "one", Status: lane.StageCarried, From: "j1"}, {Name: "two", Status: lane.StagePassed}}

// At the done of a QA phase whose gate reruns cleared, one full run of the gate's command confirms it when
// files beyond Markdown and the failing tests' own changed since the full run: queued by baton at HEAD, the
// phase verifying on it, done if it passes, its red gate if it fails, which the same command then reruns
// slim. With only the failing tests' files and Markdown changed, done goes on, saying so. A rerun still
// running at the done owes it, queued once the rerun passes. Outside a QA phase, nothing is confirmed.
func TestDoneConfirmsAQAPhasesReruns(t *testing.T) {
	code := map[string]string{"src/parse.go": "package src // fixed\n"}
	tests := map[string]string{"src/parse_test.go": "package src // TestA fixed\n", "README.md": "# fixed\n"}
	const confirming = "baton: P0 is verifying: one full run confirms it (j3), since files beyond the failing tests changed since the full run (j1): src/parse.go. " +
		"If it passes, P0 is done; if it fails, it is P0's red gate, and the same command reruns what failed in it.\n"

	for _, outcome := range []string{state.JobPassed, state.JobFailed} {
		t.Run("code changed, the confirmation "+outcome, func(t *testing.T) {
			s, root := qaRedGate(t, true, code)
			s.must("", "verify", "make", "e2e", "--notes", "TestA fixed")
			s.staged("j2", state.JobPassed, 0, passedRerun...)
			out := s.must("", "done", "P0", "--notes", "every red fixed")
			if !strings.Contains(out, confirming) {
				t.Errorf("done says:\n%s", out)
			}
			st := s.state()
			j, ps := st.Job("j3"), st.Phases["P0"]
			if j == nil || j.Command != "make e2e" || j.Commit != headOf(t, root) || !j.Requeue || !j.QAPhase || j.Reruns != "" ||
				!strings.HasPrefix(j.Notes, "baton ran `make e2e` whole once more after reruns cleared P0's gate") || !strings.HasSuffix(j.Notes, "Before that: TestA fixed") {
				t.Fatalf("j3 %+v", j)
			}
			if ps.Status != state.PhaseVerifying || !slices.Equal(ps.On, []string{"j3"}) || ps.Confirm == nil || ps.Confirm.Job != "j3" || ps.Confirm.Of != "j1" {
				t.Fatalf("P0 %+v, confirm %+v", ps, ps.Confirm)
			}
			if !slices.Contains(st.Run.Announce, "baton: one full run confirms P0's reruns (job j3, `make e2e`), since files beyond the failing tests changed since the full run (j1): src/parse.go") {
				t.Errorf("announced %q", st.Run.Announce)
			}
			if !s.logged("qa_confirm_queued", map[string]any{"phase": "P0", "job": "j3", "of": "j1", "command": "make e2e", "commit": headOf(t, root), "by": "done"}) ||
				!s.logged("phase_verifying", map[string]any{"phase": "P0", "job": "j3"}) {
				t.Errorf("events:\n%s", s.events())
			}

			s.staged("j3", outcome, map[string]int{state.JobPassed: 0, state.JobFailed: 1}[outcome], passedOne, failedTwo)
			if outcome == state.JobPassed {
				s.set(func(x *state.State) {
					x.Job("j3").Stages = []lane.Stage{passedOne, {Name: "two", Status: lane.StagePassed}}
				})
			}
			got := s.settle()
			st = s.state()
			if outcome == state.JobPassed {
				if st.Phases["P0"].Status != state.PhaseDone || len(got.Confirms) != 0 {
					t.Errorf("settled %+v, P0 %+v", got, st.Phases["P0"])
				}
				return
			}
			if g := st.RedGate("P0"); st.Phases["P0"].Status != state.PhaseParked || g == nil || g.ID != "j3" {
				t.Fatalf("P0 %+v, red gate %+v", st.Phases["P0"], g)
			}
			// Taken up again, the same command reruns what failed in the confirmation.
			s.set(func(x *state.State) {
				x.Current, x.BoundaryOwed = "P0", false
				x.Phases["P0"].Status = state.PhaseActive
			})
			if out := s.must("", "verify", "make", "e2e", "--notes", "TestA again"); !strings.Contains(out, "baton: it reruns what failed in job j3:\n") {
				t.Errorf("verify says:\n%s", out)
			}
			if j := s.state().Job("j4"); j == nil || j.Reruns != "j3" {
				t.Errorf("j4 %+v", j)
			}
		})
	}

	t.Run("only the failing test's file and Markdown changed", func(t *testing.T) {
		s, _ := qaRedGate(t, true, tests)
		s.must("", "verify", "make", "e2e", "--notes", "TestA fixed")
		s.staged("j2", state.JobPassed, 0, passedRerun...)
		out := s.must("", "done", "P0", "--notes", "every red fixed")
		if !strings.Contains(out, "baton: no full run follows P0's reruns: since the full run (j1), only Markdown and the failing tests' own files changed (2 files).\n") ||
			strings.Contains(out, "one full run") {
			t.Errorf("done says:\n%s", out)
		}
		st := s.state()
		if st.Phases["P0"].Status != state.PhaseDone || st.Phases["P0"].Confirm != nil || st.Job("j3") != nil {
			t.Errorf("P0 %+v, j3 %+v", st.Phases["P0"], st.Job("j3"))
		}
		if !s.logged("qa_confirm_skipped", map[string]any{"phase": "P0", "of": "j1", "gate": "j2", "changed": 2.0}) || s.logged("qa_confirm_queued", nil) {
			t.Errorf("events:\n%s", s.events())
		}
	})

	t.Run("owed while the rerun runs, queued once it passes", func(t *testing.T) {
		s, _ := qaRedGate(t, true, code)
		s.must("", "verify", "make", "e2e", "--notes", "TestA fixed")
		out := s.must("", "done", "P0", "--notes", "every red fixed")
		if !strings.Contains(out, "baton: P0 is verifying on j2, which reruns what failed; once it passes, one full run of `make e2e` confirms it, "+
			"since files beyond the failing tests changed since the full run (j1): src/parse.go. If either fails, P0 reopens, and the same command reruns what failed.\n") {
			t.Errorf("done says:\n%s", out)
		}
		st := s.state()
		if ps := st.Phases["P0"]; ps.Status != state.PhaseVerifying || !slices.Equal(ps.On, []string{"j2"}) || ps.Confirm == nil || ps.Confirm.After != "j2" || st.Job("j3") != nil {
			t.Fatalf("P0 %+v, confirm %+v", ps, ps.Confirm)
		}
		if !s.logged("qa_confirm_owed", map[string]any{"phase": "P0", "after": "j2", "of": "j1", "by": "done"}) {
			t.Errorf("events:\n%s", s.events())
		}
		s.staged("j2", state.JobPassed, 0, passedRerun...)
		got := s.settle()
		st = s.state()
		if len(got.Confirms) != 1 || got.Confirms[0].ID != "j3" || st.Phases["P0"].Status != state.PhaseVerifying || !st.Job("j3").Requeue {
			t.Errorf("settled %+v, P0 %+v", got, st.Phases["P0"])
		}
	})

	t.Run("outside a QA phase", func(t *testing.T) {
		s, _ := qaRedGate(t, false, code)
		s.must("", "verify", "--failed", "--notes", "TestA fixed")
		s.staged("j2", state.JobPassed, 0, passedRerun...)
		out := s.must("", "done", "P0", "--notes", "every red fixed")
		if strings.Contains(out, "full run") || s.state().Phases["P0"].Status != state.PhaseDone || s.logged("qa_confirm_skipped", nil) {
			t.Errorf("done says:\n%s\nevents:\n%s", out, s.events())
		}
	})
}
