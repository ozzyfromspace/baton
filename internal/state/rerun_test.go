package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// stageNames is "name:status" of each stage, with what it was carried from (name:status@from) and its
// failures (name:status[a b]).
func stageNames(stages []lane.Stage) []string {
	var out []string
	for _, s := range stages {
		n := s.Name + ":" + s.Status
		if s.From != "" {
			n += "@" + s.From
		}
		if len(s.Failures) > 0 {
			n += fmt.Sprint(s.Failures)
		}
		out = append(out, n)
	}
	return out
}

// What --failed reruns for a phase: its red gate, drawing on the later runs of its command stopped (or
// running) after a failure, whose records are newer; with no red gate, its latest gate stopped after a
// failure. Never a gate of another command after a red one, nor one with a verdict that cleared nothing.
func TestWhatFailedReruns(t *testing.T) {
	passed := func(name string) lane.Stage { return lane.Stage{Name: name, Status: lane.StagePassed} }
	failed := func(name string, ids ...string) lane.Stage {
		return lane.Stage{Name: name, Status: lane.StageFailed, Exit: 1, Failures: ids}
	}
	red := Job{ID: "j1", Phase: "P0", Command: "make e2e", Commit: "c1", Status: JobFailed, Exit: 1}
	recs := map[string][]lane.Stage{
		"j1": {passed("one"), failed("two", "TestA", "TestB")},
		"j2": {passed("one"), failed("two", "TestA")},
		"j3": {passed("one")},
	}
	for _, c := range []struct {
		name   string
		jobs   []Job
		of     string // RerunOf's gate, "" for none
		from   []string
		why    string
		stages []string
	}{
		{"nothing on record", nil, "", nil, RerunNoGate, nil},
		{"a red gate", []Job{red}, "j1", []string{"j1"}, "", []string{"one:passed@j1", "two:failed[TestA TestB]"}},
		{"a red gate with no stages", []Job{{ID: "j7", Phase: "P0", Command: "make e2e", Status: JobFailed}, {ID: "j9", Phase: "P0", Command: "make e2e", Status: JobInterrupted}},
			"j7", []string{"j7"}, "", nil},
		{"stopped again after a failure", []Job{red, {ID: "j2", Phase: "P0", Command: "make e2e", Commit: "c2", Status: JobInterrupted}},
			"j1", []string{"j1", "j2"}, "", []string{"one:passed@j2", "two:failed[TestA]"}},
		{"a run that runs on after a failure", []Job{red, {ID: "j2", Phase: "P0", Command: "make e2e", Commit: "c2", Status: JobRunning}},
			"j1", []string{"j1", "j2"}, "", []string{"one:passed@j2", "two:failed[TestA]"}},
		{"stopped mid-stage on a failure", []Job{red, {ID: "j3", Phase: "P0", Command: "make e2e", Commit: "c3", Status: JobInterrupted,
			Failing: &Failing{Line: "--- FAIL: TestC", Stage: "two"}}}, "j1", []string{"j1", "j3"}, "", []string{"one:passed@j3"}},
		{"stopped before any failure", []Job{red, {ID: "j3", Phase: "P0", Command: "make e2e", Status: JobInterrupted}},
			"j1", []string{"j1"}, "", []string{"one:passed@j1", "two:failed[TestA TestB]"}},
		{"a narrower check stopped after a failure", []Job{red, {ID: "j2", Phase: "P0", Command: "go test -run TestA ./e2e", Status: JobInterrupted}},
			"j1", []string{"j1"}, "", []string{"one:passed@j1", "two:failed[TestA TestB]"}},
		{"a red rerun", []Job{red, {ID: "j2", Phase: "P0", Command: "make e2e", Commit: "c2", Reruns: "j1", Status: JobFailed}},
			"j2", []string{"j2"}, "", []string{"one:passed@j2", "two:failed[TestA]"}},
		{"no red gate: a gate stopped after a failure", []Job{{ID: "j2", Phase: "P0", Command: "make e2e", Commit: "c2", Status: JobInterrupted}},
			"j2", []string{"j2"}, "", []string{"one:passed@j2", "two:failed[TestA]"}},
		{"no red gate: a pass since", []Job{{ID: "j2", Phase: "P0", Command: "make e2e", Status: JobInterrupted},
			{ID: "j4", Phase: "P0", Command: "make e2e", Status: JobPassed}}, "", nil, RerunNoGate, nil},
		{"no red gate: a gate runs with no failure", []Job{{ID: "j4", Phase: "P0", Command: "make e2e", Status: JobRunning}}, "", nil, RerunNoGate, nil},
		{"another phase's red gate", []Job{{ID: "j1", Phase: "P1", Command: "make e2e", Status: JobFailed}}, "", nil, RerunNoGate, nil},
		{"a red wait outside", []Job{{ID: "j1", Phase: "P0", Kind: JobAwait, Command: "gh run watch 1", Status: JobFailed}}, "", nil, RerunNoGate, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := State{Jobs: c.jobs}
			r, why := st.RerunOf("P0", func(j Job) []lane.Stage { return recs[j.ID] })
			if c.why == "" && len(c.stages) == 0 {
				c.why = RerunNoStages
			}
			if why != c.why || r.Of.ID != c.of || !slices.Equal(ids(r.From), c.from) {
				t.Fatalf("RerunOf: %q of %q from %v, want %q of %q from %v", why, r.Of.ID, ids(r.From), c.why, c.of, c.from)
			}
			if got := stageNames(r.Stages); !slices.Equal(got, c.stages) {
				t.Errorf("stages %v, want %v", got, c.stages)
			}
			for _, s := range r.Stages {
				if s.Status == lane.StagePassed && s.Commit == "" {
					t.Errorf("stage %s passed on no commit", s.Name)
				}
			}
		})
	}
}

// A rerun is the same check as its gate, and as any run of its command: neither stops the other unasked.
func TestARerunIsTheSameCheck(t *testing.T) {
	rerun := Job{Phase: "P0", Command: "make e2e", Reruns: "j1"}
	for _, c := range []struct {
		name    string
		running Job
		clash   bool
	}{
		{"its gate's command, running", Job{ID: "j2", Phase: "P0", Command: "make e2e", Status: JobRunning}, false},
		{"another rerun of it", Job{ID: "j2", Phase: "P0", Command: "make e2e", Reruns: "j1", Status: JobQueued}, false},
		{"a narrower check", Job{ID: "j2", Phase: "P0", Command: "go test -run TestA ./e2e", Status: JobRunning}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := State{Jobs: []Job{{ID: "j1", Phase: "P0", Command: "make e2e", Status: JobFailed}, c.running}}
			if got := st.Clash(rerun) != nil; got != c.clash {
				t.Errorf("clash %v, want %v", got, c.clash)
			}
			if got := rerun.Same(c.running); got == c.clash {
				t.Errorf("same %v", got)
			}
		})
	}
	st := State{Jobs: []Job{{ID: "j1", Phase: "P0", Command: "make e2e", Status: JobFailed}, {ID: "j2", Phase: "P0", Command: "make e2e", Status: JobRunning}}}
	q, replaced, _ := QueueJob(&st, rerun, t0)
	if replaced == nil || replaced.ID != "j2" || st.Job("j2").Status != JobInterrupted || q.Reruns != "j1" {
		t.Errorf("queued %+v, replaced %+v", q, replaced)
	}
}

// After a red gate, the checks that did not clear it: a rerun stopped, say, but not one that failed in its
// place.
func TestAfterARedGate(t *testing.T) {
	st := State{Jobs: []Job{
		{ID: "j1", Phase: "P0", Command: "make e2e", Status: JobFailed},
		{ID: "j2", Phase: "P0", Command: "make e2e", Reruns: "j1", Status: JobInterrupted},
		{ID: "j3", Phase: "P0", Command: "go test -run TestA ./e2e", Status: JobPassed},
	}}
	g := st.RedGate("P0")
	if g == nil || g.ID != "j1" || !slices.Equal(ids(st.After(*g)), []string{"j2", "j3"}) {
		t.Fatalf("red gate %+v, after %v", g, ids(st.After(*g)))
	}
	st.Jobs = append(st.Jobs, Job{ID: "j4", Phase: "P0", Command: "make e2e", Reruns: "j1", Status: JobFailed})
	if g := st.RedGate("P0"); g == nil || g.ID != "j4" || len(st.After(*g)) != 0 {
		t.Errorf("red gate %+v", g)
	}
}

// baton stop ends a job queued or running as interrupted, with no result to tell, and the red gate its
// phase stood on before stands as it was. A job that ended, or is not on record, is not stopped.
func TestStopLeavesTheGateStanding(t *testing.T) {
	st := State{Jobs: []Job{
		{ID: "j1", Phase: "P0", Command: "make e2e", Status: JobFailed},
		{ID: "j2", Phase: "P0", Command: "make e2e", Status: JobRunning, PID: 7},
		{ID: "j3", Phase: "P0", Kind: JobAwait, Command: "gh run watch 1", Status: JobQueued},
	}}
	if got := ids(st.Standing("P0")); !slices.Equal(got, []string{"j2", "j3"}) {
		t.Fatalf("standing %v", got)
	}
	was, ok := StopJob(&st, "j2", "stopped with baton stop (two failed)", t0)
	if !ok || was.Status != JobRunning || was.PID != 7 {
		t.Fatalf("stopped %v %+v", ok, was)
	}
	j := st.Job("j2")
	if j.Status != JobInterrupted || j.Reason != "stopped with baton stop (two failed)" || j.Exit != -1 || !j.Ended.Equal(t0) || !j.Delivered.Equal(t0) {
		t.Errorf("j2 %+v", j)
	}
	if g := st.RedGate("P0"); g == nil || g.ID != "j1" || !slices.Equal(ids(st.Standing("P0")), []string{"j1", "j3"}) {
		t.Errorf("red gate %+v, standing %v", g, ids(st.Standing("P0")))
	}
	if st.Job("j3").Status != JobQueued {
		t.Errorf("the await beside it stopped too")
	}
	for _, id := range []string{"j1", "j2", "j9"} {
		if _, ok := StopJob(&st, id, "again", t0.Add(time.Minute)); ok {
			t.Errorf("stopped %s", id)
		}
	}
	if st.Job("j2").Reason != "stopped with baton stop (two failed)" {
		t.Errorf("a second stop changed j2: %+v", st.Job("j2"))
	}
}

// --stages runs the stages it names whole: carried ones brought back, failed ones run whole rather than
// narrowed. A name the record lacks is told apart.
func TestStagesBroughtBack(t *testing.T) {
	stages := []lane.Stage{{Name: "one", Status: lane.StagePassed}, {Name: "two", Status: lane.StageFailed, Failures: []string{"TestA"}}, {Name: "three", Status: lane.StageSkipped}}
	if got := stageNames(RerunRecord(stages, []string{"one"})); !slices.Equal(got, []string{"two:failed[TestA]", "three:skipped"}) {
		t.Errorf("record %v", got)
	}
	if got := stageNames(RerunRecord(stages, nil)); len(got) != 3 || stages[0].Name != "one" {
		t.Errorf("record %v", got)
	}
	if got := Unrecorded(stages, []string{"one", "four", "tow"}); !slices.Equal(got, []string{"four", "tow"}) {
		t.Errorf("unrecorded %v", got)
	}
}

// What a rerun carries and narrows, as queued and as it ran, with the files changed since each carried
// stage passed: at most MaxChanged named, none for a stage that passed on the rerun's own commit, and
// unknown where git could not say.
func TestARerunsView(t *testing.T) {
	r := Rerun{Of: Job{ID: "j1"}, Stages: []lane.Stage{
		{Name: "lint", Status: lane.StagePassed, Commit: "c2", From: "j2"},
		{Name: "unit", Status: lane.StageCarried, Commit: "c1", From: "j1"},
		{Name: "e2e", Status: lane.StageFailed, Failures: []string{"TestA"}},
		{Name: "perf", Status: lane.StageFailed},
		{Name: "docs", Status: lane.StagePassed, Commit: "c3", From: "j3"},
	}}
	record := RerunRecord(r.Stages, []string{"lint"})
	j := Job{ID: "j5", Commit: "c3", Reruns: "j1"}
	v := PlannedRerun(j, r, record)
	if v.Of != "j1" || v.Commit != "c3" || !slices.Equal(stageNames(carriedOf(v)), []string{"unit:carried@j1", "docs:passed@j3"}) ||
		!slices.Equal(stageNames(v.Narrowed), []string{"e2e:failed[TestA]"}) || !slices.Equal(v.Whole, []string{"lint", "perf"}) {
		t.Fatalf("planned %+v", v)
	}
	var many []string
	for i := range MaxChanged + 3 {
		many = append(many, fmt.Sprintf("f%d.go", i))
	}
	asked := 0
	v.Changes(func(from, to string) ([]string, error) {
		asked++
		if from != "c1" || to != "c3" {
			t.Errorf("changes %s..%s", from, to)
		}
		return many, nil
	})
	if c := v.Carried[0]; asked != 1 || len(c.Changed) != MaxChanged || c.More != 3 || !c.Known {
		t.Errorf("unit %+v", c)
	}
	if c := v.Carried[1]; c.Changed != nil || c.Known {
		t.Errorf("docs, on the rerun's own commit: %+v", c)
	}
	v.Carried[0].Changed, v.Carried[0].Known = nil, false
	v.Changes(func(string, string) ([]string, error) { return nil, errors.New("no git") })
	if c := v.Carried[0]; c.Known || c.Changed != nil {
		t.Errorf("unit, with no git: %+v", c)
	}

	ran := []lane.Stage{
		{Name: "lint", Status: lane.StagePassed, Commit: "c3"},
		{Name: "unit", Status: lane.StageCarried},
		{Name: "e2e", Status: lane.StageFailed, Failures: []string{"TestA"}},
		{Name: "perf", Status: lane.StagePassed},
	}
	got := RanRerun(j, ran, record)
	if got.Of != "j1" || !slices.Equal(stageNames(carriedOf(got)), []string{"unit:carried@j1"}) || got.Carried[0].Commit != "c1" ||
		!slices.Equal(stageNames(got.Narrowed), []string{"e2e:failed[TestA]"}) || got.Whole != nil {
		t.Errorf("ran %+v", got)
	}
}

func carriedOf(v RerunView) []lane.Stage {
	var out []lane.Stage
	for _, c := range v.Carried {
		out = append(out, c.Stage)
	}
	return out
}

// A rerun's record is written whole beside its log, read back as a stages file, and kept with the job's
// other files; a job's stages are its stages file's, or, with none, what the runner read from its log.
func TestARerunsRecordIsKeptWithItsJob(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), DirName), "i", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	record := []lane.Stage{{Name: "one", Status: lane.StagePassed, Commit: "c1", From: "j1"}, {Name: "two", Status: lane.StageFailed, Failures: []string{"TestA"}}}
	if err := s.SaveRerun("j2", record); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRerun("j2", record[:1]); err != nil { // written whole, never appended to
		t.Fatal(err)
	}
	got, err := lane.ReadStages(s.JobRerun("j2"))
	if err != nil || !slices.Equal(stageNames(got), []string{"one:passed@j1"}) || got[0].Commit != "c1" {
		t.Fatalf("read back %+v, %v", got, err)
	}
	if id := JobFileID(filepath.Base(s.JobRerun("j2"))); id != "j2" {
		t.Errorf("JobFileID %q", id)
	}

	j := Job{ID: "j3", Stages: []lane.Stage{{Name: "from-the-log", Status: lane.StagePassed}}}
	if got := stageNames(s.StagesOf(j)); !slices.Equal(got, []string{"from-the-log:passed"}) {
		t.Errorf("no stages file: %v", got)
	}
	if err := lane.AppendStage(s.JobStages("j3"), lane.Stage{Name: "from-the-file", Status: lane.StageFailed}); err != nil {
		t.Fatal(err)
	}
	if got := stageNames(s.StagesOf(j)); !slices.Equal(got, []string{"from-the-file:failed"}) {
		t.Errorf("a stages file: %v", got)
	}

	if s.Reran(Job{ID: "j3"}) != nil {
		t.Error("a job that reruns nothing has a rerun's view")
	}
	os.WriteFile(s.JobStages("j2"), []byte(`{"name":"one","status":"carried"}`+"\n"), 0o644)
	if v := s.Reran(Job{ID: "j2", Reruns: "j1", Commit: "c2"}); v == nil || len(v.Carried) != 1 || v.Carried[0].From != "j1" || v.Carried[0].Commit != "c1" || v.Carried[0].Known {
		t.Errorf("reran %+v", v)
	}
}
