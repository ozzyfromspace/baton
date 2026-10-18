package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// ranFor records job id's end after its command ran for took, as its runner would, with stages if given
// (a gate built from them) or a log of no stage marker.
func (s *session) ranFor(id, status string, took time.Duration, stages ...lane.Stage) {
	s.t.Helper()
	log := s.store().JobLog(id)
	os.MkdirAll(filepath.Dir(log), 0o755)
	if err := os.WriteFile(log, []byte("ok  \tsrc\t1.2s\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.set(func(x *state.State) {
		j := x.Job(id)
		j.Status, j.Started, j.Ran = status, s.now.Add(-2*time.Hour), s.now.Add(-2*time.Hour+time.Minute)
		j.Ended, j.Stages = j.Ran.Add(took), stages
		if status != state.JobPassed {
			j.Exit = 1
		}
	})
}

// qaLast makes the plan's last phase its QA phase.
func qaLast(t *testing.T, s *session) {
	pl, err := s.store().LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	pl.Phases[len(pl.Phases)-1].Type = plan.TypeQA
	if err := s.store().WritePlan(pl); err != nil {
		t.Fatal(err)
	}
}

// questioned lists the types of the gate_questioned events logged, oldest first.
func (s *session) questioned() []string {
	var types []string
	for _, line := range strings.Split(s.events(), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && ev["kind"] == "gate_questioned" {
			types = append(types, fmt.Sprint(ev["type"]))
		}
	}
	return types
}

var staged = []lane.Stage{{Name: "unit", Status: lane.StagePassed}}

// A gate out of proportion to the change is questioned once as it is queued, each kind of question once
// per command and commit, and the same call again queues it. A first run, or a short one, is never asked.
func TestAGateOutOfProportionIsQuestionedOnce(t *testing.T) {
	const notes = "--notes=the parser is done"
	m := time.Minute
	for _, c := range []struct {
		name  string
		qa    bool                           // the plan's last phase is a QA phase
		setup func(s *session, root string)  // the run before the gate is queued
		args  []string                       // the gate
		want  []string                       // the kinds asked
		says  []string                       // what the refusal says
		after func(t *testing.T, out string) // what queueing it the second time says
	}{
		{name: "a first run", args: []string{"make e2e"}},
		{name: "a short run before", setup: func(s *session, root string) {
			s.must("", "verify", "make e2e", notes)
			s.ranFor("j1", state.JobPassed, 9*m, staged...)
		}, args: []string{"make e2e"}},
		{name: "a repeat that passed on this commit", setup: func(s *session, root string) {
			s.must("", "verify", "make e2e", notes)
			s.ranFor("j1", state.JobPassed, 31*m, staged...)
		}, args: []string{"make e2e"}, want: []string{"repeat"},
			says: []string{"not queued, this once — baton questions `make e2e` before it runs",
				"- A repeat: job j1 ran `make e2e` for 31m on this same code, and it passed. Running it again proves nothing new",
				"To run this gate as it is, run the same command again"}},
		{name: "a red repeat on this commit", setup: func(s *session, root string) {
			s.must("", "verify", "make e2e", notes)
			s.ranFor("j1", state.JobFailed, 31*m, lane.Stage{Name: "unit", Status: lane.StageFailed, Failures: []string{"TestParse"}})
		}, args: []string{"make e2e"}, want: []string{"repeat"},
			says: []string{"and it failed. Running it whole again spends that time again",
				"`baton verify --failed --notes \"…\"` reruns only what failed, carrying the stages that passed."}},
		{name: "a repeat on another commit, only tests changed since a full run passed", setup: func(s *session, root string) {
			s.must("", "verify", "make e2e", notes)
			s.ranFor("j1", state.JobPassed, 20*m, staged...)
			gittest.Write(t, root, "src/parse_test.go", "package src\n")
			gittest.Commit(t, root, "a test", "src/parse_test.go")
			s.fails("verify", "make e2e", notes) // asked as a repeat
			s.must("", "verify", "make e2e", notes)
			s.ranFor("j2", state.JobFailed, 22*m, lane.Stage{Name: "unit", Status: lane.StageFailed, Failures: []string{"TestParse"}})
			gittest.Write(t, root, "src/x_test.go", "package src\n")
			gittest.Write(t, root, "docs/parse.md", "# parse\n")
			gittest.Commit(t, root, "another", "src/x_test.go", "docs/parse.md")
		}, args: []string{"make e2e"}, want: []string{"repeat"},
			says: []string{"- A repeat: job j2 ran `make e2e` for 22m on ", ", and it failed. Since then 2 files changed: docs/parse.md, src/x_test.go. " +
				"Does a narrower check prove the change: the tests of those files, or `baton verify --failed --notes \"…\"`, which reruns only what failed in j2",
				"Code outside the tests is unchanged since job j1 passed in full on ", "If what is red is on the tests' side, finish with `baton done P0 --despite-red \"<why>\"`."}},
		{name: "the files changed, at most 20", setup: func(s *session, root string) {
			s.must("", "verify", "make e2e", notes)
			s.ranFor("j1", state.JobPassed, 20*m, staged...)
			var paths []string
			for i := range 25 {
				p := fmt.Sprintf("src/f%02d.go", i)
				gittest.Write(t, root, p, "package src\n")
				paths = append(paths, p)
			}
			gittest.Commit(t, root, "many", paths...)
		}, args: []string{"make e2e"}, want: []string{"repeat"},
			says: []string{"Since then 25 files changed: src/f00.go, src/f01.go", "src/f19.go and 5 more. Does a narrower check prove the change: the tests of those files?"}},
		{name: "a script outside the repository", args: []string{"sh /tmp/gate.sh"}, want: []string{"outside_script"},
			says: []string{"- A script outside the repository: `/tmp/gate.sh` is in no commit", "Commit it in the repository first"}},
		{name: "a script in the repository, and a program", args: []string{"sh scripts/gate.sh && /usr/local/bin/golangci-lint run ./src"}},
		{name: "a long gate not built from stages", setup: func(s *session, root string) {
			s.must("", "verify", "./scripts/gate.sh", notes)
			s.ranFor("j1", state.JobPassed, 12*m)
			gittest.Write(t, root, "src/y.go", "package src\n")
			gittest.Commit(t, root, "y", "src/y.go")
		}, args: []string{"./scripts/gate.sh"}, want: []string{"repeat", "unstaged"},
			says: []string{"- Not built from stages: job j1 ran it for 12m, and its log has no stage marker",
				"`baton stage <name> -- <command>`"}},
		{name: "a whole suite before the QA phase", qa: true, args: []string{"make test"}, want: []string{"long_before_qa"},
			says: []string{"- A long gate before QA: it runs a whole suite, and P0 is not a QA phase. Phases run scoped checks that finish in seconds or minutes",
				"A full check you run early on purpose is flagged: baton verify --qa \"<why now>\" --notes \"…\" -- make test."},
			after: func(t *testing.T, out string) {
				if strings.Contains(out, "Is this a focused check") {
					t.Errorf("asked again as it was queued:\n%s", out)
				}
			}},
		{name: "a gate whose last run was long, before the QA phase", qa: true, setup: func(s *session, root string) {
			s.must("", "verify", "./scripts/gate.sh", notes)
			s.ranFor("j1", state.JobPassed, 91*m, staged...)
			gittest.Write(t, root, "src/y.go", "package src\n")
			gittest.Commit(t, root, "y", "src/y.go")
		}, args: []string{"./scripts/gate.sh"}, want: []string{"repeat", "long_before_qa"},
			says: []string{"- A long gate before QA: its last run took 1h31m, and P0 is not a QA phase"}},
		{name: "flagged as an early full check", qa: true, args: []string{"make test", "--qa", "the parser touches every package"}},
		{name: "with the QA question, in one refusal", qa: true, args: []string{"sh /tmp/gate.sh"}, want: []string{"outside_script", "qa_ask"},
			says: []string{"- A script outside the repository", "- Is this a focused check of what P0 changed, or a full suite, lint or end-to-end run that"},
			after: func(t *testing.T, out string) {
				if strings.Contains(out, "Is this a focused check") {
					t.Errorf("asked again as it was queued:\n%s", out)
				}
			}},
		{name: "a focused check before the QA phase", qa: true, args: []string{"go test ./src"},
			after: func(t *testing.T, out string) {
				if !strings.Contains(out, "Is this a focused check of what P0 changed") {
					t.Errorf("not asked as it was queued:\n%s", out)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := gitSession(t)
			if c.qa {
				qaLast(t, s)
			}
			if c.setup != nil {
				c.setup(s, root)
			}
			before := s.questioned()
			args := append(append([]string{"verify"}, c.args...), notes)
			jobs := len(s.state().Jobs)
			if len(c.want) > 0 {
				errs := s.fails(args...)
				for _, want := range c.says {
					if !strings.Contains(errs, want) {
						t.Errorf("refused with:\n%s\nwant %q", errs, want)
					}
				}
				if n := len(s.state().Jobs); n != jobs {
					t.Errorf("%d jobs, want %d: the gate is queued", n, jobs)
				}
				if got := s.questioned()[len(before):]; !slices.Equal(got, c.want) {
					t.Errorf("questioned %v, want %v:\n%s", got, c.want, s.events())
				}
				if q := s.state().Run.GatesQuestioned; len(q) < len(c.want) {
					t.Errorf("recorded %q", q)
				}
			}
			out := s.must("", args...) // the same call again, or the first that asks nothing
			if n := len(s.state().Jobs); n != jobs+1 {
				t.Errorf("%d jobs, want %d", n, jobs+1)
			}
			if len(c.want) == 0 && !slices.Equal(s.questioned(), before) {
				t.Errorf("questioned %v:\n%s", s.questioned(), s.events())
			}
			if c.after != nil {
				c.after(t, out)
			}
		})
	}
}
