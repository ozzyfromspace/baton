package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What a result shows of a gate reads, beside its log's end, the tests a red one says failed, and what its
// phase's gates have cost so far; an await reads neither, and a log that is gone names no test.
func TestJobLogReadsWhatFailedAndWhatGatesCost(t *testing.T) {
	f := newFixture(t, true)
	dir := t.TempDir()
	logOf := func(id, text string) string {
		p := filepath.Join(dir, id+".log")
		os.WriteFile(p, []byte(text), 0o644)
		return p
	}
	const red = "=== RUN   TestA\n--- FAIL: TestA (0.01s)\n--- FAIL: TestB (0.02s)\n    --- FAIL: TestB/sub (0.00s)\nFAIL\texample.com/m/internal/x\t0.3s\n"
	ago := func(m int) time.Time { return f.now.Add(-time.Duration(m) * time.Minute) }
	jobs := []state.Job{
		{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1, Started: ago(50), Ran: ago(48), Ended: ago(36),
			Log: logOf("j1", red)},
		{ID: "j2", Phase: "P0", Kind: state.JobAwait, Command: "gh run watch 7", Status: state.JobFailed, Exit: 1, Started: ago(40), Ended: ago(5),
			Log: logOf("j2", red)},
		{ID: "j3", Phase: "P0", Kind: state.JobVerify, Command: "make lint", Status: state.JobPassed, Started: ago(30), Ran: ago(30), Ended: ago(28),
			Log: logOf("j3", "ok\n")},
		{ID: "j4", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1, Started: ago(20), Ran: ago(20), Ended: ago(8),
			Log: logOf("j4", red)},
		{ID: "j5", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1, Started: ago(7), Ran: ago(7), Ended: ago(6),
			Log: filepath.Join(dir, "gone.log")},
	}
	f.store.Update(func(st *state.State) error { st.Jobs = append(st.Jobs, jobs...); return nil })

	x := "example.com/m/internal/x"
	for _, c := range []struct {
		name   string
		job    state.Job
		failed []lane.FailedTest
		cost   state.Cost
	}{
		{"a red gate", jobs[3], []lane.FailedTest{{Runner: lane.RunGo, ID: "TestA", Scope: x, Took: 10 * time.Millisecond},
			{Runner: lane.RunGo, ID: "TestB", Scope: x, Took: 20 * time.Millisecond}},
			state.Cost{Runs: 4, Gates: 27 * time.Minute, Of: "make test", Command: 3, CommandTime: 25 * time.Minute}},
		{"a passed gate", jobs[2], nil, state.Cost{Runs: 4, Gates: 27 * time.Minute, Of: "make lint", Command: 1, CommandTime: 2 * time.Minute}},
		{"its log gone", jobs[4], nil, state.Cost{Runs: 4, Gates: 27 * time.Minute, Of: "make test", Command: 3, CommandTime: 25 * time.Minute}},
		{"an await", jobs[1], nil, state.Cost{}},
	} {
		log := JobLog(f.store, c.job)
		if len(log.Failed) != len(c.failed) || log.FailedMore != 0 {
			t.Errorf("%s: failed %+v (and %d more), want %+v", c.name, log.Failed, log.FailedMore, c.failed)
		}
		for i := range min(len(log.Failed), len(c.failed)) {
			if log.Failed[i] != c.failed[i] {
				t.Errorf("%s: failed %+v, want %+v", c.name, log.Failed, c.failed)
			}
		}
		got := log.Cost
		got.Phase = 0 // when the phase started is state.GateCost's to say
		if got != c.cost {
			t.Errorf("%s: cost %+v, want %+v", c.name, got, c.cost)
		}
		if c.cost.Runs > 0 && log.Cost.Phase < 48*time.Minute {
			t.Errorf("%s: the phase's time %s, from before its first gate", c.name, log.Cost.Phase)
		}
	}

	// The result gives the tests, the rerun of only them, and the cost at its end.
	got := decide.JobResult(jobs[3], JobLog(f.store, jobs[3]), nil, "")
	for _, w := range []string{"\nFailed (2): TestA and TestB.\nRerun only these: `go test -run '^(TestA|TestB)$' example.com/m/internal/x`",
		"\nP0 so far: 4 gate runs, 27m of gates in its "} {
		if !strings.Contains(got, w) {
			t.Errorf("the result lacks %q:\n%s", w, got)
		}
	}
	if !strings.HasSuffix(got, "; `make test` 3 of them, 25m. This gate spends 12m of the human's time; to you it is one tool call.") {
		t.Errorf("the result ends:\n%s", got)
	}
}

// A red gate's failures that waited out a timeout are read from the ids its log names, against how long it
// ran, and its result says so; a gate they took too small a share of, and an await, have none.
func TestJobLogFindsTheFailuresThatWaitedOutATimeout(t *testing.T) {
	f := newFixture(t, true)
	log := filepath.Join(t.TempDir(), "j1.log")
	os.WriteFile(log, []byte("--- FAIL: TestA (300.00s)\n--- FAIL: TestB (301.20s)\n--- FAIL: TestC (0.10s)\n--- FAIL: TestD (299.50s)\n"+
		"FAIL\texample.com/m/internal/e2e\t901.0s\n"), 0o644)
	gate := func(id, kind string, ran time.Duration) state.Job {
		return state.Job{ID: id, Phase: "P0", Kind: kind, Command: "make e2e", Status: state.JobFailed, Exit: 1, Log: log,
			Started: f.now.Add(-ran - time.Minute), Ran: f.now.Add(-ran), Ended: f.now}
	}
	for _, c := range []struct {
		name string
		job  state.Job
		want []string
	}{
		{"a 20-minute gate", gate("j1", state.JobVerify, 20*time.Minute), []string{"TestA", "TestB", "TestD"}},
		{"a 70-minute gate", gate("j2", state.JobVerify, 70*time.Minute), nil},
		{"an await", gate("j3", state.JobAwait, 20*time.Minute), nil},
	} {
		var got []string
		for _, ft := range JobLog(f.store, c.job).TimedOut {
			got = append(got, ft.ID)
		}
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: timed out %q, want %q", c.name, got, c.want)
		}
	}
	j := gate("j1", state.JobVerify, 20*time.Minute)
	if got := decide.JobResult(j, JobLog(f.store, j), nil, ""); !strings.Contains(got, "3 of 4 failures ran about 5m each, 15 of this gate's 20 minutes: they waited out a timeout.") {
		t.Errorf("the result:\n%s", got)
	}
}
