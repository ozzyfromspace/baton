//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// workerPlan has two phases that can go ahead at once: the lead takes P0, and a worker P1. P2 needs both.
const workerPlan = `# Worker plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one
After: none

Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'wrote one'

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// WorkerEvents reads the event log of the run's worker on phase (.baton/runs/*/workers/<phase>/events.jsonl).
func (s *Session) WorkerEvents(phase string) []map[string]any {
	logs, _ := filepath.Glob(filepath.Join(s.Dir, ".baton", "runs", "*", "workers", phase, "events.jsonl"))
	var out []map[string]any
	for _, path := range logs {
		out = append(out, readEvents(path)...)
	}
	return out
}

// logEvents logs every event, for a failure's diagnosis.
func logEvents(t *testing.T, of string, events []map[string]any) {
	t.Logf("events of %s:", of)
	for _, e := range events {
		b, _ := json.Marshal(e)
		t.Logf("  %s", b)
	}
}

// A worker takes up P1 beside the lead, which works on P0: in a worktree of its own, from its own brief,
// with the lead's permission rules. Once it reports P1 done, its commit lands on the run's branch under
// the lead, the worker ends, and P2, which needs both, starts only then. The lead hears what landed.
func TestAWorkerWorksBesideTheLead(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, workerPlan)
	s := StartEnv(t, dir, []string{"BATON_WORKERS=1"}, "Start the attached plan, and follow baton's instructions.", "--model", "haiku",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	defer s.Close()
	t.Cleanup(func() {
		if t.Failed() {
			logEvents(t, "the run", s.Events())
			logEvents(t, "the worker on P1", s.WorkerEvents("P1"))
		}
	})
	s.Trust()
	s.AutoApprove()

	spawned := s.WaitEvent("worker_spawned", 2*time.Minute)
	if spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no worker on P1: %v", spawned)
	}
	worktree, _ := spawned["worktree"].(string)
	s.Until("the plan to complete", 8*time.Minute, func() bool {
		return first(s.Events(), is("plan_complete")) >= 0 || first(s.Events(), is("phase_done", "phase", "P2")) >= 0
	})
	if first(s.Events(), is("worker_started", "phase", "P1")) < 0 {
		t.Error("the worker never started")
	}
	w := s.WorkerEvents("P1")
	for i, want := range []func(map[string]any) bool{is("phase_started", "phase", "P1"), is("brief", "type", "worker"), is("phase_done", "phase", "P1"), is("worker_finished", "phase", "P1")} {
		if first(w, want) < 0 {
			t.Fatalf("the worker's events lack step %d", i)
		}
	}
	ev := s.Events()
	landed := first(ev, is("landed", "phase", "P1"))
	if first(ev, is("land_queued", "phase", "P1")) < 0 || landed < 0 {
		t.Fatal("P1's work did not land")
	}
	if p2 := first(ev, is("phase_started", "phase", "P2")); p2 < landed || first(ev, is("phase_done", "phase", "P0")) > p2 {
		t.Errorf("P2 started (event %d) before P0 was done or P1 landed (event %d)", p2, landed)
	}
	if log := Git(t, dir, "log", "--format=%s"); !strings.Contains(log, "feat: one") {
		t.Errorf("the run's branch lacks the worker's commit: %s", log)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "one.txt")); err != nil || strings.TrimSpace(string(got)) != "one" {
		t.Errorf("one.txt in the checkout: %q, %v", got, err)
	}
	if first(ev, is("worker_ended", "phase", "P1", "why", "its work landed")) < 0 {
		t.Error("the landed worker did not end")
	}
	if _, err := os.Stat(worktree); err == nil {
		t.Errorf("the landed worker's worktree %s is still there", worktree)
	}
	if first(ev, is("landed_told")) < 0 {
		t.Error("the lead was never told what landed")
	}
	none(t, s, "workers_off", "worker_resumed", "controller_panic", "land_returned", "land_deferred")
}
