package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// eventsOf is every event of kind in the run's log, oldest first.
func eventsOf(t *testing.T, s *state.Store, kind string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(s.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var e map[string]any
		json.Unmarshal(sc.Bytes(), &e)
		if e["kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

// stagesOf is stages in short: name, status, failures.
func stagesOf(stages []lane.Stage) []string {
	var out []string
	for _, st := range stages {
		out = append(out, fmt.Sprintf("%s %s %v keep-going=%v", st.Name, st.Status, st.Failures, st.KeepGoing))
	}
	return out
}

// runAsync runs lane-run for j1 in the background, returning a channel its exit code comes on.
func runAsync(env map[string]string) chan int {
	done := make(chan int, 1)
	go func() {
		code, _ := laneRun(env, "sess-1", "j1")
		done <- code
	}()
	return done
}

// A gate that prints its stages' markers itself: each stage is recorded as it ends, in the job's record
// and its stages file, and its first failure as it is printed, while the gate runs on.
func TestLaneRunRecordsStagesAsTheyEnd(t *testing.T) {
	defer func(d time.Duration) { stageSettle = d }(stageSettle)
	stageSettle = 100 * time.Millisecond
	goFile := filepath.Join(t.TempDir(), "go")
	s, env := laneProject(t, strings.Join([]string{
		"echo '::baton stage lint start'", "echo lint ok", "echo '::baton stage lint passed exit=0 seconds=0'",
		"echo '::baton stage unit start'", "echo '::baton fail TestA'", "echo '--- FAIL: TestA (0.01s)'",
		fmt.Sprintf(`i=0; while [ ! -e %s ] && [ "$i" -lt 2000 ]; do sleep 0.01; i=$((i + 1)); done`, goFile),
		"echo '::baton stage unit failed exit=1 seconds=1'", "exit 1",
	}, "; "))
	done := runAsync(env)

	// Live: the failure, and the stage that ended before it, while the gate still runs.
	var j state.Job
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		st, _ := s.Load()
		if j = *st.Job("j1"); j.Failing != nil && len(j.Stages) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the failure and the first stage were never recorded while the gate ran: %+v", j)
		}
	}
	if j.Status != state.JobRunning || j.Failing.Line != "::baton fail TestA" || j.Failing.Stage != "unit" || j.Failing.At.Before(j.Started) {
		t.Errorf("while it ran: %s, failing %+v", j.Status, *j.Failing)
	}
	if got := stagesOf(j.Stages); !slices.Equal(got, []string{"lint passed [] keep-going=false"}) {
		t.Errorf("stages while it ran: %q", got)
	}
	os.WriteFile(goFile, nil, 0o644)
	if code := <-done; code != 0 {
		t.Fatalf("lane-run: exit %d", code)
	}

	st, _ := s.Load()
	j = *st.Job("j1")
	want := []string{"lint passed [] keep-going=false", "unit failed [TestA] keep-going=false"}
	if j.Status != state.JobFailed || !slices.Equal(stagesOf(j.Stages), want) || j.Failing.Line != "::baton fail TestA" {
		t.Errorf("job: %s, stages %q, failing %+v", j.Status, stagesOf(j.Stages), j.Failing)
	}
	for _, st := range j.Stages {
		if st.Commit == "" || st.Commit != j.Commit || st.Ended.Before(st.Started) || st.Started.Before(j.Started) {
			t.Errorf("stage %+v", st)
		}
	}
	if file, err := lane.ReadStages(s.JobStages("j1")); err != nil || !slices.Equal(stagesOf(file), want) {
		t.Errorf("the stages file: %q, %v", stagesOf(file), err)
	}
	var ended []string
	for _, e := range eventsOf(t, s, "stage_finished") {
		ended = append(ended, fmt.Sprintf("%s %s %s %v", e["job"], e["stage"], e["status"], e["failures"]))
		if e["phase"] != "P1" || e["seconds"] == nil {
			t.Errorf("stage_finished: %v", e)
		}
	}
	if !slices.Equal(ended, []string{"j1 lint passed 0", "j1 unit failed 1"}) {
		t.Errorf("stage_finished: %q", ended)
	}
	if e := eventsOf(t, s, "job_failing"); len(e) != 1 || e[0]["job"] != "j1" || e[0]["phase"] != "P1" || e[0]["line"] != "::baton fail TestA" || e[0]["stage"] != "unit" {
		t.Errorf("job_failing: %v", e)
	}
}

// A stage `baton stage` ran is recorded as it wrote it in the stages file, a moment after its marker,
// with what only it knows (--keep-going, its runner's failure ids), and only once.
func TestLaneRunTakesAStageFromItsStagesFile(t *testing.T) {
	record := `{"name":"unit","status":"failed","exit":1,"keep_going":true,"failures":["TestB"]}`
	s, env := laneProject(t, strings.Join([]string{
		"echo '::baton stage unit start'", "echo '::baton stage unit failed exit=1 seconds=1'",
		"sleep 0.2", `printf '%s\n' '` + record + `' >> "$BATON_STAGES"`, "exit 1",
	}, "; "))
	if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
		t.Fatalf("lane-run: exit %d: %s", code, errs)
	}
	st, _ := s.Load()
	want := []string{"unit failed [TestB] keep-going=true"}
	if j := st.Job("j1"); !slices.Equal(stagesOf(j.Stages), want) || j.Failing == nil || j.Failing.Stage != "unit" {
		t.Errorf("job: stages %q, failing %+v", stagesOf(j.Stages), j.Failing)
	}
	if file, _ := lane.ReadStages(s.JobStages("j1")); !slices.Equal(stagesOf(file), want) {
		t.Errorf("the stages file: %q", stagesOf(file))
	}
	if e := eventsOf(t, s, "stage_finished"); len(e) != 1 || e[0]["failures"] != 1.0 {
		t.Errorf("stage_finished: %v", e)
	}
}

// In a QA phase's gate, a stage recorded from its markers alone keeps going, as `baton stage` records one
// there, so that it stops no stage after it; in any other gate it stops them.
func TestLaneRunRecordsAQAPhasesStagesKeepingGoing(t *testing.T) {
	defer func(d time.Duration) { stageSettle = d }(stageSettle)
	stageSettle = 50 * time.Millisecond
	for _, qa := range []bool{false, true} {
		s, env := laneProject(t, "echo '::baton stage lint start'; echo '::baton stage lint failed exit=1 seconds=0'; exit 1")
		s.Update(func(st *state.State) error { st.Jobs[0].QAPhase = qa; return nil })
		if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
			t.Fatalf("qa %v: lane-run: exit %d: %s", qa, code, errs)
		}
		st, _ := s.Load()
		want := []string{fmt.Sprintf("lint failed [] keep-going=%v", qa)}
		if j := st.Job("j1"); !slices.Equal(stagesOf(j.Stages), want) {
			t.Errorf("qa %v: job's stages %q, want %q", qa, stagesOf(j.Stages), want)
		}
		file, _ := lane.ReadStages(s.JobStages("j1"))
		if !slices.Equal(stagesOf(file), want) {
			t.Errorf("qa %v: the stages file: %q, want %q", qa, stagesOf(file), want)
		}
		if _, halted := lane.Halted(file); halted == qa {
			t.Errorf("qa %v: halted %v", qa, halted)
		}
	}
}

// A failure seen as the gate printed it is kept, though the log's cap cuts it out of the log after.
func TestLaneRunKeepsAFailureCutFromItsLog(t *testing.T) {
	s, env := laneProject(t, fmt.Sprintf("yes filler | head -c %d; echo; echo '--- FAIL: TestCut'; yes filler | head -c %d; exit 1", lane.HeadBytes+40<<10, lane.MaxBytes+1<<20))
	if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
		t.Fatalf("lane-run: exit %d: %s", code, errs)
	}
	st, _ := s.Load()
	j := st.Job("j1")
	if j.Status != state.JobFailed || j.Failing == nil || j.Failing.Line != "--- FAIL: TestCut" || j.Failing.Stage != "" {
		t.Errorf("job: %s, failing %+v", j.Status, j.Failing)
	}
	// The runner's own line names the command, the failure's line with it; the line itself is gone.
	if log, _ := os.ReadFile(j.Log); strings.Contains(string(log), "\n--- FAIL: TestCut\n") || !strings.Contains(string(log), "bytes of output cut here") {
		t.Errorf("the log still holds the failure, or was never cut (%d bytes)", len(log))
	}
	if e := eventsOf(t, s, "job_failing"); len(e) != 1 || e[0]["line"] != "--- FAIL: TestCut" {
		t.Errorf("job_failing: %v", e)
	}
}
