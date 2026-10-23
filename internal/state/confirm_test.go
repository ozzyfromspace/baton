package state

import (
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// A QA phase's fixes since its full run need no full run again when they touched only Markdown and the
// failing tests' own files: a pytest, vitest, jest or Playwright test's file, by its id (from the runner's
// root or the repository's), and a `_test.go` file in a go package that failed. Any other file needs one.
func TestNeedsConfirm(t *testing.T) {
	goTest := lane.FailedTest{Runner: lane.RunGo, ID: "TestParse", Scope: "example.com/m/internal/parse"}
	for _, c := range []struct {
		name    string
		changed []string
		failed  []lane.FailedTest
		want    []string
	}{
		{"nothing changed", nil, []lane.FailedTest{goTest}, nil},
		{"a go test file of the failing package", []string{"internal/parse/parse_test.go"}, []lane.FailedTest{goTest}, nil},
		{"a go test file of another package", []string{"internal/lane/lane_test.go"}, []lane.FailedTest{goTest}, []string{"internal/lane/lane_test.go"}},
		{"code of the failing package", []string{"internal/parse/parse.go"}, []lane.FailedTest{goTest}, []string{"internal/parse/parse.go"}},
		{"a go package that failed whole", []string{"internal/parse/parse_test.go"},
			[]lane.FailedTest{{Runner: lane.RunGo, ID: "example.com/m/internal/parse", Scope: "example.com/m/internal/parse", Whole: true}}, nil},
		{"a go test whose package is not known", []string{"internal/parse/parse_test.go"}, []lane.FailedTest{{Runner: lane.RunGo, ID: "TestParse"}},
			[]string{"internal/parse/parse_test.go"}},
		{"a go test file at the root", []string{"main_test.go"}, []lane.FailedTest{{Runner: lane.RunGo, ID: "TestMain", Scope: "example.com/m"}}, []string{"main_test.go"}},
		{"Markdown only", []string{"README.md", "docs/design.markdown", "CHANGELOG.MD"}, nil, nil},
		{"a Playwright test, from its own root", []string{"web/tests/login.spec.ts"},
			[]lane.FailedTest{{Runner: lane.RunPlaywright, ID: "tests/login.spec.ts:12", Scope: "chromium"}}, nil},
		{"a Playwright test's page", []string{"web/src/login.tsx", "web/tests/login.spec.ts"},
			[]lane.FailedTest{{Runner: lane.RunPlaywright, ID: "tests/login.spec.ts:12"}}, []string{"web/src/login.tsx"}},
		{"a pytest file", []string{"tests/test_x.py"}, []lane.FailedTest{{Runner: lane.RunPytest, ID: "tests/test_x.py::test_a[p]"}}, nil},
		{"a vitest file", []string{"src/a.test.ts"}, []lane.FailedTest{{Runner: lane.RunVitest, ID: "src/a.test.ts > suite > name"}}, nil},
		{"a jest file by its whole path", []string{"src/c.test.js"}, []lane.FailedTest{{Runner: lane.RunJest, ID: "/home/u/p/src/c.test.js > name"}}, nil},
		{"a fail line's id names no file", []string{"tests/x.spec.ts"}, []lane.FailedTest{{ID: "tests/x.spec.ts"}}, []string{"tests/x.spec.ts"}},
		{"code, a test and Markdown", []string{"README.md", "internal/parse/parse.go", "internal/parse/parse_test.go"}, []lane.FailedTest{goTest},
			[]string{"internal/parse/parse.go"}},
	} {
		if got := NeedsConfirm(c.changed, c.failed); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// What a confirmation goes back to: a QA phase's gate that a rerun took over from, passed or still running,
// and the full run its reruns go back to; none for a gate that ran whole, is red, or checked nothing. A full
// run gone from the record leaves the gate's command, when a staged rerun ran it.
func TestConfirmFrom(t *testing.T) {
	whole := Job{ID: "j1", Phase: "P0", Command: "./qa.sh", Commit: "c1", Status: JobFailed, Exit: 1}
	rerun := func(id, of, status string) Job {
		return Job{ID: id, Phase: "P0", Command: "./qa.sh", Commit: "c2", Reruns: of, Status: status}
	}
	narrowed := Job{ID: "j2", Phase: "P0", Command: "go test -run '^TestA$' ./x", Reruns: "j1", Narrows: []string{"TestA"}, Status: JobPassed}
	for _, c := range []struct {
		name     string
		jobs     []Job
		ok       bool
		gate, of string
		command  string // the full run's
	}{
		{"reruns passed", []Job{whole, rerun("j2", "j1", JobFailed), rerun("j3", "j2", JobPassed)}, true, "j3", "j1", "./qa.sh"},
		{"a narrowed rerun passed", []Job{whole, narrowed}, true, "j2", "j1", "./qa.sh"},
		{"a rerun runs", []Job{whole, rerun("j2", "j1", JobRunning)}, true, "j2", "j1", "./qa.sh"},
		{"the full run passed", []Job{whole, {ID: "j2", Phase: "P0", Command: "./qa.sh", Status: JobPassed}}, false, "", "", ""},
		{"a rerun failed", []Job{whole, rerun("j2", "j1", JobFailed)}, false, "", "", ""},
		{"a rerun was stopped", []Job{whole, rerun("j2", "j1", JobInterrupted)}, false, "", "", ""},
		{"the full run is gone", []Job{rerun("j2", "j1", JobFailed), rerun("j3", "j2", JobPassed)}, true, "j3", "", "./qa.sh"},
		{"the full run is gone, narrowed", []Job{narrowed}, true, "j2", "", ""},
	} {
		st := State{Jobs: c.jobs}
		g, w, ok := st.ConfirmFrom("P0")
		if ok != c.ok || g.ID != c.gate || w.ID != c.of || w.Command != c.command {
			t.Errorf("%s: gate %q, full run %q (`%s`), %v; want %q, %q (`%s`), %v", c.name, g.ID, w.ID, w.Command, ok, c.gate, c.of, c.command, c.ok)
		}
	}
}

// At a QA phase's done, a confirmation is queued at once when the rerun that cleared its gate passed: the
// phase verifies on it, done if it passes, its red gate if it fails. While the rerun runs, the
// confirmation is owed, and Settle queues it once the rerun passes; a rerun that fails reopens the phase and
// owes nothing. A gate that is not the one it was worked out for records nothing.
func TestConfirmAtDoneAndSettle(t *testing.T) {
	p := graph("P0:", "P1:")
	t1, t2 := t0.Add(time.Hour), t0.Add(2*time.Hour)
	c := Confirm{Of: "j1", Command: "./qa.sh", Commit: "c3", Files: []string{"src/a.go"}, Notes: "confirm"}
	setup := func(status string) State {
		st := Attach(p, t0, Origin{})
		st.Jobs = []Job{
			{ID: "j1", Phase: "P0", Kind: JobVerify, Command: "./qa.sh", Commit: "c1", Status: JobFailed, Exit: 1, QAPhase: true},
			{ID: "j2", Phase: "P0", Kind: JobVerify, Command: "./qa.sh", Commit: "c2", Reruns: "j1", Status: status, QAPhase: true},
		}
		st.JobSeq = 2
		return st
	}
	for _, c2 := range []struct {
		name    string
		rerun   string // j2's status at the done
		then    string // j2's status once it ends ("" when it had ended)
		confirm string // the confirmation's status
		phase   string // P0's, at the end
	}{
		{"queued at the done, passes", JobPassed, "", JobPassed, PhaseDone},
		{"queued at the done, fails", JobPassed, "", JobFailed, PhaseParked},
		{"owed while the rerun runs, passes", JobRunning, JobPassed, JobPassed, PhaseDone},
		{"owed while the rerun runs, fails", JobRunning, JobPassed, JobFailed, PhaseParked},
		{"owed, and the rerun fails", JobRunning, JobFailed, "", PhaseParked},
	} {
		st := setup(c2.rerun)
		if _, ok := ConfirmAtDone(&st, "P0", c, "j9", t0); ok || st.Phases["P0"].Confirm != nil || len(st.Jobs) != 2 {
			t.Fatalf("%s: recorded for another gate: %+v", c2.name, st.Phases["P0"].Confirm)
		}
		q, ok := ConfirmAtDone(&st, "P0", c, "j2", t0)
		if !ok {
			t.Fatalf("%s: not recorded", c2.name)
		}
		if _, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil {
			t.Fatal(err)
		}
		ps := st.Phases["P0"]
		if c2.then == "" {
			if q.ID != "j3" || !q.Requeue || !q.QAPhase || q.Command != "./qa.sh" || q.Commit != "c3" || q.Notes != "confirm" ||
				ps.Confirm == nil || ps.Confirm.Job != "j3" || ps.Confirm.After != "" || !slices.Equal(ps.On, []string{"j3"}) || ps.Status != PhaseVerifying {
				t.Fatalf("%s: queued %+v, P0 %+v, confirm %+v", c2.name, q, ps, ps.Confirm)
			}
		} else {
			if q.ID != "" || ps.Confirm == nil || ps.Confirm.After != "j2" || ps.Confirm.Job != "" || !slices.Equal(ps.On, []string{"j2"}) {
				t.Fatalf("%s: queued %+v, P0 %+v, confirm %+v", c2.name, q, ps, ps.Confirm)
			}
			st.Job("j2").Status = c2.then
			got := Settle(&st, p, t1)
			if c2.then == JobFailed {
				if len(got.Confirms) != 0 || ps.Status != PhaseParked || len(st.Jobs) != 2 {
					t.Errorf("%s: settled %+v, P0 %+v", c2.name, got, ps)
				}
				continue
			}
			if len(got.Confirms) != 1 || got.Confirms[0].ID != "j3" || len(got.Verdicts) != 0 || ps.Status != PhaseVerifying ||
				!slices.Equal(ps.On, []string{"j3"}) || ps.Confirm.Job != "j3" || st.Job("j2").Delivered.IsZero() || !st.Job("j3").Requeue {
				t.Fatalf("%s: settled %+v, P0 %+v, confirm %+v", c2.name, got, ps, ps.Confirm)
			}
		}
		st.Job("j3").Status = c2.confirm
		got := Settle(&st, p, t2)
		if len(got.Confirms) != 0 || len(got.Verdicts) != 1 || got.Verdicts[0].Job.ID != "j3" || ps.Status != c2.phase {
			t.Errorf("%s: settled %+v, P0 %+v", c2.name, got, ps)
		}
		if g := st.RedGate("P0"); (c2.confirm == JobFailed) != (g != nil && g.ID == "j3") {
			t.Errorf("%s: red gate %+v", c2.name, g)
		}
	}
}
