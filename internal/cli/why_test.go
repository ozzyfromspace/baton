package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red go gate's log, built from no stages: two tests failed in one package.
const goRed = "=== RUN   TestParse\n--- FAIL: TestParse (0.01s)\n=== RUN   TestLex\n--- FAIL: TestLex (0.00s)\nFAIL\nFAIL\tex.com/m/src\t0.4s\n"

// ranAt records job id's end, as its runner would: its command began ago before now and ran for took,
// leaving log.
func (s *session) ranAt(id, status string, ago, took time.Duration, log string) {
	s.t.Helper()
	path := s.store().JobLog(id)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.set(func(x *state.State) {
		j := x.Job(id)
		j.Status, j.Started, j.Ran = status, s.now.Add(-ago), s.now.Add(-ago)
		j.Ended = j.Ran.Add(took)
		if status != state.JobPassed {
			j.Exit = 1
		}
	})
}

// answered is what the latest job_queued event says --why answered.
func (s *session) answered() []string {
	var kinds []string
	for _, line := range strings.Split(s.events(), "\n") {
		var ev struct {
			Kind     string   `json:"kind"`
			Answered []string `json:"answered"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == "job_queued" {
			kinds = ev.Answered
		}
	}
	return kinds
}

// A long repeat states its reason: baton's question leads with what the gate spends of the human's time and
// what the phase spent so far, names what failed and the narrower rerun, and asks for --why. A repeat
// queued with --why runs, its reason told to the human; one queued again without runs too, flagged to the
// human and in its result. A gate baton does not question needs no --why.
func TestARepeatStatesItsReason(t *testing.T) {
	const notes = "--notes=the parser is fixed"
	m := time.Minute
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, s *session, root string) // after j1, `make test`, failed in 12m 40 minutes ago
		args  []string                                    // queued, after any refusals the setup leaves to it
		says  []string                                    // in what queueing it says
		check func(t *testing.T, s *session, out string)
	}{
		{name: "refused with the ids and the narrower command", setup: func(t *testing.T, s *session, root string) {
			s.must("", "verify", "make test", "--why", "the lexer fix", notes)
			s.ranAt("j2", state.JobFailed, 25*m, 12*m, goRed)
			gittest.Write(t, root, "src/parse.go", "package src\n")
			gittest.Commit(t, root, "parse", "src/parse.go")
			errs := s.fails("verify", "make test", notes)
			for _, want := range []string{
				"not queued, this once. This gate spends 12m of the human's time; to you it is one tool call. " +
					"P0 so far: 2 gate runs, 24m of gates in its 40m; all of them `make test`.\nbaton questions `make test` before it runs:\n",
				"- A repeat: job j2 ran `make test` for 12m on ",
				"Does a narrower check prove the change: the tests of that file, or only what failed in j2 (TestParse, TestLex): " +
					"`baton verify --failed --notes \"…\" -- go test -run '^(TestParse|TestLex)$' ex.com/m/src`, and a pass clears the red gate?",
				"To run the whole gate anyway, queue it again with `--why \"…\"`: baton tells the human your reason.",
			} {
				if !strings.Contains(errs, want) {
					t.Errorf("refused with:\n%s\nwant %q", errs, want)
				}
			}
			if strings.Contains(errs, "run the same command again") {
				t.Errorf("refused with:\n%s", errs)
			}
		}, args: []string{"make test", "--why", "TestLex needs the whole set"},
			says: []string{"baton told the human: P0 reruns `make test` (12m, run 3): TestLex needs the whole set."}},
		{name: "on the same code, the ids", setup: func(t *testing.T, s *session, root string) {
			errs := s.fails("verify", "make test", notes)
			want := "Running it whole again spends that time again to learn what its result already says: rerun only what failed in j1 (TestParse, TestLex): " +
				"`baton verify --failed --notes \"…\" -- go test -run '^(TestParse|TestLex)$' ex.com/m/src`, and a pass clears the red gate."
			if !strings.Contains(errs, want) {
				t.Errorf("refused with:\n%s\nwant %q", errs, want)
			}
		}, args: []string{"make test", "--why", "the fix touches every package"},
			says: []string{"baton told the human: P0 reruns `make test` (12m, run 2): the fix touches every package."}},
		{name: "--why queues it and is told", args: []string{"make test", "--why", "the lexer fix shows only in the whole set"},
			says: []string{"baton: queued j2", "baton told the human: P0 reruns `make test` (12m, run 2): the lexer fix shows only in the whole set."},
			check: func(t *testing.T, s *session, out string) {
				st := s.state()
				if j := st.Job("j2"); j == nil || j.Why != "the lexer fix shows only in the whole set" || j.Unexplained != "" {
					t.Errorf("j2: %+v", j)
				}
				if !slices.Contains(st.Run.Announce, "baton: P0 reruns `make test` (12m, run 2): the lexer fix shows only in the whole set") {
					t.Errorf("announced %q", st.Run.Announce)
				}
				if !s.logged("gate_repeated", map[string]any{"job": "j2", "phase": "P0", "command": "make test", "last": "j1",
					"why": "the lexer fix shows only in the whole set", "took_seconds": 720.0, "run": 2.0}) {
					t.Errorf("events:\n%s", s.events())
				}
				if !s.logged("job_queued", map[string]any{"job": "j2", "why": "the lexer fix shows only in the whole set"}) ||
					!slices.Equal(s.answered(), []string{"repeat", "unstaged"}) {
					t.Errorf("events:\n%s", s.events())
				}
				if q := s.questioned(); len(q) != 0 || s.logged("gate_unexplained", nil) {
					t.Errorf("questioned %v:\n%s", q, s.events())
				}
			}},
		{name: "a bare resubmit queues it, flagged", setup: func(t *testing.T, s *session, root string) {
			s.fails("verify", "make test", notes)
		}, args: []string{"make test"},
			says: []string{"baton: queued j2", "baton told the human: P0 reran `make test` (12m, run 2, 12m so far) without saying why."},
			check: func(t *testing.T, s *session, out string) {
				const line = "P0 reran `make test` (12m, run 2, 12m so far) without saying why"
				st := s.state()
				j := st.Job("j2")
				if j == nil || j.Unexplained != line || j.Why != "" {
					t.Fatalf("j2: %+v", j)
				}
				if !slices.Contains(st.Run.Announce, "baton: "+line) {
					t.Errorf("announced %q", st.Run.Announce)
				}
				if !s.logged("gate_unexplained", map[string]any{"job": "j2", "last": "j1", "run": 2.0, "so_far_seconds": 720.0}) || s.logged("gate_repeated", nil) {
					t.Errorf("events:\n%s", s.events())
				}
				if r := decide.JobResult(*j, decide.Log{}, nil, ""); !strings.HasPrefix(r, "[baton] "+line+"; the human was told.") {
					t.Errorf("its result:\n%s", r)
				}
			}},
		{name: "a gate not questioned needs no --why", args: []string{"go test ./src"},
			check: func(t *testing.T, s *session, out string) {
				if strings.Contains(out, "told the human") || s.logged("gate_repeated", nil) || s.logged("gate_unexplained", nil) || len(s.questioned()) != 0 {
					t.Errorf("said:\n%s\nevents:\n%s", out, s.events())
				}
				if j := s.state().Job("j2"); j == nil || j.Unexplained != "" {
					t.Errorf("j2: %+v", j)
				}
			}},
		{name: "--why on a gate not questioned is kept, and told to nobody", args: []string{"go test ./src", "--why", "the parser's tests"},
			check: func(t *testing.T, s *session, out string) {
				if strings.Contains(out, "told the human") || len(s.answered()) != 0 {
					t.Errorf("said:\n%s\nevents:\n%s", out, s.events())
				}
				if j := s.state().Job("j2"); j == nil || j.Why != "the parser's tests" || !s.logged("job_queued", map[string]any{"job": "j2", "why": "the parser's tests"}) {
					t.Errorf("j2: %+v\n%s", j, s.events())
				}
			}},
		{name: "--why answers another question", args: []string{"sh /tmp/gate.sh", "--why", "the deploy check lives outside on purpose"},
			says: []string{"baton told the human: P0 runs `sh /tmp/gate.sh` over baton's question (it runs a script in no commit): the deploy check lives outside on purpose."},
			check: func(t *testing.T, s *session, out string) {
				if !slices.Equal(s.answered(), []string{"outside_script"}) || s.logged("gate_repeated", nil) || len(s.questioned()) != 0 {
					t.Errorf("events:\n%s", s.events())
				}
			}},
		{name: "an await keeps its --why", args: []string{"gh run watch", "--why", "CI runs on three systems"},
			check: func(t *testing.T, s *session, out string) {
				if j := s.state().Job("j2"); j == nil || j.Why != "CI runs on three systems" {
					t.Errorf("j2: %+v", j)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := gitSession(t)
			s.must("", "verify", "make test", notes)
			s.ranAt("j1", state.JobFailed, 40*m, 12*m, goRed)
			if c.setup != nil {
				c.setup(t, s, root)
			}
			kind := "verify"
			if c.args[0] == "gh run watch" {
				kind = "await"
			}
			out := s.must("", append(append([]string{kind}, c.args...), notes)...)
			for _, want := range c.says {
				if !strings.Contains(out, want) {
					t.Errorf("said:\n%s\nwant %q", out, want)
				}
			}
			if c.check != nil {
				c.check(t, s, out)
			}
		})
	}
}

// A phase agent's run is watched by nobody: what the human is told of its gate goes to the lead's session.
func TestAWorkersRepeatIsToldInTheLeadsSession(t *testing.T) {
	w, lead := asWorker(t)
	tellLead(w.store(), "P1 reruns `make test` (12m, run 2): the lexer fix")
	if a := lead.state().Run.Announce; !slices.Contains(a, "baton: P1 reruns `make test` (12m, run 2): the lexer fix") {
		t.Errorf("the lead's announcements: %q", a)
	}
	tellLead(lead.store(), "nothing") // the lead's own run has it already
	if a := lead.state().Run.Announce; slices.Contains(a, "baton: nothing") {
		t.Errorf("the lead's announcements: %q", a)
	}
}
