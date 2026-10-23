package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// wholeIn is the whole list of the latest event of kind: the stages a whole rerun's question was about.
func (s *session) wholeIn(kind string) []string {
	var whole []string
	for _, line := range strings.Split(s.events(), "\n") {
		var ev struct {
			Kind  string   `json:"kind"`
			Whole []string `json:"whole"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == kind {
			whole = ev.Whole
		}
	}
	return whole
}

// A --failed rerun that would run whole a red stage that took GateLong or more last time is questioned
// once per command and commit: why the stage runs whole, its last time, where the failures may be named,
// the rerun by name, and --why. The same call again is queued, flagged to the human and in its result; one
// with --why is queued at once, its reason told to the human. A short whole stage, a long one narrowed, and
// a long one that passed and is brought back are not questioned; a long red one brought back is.
func TestALongWholeRerunIsQuestionedOnce(t *testing.T) {
	const notes = "--notes=the drives are fixed"
	m := time.Minute
	stage := func(name, status string, took time.Duration, s lane.Stage) lane.Stage {
		s.Name, s.Status, s.Started = name, status, time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
		s.Ended = s.Started.Add(took)
		if status == lane.StageFailed {
			s.Exit = 1
		}
		return s
	}
	noIDs := lane.Stage{Unnarrowed: lane.UnnarrowedNoIDs}
	for _, c := range []struct {
		name   string
		stages []lane.Stage
		args   []string                                         // after verify --failed, before the notes
		says   string                                           // in what queueing it says, with no check
		check  func(t *testing.T, s *session, root, log string) // after the stages are recorded and the fix committed
	}{
		{name: "refused once, then queued, flagged", stages: []lane.Stage{stage("one", lane.StagePassed, 2*m, lane.Stage{}), stage("two", lane.StageFailed, 78*m, noIDs)},
			check: func(t *testing.T, s *session, root, log string) {
				errs := s.fails("verify", "--failed", notes)
				want := "not queued, this once. This gate spends 1h18m of the human's time; to you it is one tool call.\n" +
					"--failed reruns what failed in job j1 (`make e2e`), and would run this stage whole: `two` (1h18m last time; baton read no failure ids from it).\n" +
					"The log of the run it went red in may name what failed (" + log + ", or a log the stage writes of its own): " +
					"if it does, rerun those by name with `baton verify --replace --notes \"…\" -- <a command naming them>`, " +
					"which makes that command P0's gate in place of j1, and the human is told. " +
					"To run it whole anyway, give the same call again with `--why \"…\"`: baton tells the human your reason."
				if !strings.Contains(errs, want) {
					t.Errorf("refused with:\n%s\nwant\n%s", errs, want)
				}
				if s.state().Job("j2") != nil {
					t.Fatalf("queued: %+v", s.state().Job("j2"))
				}
				if !s.logged("gate_questioned", map[string]any{"type": "whole_rerun", "command": "make e2e", "phase": "P0", "commit": headOf(t, root),
					"job": "j1", "took_seconds": 4680.0}) || !slices.Equal(s.wholeIn("gate_questioned"), []string{"two"}) {
					t.Errorf("events:\n%s", s.events())
				}

				out := s.must("", "verify", "--failed", notes)
				const line = "P0 reran `two` of `make e2e` whole (1h18m last time, run 2, 1h20m so far) without saying why"
				for _, want := range []string{"baton: queued j2", "- run whole: `two` (1h18m last time; baton read no failure ids from it)\n",
					"baton told the human: " + line + "."} {
					if !strings.Contains(out, want) {
						t.Errorf("queued with:\n%s\nwant %q", out, want)
					}
				}
				st := s.state()
				j := st.Job("j2")
				if j == nil || j.Reruns != "j1" || j.Unexplained != line || j.Why != "" {
					t.Fatalf("j2: %+v", j)
				}
				if !slices.Contains(st.Run.Announce, "baton: "+line) {
					t.Errorf("announced %q", st.Run.Announce)
				}
				if !s.logged("gate_unexplained", map[string]any{"job": "j2", "phase": "P0", "command": "make e2e", "last": "j1", "took_seconds": 4680.0, "run": 2.0}) ||
					!slices.Equal(s.wholeIn("gate_unexplained"), []string{"two"}) || s.logged("gate_repeated", nil) {
					t.Errorf("events:\n%s", s.events())
				}
				if q := s.questioned(); !slices.Equal(q, []string{"whole_rerun"}) {
					t.Errorf("questioned %v", q)
				}
				if r := decide.JobResult(*j, decide.Log{}, nil, ""); !strings.HasPrefix(r, "[baton] "+line+"; the human was told.") {
					t.Errorf("its result:\n%s", r)
				}
			}},
		{name: "--why queues it, told to the human", stages: []lane.Stage{stage("two", lane.StageFailed, 78*m, noIDs)},
			args: []string{"--why", "the drives' fix touches every drive"},
			check: func(t *testing.T, s *session, root, log string) {
				const line = "P0 reruns `two` of `make e2e` whole (1h18m last time, run 2): the drives' fix touches every drive"
				st := s.state()
				j := st.Job("j2")
				if j == nil || j.Reruns != "j1" || j.Why != "the drives' fix touches every drive" || j.Unexplained != "" {
					t.Fatalf("j2: %+v", j)
				}
				if !slices.Contains(st.Run.Announce, "baton: "+line) {
					t.Errorf("announced %q", st.Run.Announce)
				}
				if !s.logged("gate_repeated", map[string]any{"job": "j2", "last": "j1", "why": "the drives' fix touches every drive", "took_seconds": 4680.0}) ||
					!slices.Equal(s.wholeIn("gate_repeated"), []string{"two"}) || s.logged("gate_unexplained", nil) {
					t.Errorf("events:\n%s", s.events())
				}
				if !slices.Equal(s.answered(), []string{"whole_rerun"}) || len(s.questioned()) != 0 {
					t.Errorf("answered %v, questioned %v", s.answered(), s.questioned())
				}
				if !slices.Contains(st.Run.GatesQuestioned, gateKey(decide.GateWholeRerun, "make e2e", headOf(t, root))) {
					t.Errorf("not recorded as asked: %q", st.Run.GatesQuestioned)
				}
			}},
		{name: "a new commit asks again", stages: []lane.Stage{stage("two", lane.StageFailed, 78*m, noIDs)},
			check: func(t *testing.T, s *session, root, log string) {
				s.fails("verify", "--failed", notes)
				gittest.Write(t, root, "src/drive.go", "package src // again\n")
				gittest.Commit(t, root, "again", "src/drive.go")
				if errs := s.fails("verify", "--failed", notes); !strings.Contains(errs, "not queued, this once.") {
					t.Errorf("refused with:\n%s", errs)
				}
				if q := s.questioned(); !slices.Equal(q, []string{"whole_rerun", "whole_rerun"}) {
					t.Errorf("questioned %v", q)
				}
			}},
		{name: "a short whole stage", stages: []lane.Stage{stage("two", lane.StageFailed, 9*m, noIDs)},
			says: "- run whole: `two` (9m last time; baton read no failure ids from it)\n"},
		{name: "a long stage narrowed", stages: []lane.Stage{stage("two", lane.StageFailed, 78*m, lane.Stage{Failures: []string{"TestA"}})},
			says: "- narrowed to what failed there: `two` (TestA)\n"},
		{name: "a long stage that passed, brought back", stages: []lane.Stage{stage("one", lane.StagePassed, 78*m, lane.Stage{}), stage("two", lane.StageFailed, m, noIDs)},
			args: []string{"--stages", "one"}, says: "- run whole: `one` (1h18m last time; brought back by --stages); `two` (1m last time; baton read no failure ids from it)\n"},
		{name: "a long red stage brought back", stages: []lane.Stage{stage("two", lane.StageFailed, 78*m, lane.Stage{Failures: []string{"TestA"}})},
			args: []string{"--stages", "two"},
			check: func(t *testing.T, s *session, root, log string) {
				errs := s.fails("verify", "--failed", "--stages", "two", notes)
				if !strings.Contains(errs, "and would run this stage whole: `two` (1h18m last time; brought back by --stages).") {
					t.Errorf("refused with:\n%s", errs)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := gitSession(t)
			gittest.Write(t, root, "src/drive.go", "package src\n")
			gittest.Commit(t, root, "drive", "src/drive.go")
			s.must("", "verify", "make", "e2e", "--notes", "the drives are done")
			s.staged("j1", state.JobFailed, 1, c.stages...)
			s.set(func(x *state.State) {
				j := x.Job("j1")
				j.Started, j.Ran, j.Ended = s.now.Add(-90*m), s.now.Add(-90*m), s.now.Add(-10*m)
			})
			gittest.Write(t, root, "src/drive.go", "package src // fixed\n")
			gittest.Commit(t, root, "fix", "src/drive.go")
			if c.check != nil {
				if len(c.args) > 0 && c.args[0] == "--why" {
					s.must("", append(append([]string{"verify", "--failed"}, c.args...), notes)...)
				}
				c.check(t, s, root, s.store().JobLog("j1"))
				return
			}
			out := s.must("", append(append([]string{"verify", "--failed"}, c.args...), notes)...)
			if j := s.state().Job("j2"); j == nil || j.Reruns != "j1" || j.Unexplained != "" {
				t.Fatalf("j2: %+v", j)
			}
			if !strings.Contains(out, c.says) || strings.Contains(out, "told the human") || len(s.questioned()) != 0 ||
				s.logged("gate_unexplained", nil) || s.logged("gate_repeated", nil) {
				t.Errorf("said:\n%s\nwant %q\nevents:\n%s", out, c.says, s.events())
			}
		})
	}
}
