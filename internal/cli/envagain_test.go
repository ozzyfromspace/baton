package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A whole run of a gate whose last whole run lost a stage to the machine is questioned once per command and
// commit, with the stage, its line and the ways out; the same call again is queued. --why answers it, its
// reason told to the human, and for a long last run as a repeat. A --failed rerun, and a gate whose last
// whole run lost nothing to its environment, are not asked.
func TestAWholeRunAfterAKilledStageIsQuestionedOnce(t *testing.T) {
	const (
		notes = "--notes=the budgets are fixed"
		oom   = "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"
	)
	m := time.Minute
	killedLog := "::baton stage unit start\n--- FAIL: TestLogin (0.20s)\n::baton stage unit failed exit=1 seconds=95\n" +
		"::baton stage check-bundle-budgets start\nnuxt build\n" + oom + "\n::baton stage check-bundle-budgets failed exit=134 seconds=300\n"
	unit := lane.Stage{Name: "unit", Status: lane.StageFailed, Exit: 1, Failures: []string{"TestLogin"}, Runner: "go"}
	build := lane.Stage{Name: "check-bundle-budgets", Status: lane.StageFailed, Exit: 134, Unnarrowed: lane.UnnarrowedNoIDs}
	question := "Its last whole run, job j1, lost `check-bundle-budgets` to the machine (`" + oom + "`). " +
		"Rerun it alone with `baton verify --failed`. A whole run puts it beside the heaviest stages again, so before one, give it room: " +
		"order the gate so it runs apart, or free memory, and record what it needs with `baton env`."
	for _, c := range []struct {
		name   string
		log    string
		stages []lane.Stage
		took   time.Duration
		check  func(t *testing.T, s *session, root string)
	}{
		{name: "asked once, then queued", log: killedLog, stages: []lane.Stage{unit, build}, took: 5 * m,
			check: func(t *testing.T, s *session, root string) {
				errs := s.fails("verify", "bin/qa-set", notes)
				if !strings.Contains(errs, "not queued, this once. This gate spends 5m of the human's time; to you it is one tool call.") ||
					!strings.Contains(errs, "- "+question+"\n") || !strings.Contains(errs, "queue it again with `--why \"…\"`") {
					t.Errorf("refused with:\n%s", errs)
				}
				if s.state().Job("j2") != nil {
					t.Fatalf("queued: %+v", s.state().Job("j2"))
				}
				if !s.logged("gate_questioned", map[string]any{"type": decide.GateEnvAgain, "command": "bin/qa-set", "phase": "P0", "commit": headOf(t, root), "job": "j1"}) {
					t.Errorf("events:\n%s", s.events())
				}
				out := s.must("", "verify", "bin/qa-set", notes)
				if j := s.state().Job("j2"); j == nil || j.Unexplained != "" || strings.Contains(out, "told the human") {
					t.Fatalf("j2 %+v, said:\n%s", j, out)
				}
				if q := s.questioned(); !slices.Equal(q, []string{decide.GateEnvAgain}) {
					t.Errorf("questioned %v", q)
				}
			}},
		{name: "--why answers it", log: killedLog, stages: []lane.Stage{unit, build}, took: 5 * m,
			check: func(t *testing.T, s *session, root string) {
				s.must("", "verify", "bin/qa-set", "--why", "the build runs apart now", notes)
				const line = "P0 runs `bin/qa-set` over baton's question (its last whole run lost a stage to its environment): the build runs apart now"
				st := s.state()
				if j := st.Job("j2"); j == nil || j.Why != "the build runs apart now" {
					t.Fatalf("j2 %+v", j)
				}
				if !slices.Contains(st.Run.Announce, "baton: "+line) {
					t.Errorf("announced %q", st.Run.Announce)
				}
				if !slices.Equal(s.answered(), []string{decide.GateEnvAgain}) || len(s.questioned()) != 0 {
					t.Errorf("answered %v, questioned %v", s.answered(), s.questioned())
				}
				if !slices.Contains(st.Run.GatesQuestioned, gateKey(decide.GateEnvAgain, "bin/qa-set", headOf(t, root))) {
					t.Errorf("not recorded as asked: %q", st.Run.GatesQuestioned)
				}
			}},
		{name: "a long repeat with --why is told", log: killedLog, stages: []lane.Stage{unit, build}, took: 41 * m,
			check: func(t *testing.T, s *session, root string) {
				errs := s.fails("verify", "bin/qa-set", notes)
				if !strings.Contains(errs, "- A repeat: job j1 ran `bin/qa-set` for 41m") || !strings.Contains(errs, "- "+question+"\n") {
					t.Errorf("refused with:\n%s", errs)
				}
				s.must("", "verify", "bin/qa-set", "--why", "the build runs apart now", notes)
				if !s.logged("gate_repeated", map[string]any{"job": "j2", "last": "j1", "why": "the build runs apart now"}) {
					t.Errorf("events:\n%s", s.events())
				}
				if a := s.answered(); !slices.Contains(a, decide.GateEnvAgain) || !slices.Contains(a, decide.GateRepeat) {
					t.Errorf("answered %v", a)
				}
			}},
		{name: "a new commit asks again", log: killedLog, stages: []lane.Stage{unit, build}, took: 5 * m,
			check: func(t *testing.T, s *session, root string) {
				s.fails("verify", "bin/qa-set", notes)
				gittest.Write(t, root, "bin/order", "build apart\n")
				gittest.Commit(t, root, "order", "bin/order")
				if errs := s.fails("verify", "bin/qa-set", notes); !strings.Contains(errs, question) {
					t.Errorf("refused with:\n%s", errs)
				}
			}},
		{name: "a rerun is not asked", log: killedLog, stages: []lane.Stage{unit, build}, took: 5 * m,
			check: func(t *testing.T, s *session, root string) {
				s.must("", "verify", "--failed", notes)
				if j := s.state().Job("j2"); j == nil || j.Reruns != "j1" || len(s.questioned()) != 0 {
					t.Errorf("j2 %+v, questioned %v", j, s.questioned())
				}
			}},
		{name: "nothing lost", log: "::baton stage unit start\n--- FAIL: TestLogin (0.20s)\n::baton stage unit failed exit=1 seconds=95\n",
			stages: []lane.Stage{unit}, took: 5 * m,
			check: func(t *testing.T, s *session, root string) {
				s.must("", "verify", "bin/qa-set", notes)
				if s.state().Job("j2") == nil || len(s.questioned()) != 0 {
					t.Errorf("questioned %v", s.questioned())
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := gitSession(t)
			gittest.Write(t, root, "src/budgets.go", "package src\n")
			gittest.Commit(t, root, "budgets", "src/budgets.go")
			s.must("", "verify", "bin/qa-set", "--notes", "the budgets are in")
			log := s.store().JobLog("j1")
			os.MkdirAll(filepath.Dir(log), 0o755)
			if err := os.WriteFile(log, []byte(c.log), 0o644); err != nil {
				t.Fatal(err)
			}
			s.staged("j1", state.JobFailed, 1, c.stages...)
			s.set(func(x *state.State) {
				j := x.Job("j1")
				j.Started, j.Ran, j.Ended = s.now.Add(-c.took-m), s.now.Add(-c.took-m), s.now.Add(-m)
			})
			c.check(t, s, root)
		})
	}
}
