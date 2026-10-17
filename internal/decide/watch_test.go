package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

func TestWatchedJobLines(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	running := state.Job{ID: "j2", Phase: "P3", Command: "go test ./...", Commit: "72b7740aaaa", Status: state.JobRunning,
		Lane: 1, Started: ago(12 * time.Minute), Ran: ago(10 * time.Minute)}
	m := time.Minute
	cases := []struct {
		name string
		job  WatchedJob
		want string
	}{
		{"queued", WatchedJob{Job: state.Job{Status: state.JobQueued, Queued: ago(m)}}, "queued 1m"},
		{"waiting for a lane", WatchedJob{Job: state.Job{Status: state.JobRunning, Started: ago(3 * m), LaneWait: ago(3 * m)}},
			"waiting 3m for a lane"},
		{"no earlier run", WatchedJob{Job: running}, "running 10m in lane 1 · got its lane after 2m"},
		{"one earlier run", WatchedJob{Job: running, Earlier: []time.Duration{11 * m}},
			"running 10m in lane 1 · its earlier run took 11m · got its lane after 2m"},
		{"past one earlier run", WatchedJob{Job: running, Earlier: []time.Duration{9 * m}},
			"running 10m in lane 1 · past the 9m its earlier run took · got its lane after 2m"},
		{"a range", WatchedJob{Job: running, Earlier: []time.Duration{11 * m, 14 * m}},
			"running 10m in lane 1 · earlier runs took 11m–14m · got its lane after 2m"},
		{"runs alike", WatchedJob{Job: running, Earlier: []time.Duration{11 * m, 11*m + 20*time.Second}},
			"running 10m in lane 1 · its 2 earlier runs took 11m · got its lane after 2m"},
		{"past them all", WatchedJob{Job: running, Earlier: []time.Duration{8 * m, 9 * m}, Still: 4 * m},
			"running 10m in lane 1 · past the 9m its longest earlier run took · got its lane after 2m · log still 4m"},
		{"here, no lane, a log still a moment", WatchedJob{Job: state.Job{Status: state.JobRunning, Here: true, Started: ago(5 * m)}, Still: 20 * time.Second},
			"running 5m"},
	}
	for _, c := range cases {
		if got := c.job.Stand(now); got != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.name, got, c.want)
		}
	}

	if got, want := (WatchedJob{Job: running}).Head(), "j2  P3  `go test ./...` on 72b7740"; got != want {
		t.Errorf("head %q, want %q", got, want)
	}
	staged := WatchedJob{Job: running, Stage: "e2e", Stages: []lane.Stage{
		{Name: "lint", Status: lane.StageCarried, From: "j1"},
		{Name: "build", Status: lane.StagePassed, Started: ago(9 * m), Ended: ago(9*m - 14*time.Second)},
		{Name: "unit", Status: lane.StageFailed, Started: ago(8 * m), Ended: ago(6 * m), Failures: []string{"TestA", "TestB"}, KeepGoing: true},
		{Name: "vet", Status: lane.StageSkipped},
	}, Failure: "--- FAIL: TestA (0.01s)", FailStage: "unit"}
	for _, c := range []struct{ got, want string }{
		{staged.StageLine(), "stages: lint passed earlier · build ✓ 14s · unit ✗ 2m00s (2 failed) · vet skipped · e2e running"},
		{staged.FailLine(), "first failure, in unit: --- FAIL: TestA (0.01s)"},
		{WatchedJob{Failure: "x failed"}.FailLine(), "first failure: x failed"},
		{WatchedJob{}.StageLine(), ""},
		{WatchedJob{}.FailLine(), ""},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
}

func TestWatchLanding(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.Local)
	queued := now.Add(-10 * time.Minute)
	w := func(m state.Merge) state.Worker {
		m.Queued = queued
		return state.Worker{Phase: "P7", Merge: &m}
	}
	here := &state.Job{ID: "j9"}
	cases := []struct {
		w    state.Worker
		here *state.Job
		want string
	}{
		{w(state.Merge{}), nil, "1. P7 finished 13:50 · landing"},
		{w(state.Merge{Reverify: "P7.j3"}), here, "1. P7 finished 13:50 · verifying again with the run's branch merged in (job P7.j3)"},
		{w(state.Merge{Deferred: "the lead's changes to the same files", DeferredAt: now.Add(-4 * time.Minute)}), here,
			"1. P7 finished 13:50 · waits to land: the lead's changes to the same files (since 13:56, 4m)"},
		{w(state.Merge{}), here, "1. P7 finished 13:50 · waits for gate j9, which runs in the working tree"},
		{w(state.Merge{Merged: "abc", Onto: "5d1e2aa99"}), nil, "1. P7 finished 13:50 · merged with 5d1e2aa, landing"},
	}
	for _, c := range cases {
		if got := WatchLanding(1, c.w, c.here, now); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestWatchEvent(t *testing.T) {
	cases := []struct {
		kind string
		f    map[string]any
		want string
	}{
		{"phase_started", map[string]any{"phase": "P9", "worker": "P9"}, "P9 started"},
		{"phase_parked", map[string]any{"phase": "P1", "reason": state.ParkRed}, "P1 set aside after its verification failed"},
		{"job_queued", map[string]any{"phase": "P8", "job": "P8.j1", "command": "go test ./internal/loop"}, "P8.j1 queued for P8: `go test ./internal/loop`"},
		{"job_finished", map[string]any{"job": "P8.j2", "status": "passed", "exit": 0.0, "seconds": 577.0}, "P8.j2 passed in 9m"},
		{"job_finished", map[string]any{"job": "j2", "status": "failed", "exit": 1.0, "seconds": 720.0}, "j2 failed (exit 1) after 12m"},
		{"job_finished", map[string]any{"job": "j2", "status": "passed", "seconds": 2580.0, "waited_seconds": 1860.0, "ran_seconds": 720.0}, "j2 passed in 12m"},
		{"lane_acquired", map[string]any{"job": "j2", "lane": 2.0, "waited_seconds": 1860.0}, "j2 got lane 2 after 31m waiting"},
		{"lane_acquired", map[string]any{"job": "j2", "lane": 1.0, "waited_seconds": 0.0}, ""},
		{"job_finished", map[string]any{"job": "j2", "status": "timeout", "seconds": 3600.0}, "j2 ran out of time after 1h00m"},
		{"job_finished", map[string]any{"job": "j2", "status": "lane_error", "reason": "no checkout"}, "j2 ended lane_error — no checkout"},
		{"job_failing", map[string]any{"job": "j2", "line": "--- FAIL: TestA", "stage": "unit"}, "j2 failing in unit: --- FAIL: TestA"},
		{"stage_finished", map[string]any{"job": "j2", "stage": "unit", "status": "passed", "seconds": 134.0}, "j2 stage unit passed in 2m14s"},
		{"worker_ended", map[string]any{"phase": "P7", "why": "its work landed"}, "P7's phase agent ended — its work landed"},
		{"worker_deferred", map[string]any{"phase": "P3", "why": "the machine is busy"}, "no phase agent on P3 yet — the machine is busy"},
		{"reverify_skipped", map[string]any{"phase": "P4", "qa": "P15"}, "P4 lands without verifying again: P15 checks it"},
		{"note", map[string]any{"what": "merged main"}, "the lead decided: merged main"},
		{"note", map[string]any{"phase": "P8", "worker": "P8", "what": "merged main"}, "P8 decided: merged main"},
		{"run_stalled", map[string]any{"quiet": "20m0s"}, "nothing moved for 20m"},
		{"lock_slow", map[string]any{"caller": "x"}, ""},
		{"turn_started", nil, ""},
		{"mod_command", nil, ""},
	}
	for _, c := range cases {
		if got := WatchEvent(c.kind, c.f); got != c.want {
			t.Errorf("%s: got %q, want %q", c.kind, got, c.want)
		}
	}
}
