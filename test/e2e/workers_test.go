//go:build !windows

package e2e

import (
	"encoding/json"
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
// with the lead's permission rules. Its commit stays on its branch (landing it is the merge queue's), and
// the worker is suspended when the session ends.
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
	branch, _ := spawned["branch"].(string)
	worktree, _ := spawned["worktree"].(string)
	s.Until("the worker on P1 to finish", 4*time.Minute, func() bool {
		return first(s.WorkerEvents("P1"), is("worker_finished", "phase", "P1")) >= 0 && first(s.Events(), is("phase_done", "phase", "P0")) >= 0
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
	if log := Git(t, dir, "log", "--format=%s", branch); !strings.Contains(log, "feat: one") {
		t.Errorf("the worker's branch %s: %s", branch, log)
	}
	if got := Git(t, worktree, "show", "HEAD:one.txt"); strings.TrimSpace(got) != "one" {
		t.Errorf("one.txt in the worker's commit: %q", got)
	}
	if strings.Contains(Git(t, dir, "log", "--format=%s"), "feat: one") {
		t.Error("the worker's commit is on the run's branch, with nothing to land it")
	}
	none(t, s, "workers_off", "worker_resumed", "controller_panic")

	// The session ends: the worker with it, suspended for the next host.
	s.WaitQuiet(3*time.Second, time.Minute)
	s.Type("/exit")
	select {
	case <-s.exited:
	case <-time.After(time.Minute):
		t.Fatal("the session did not end")
	}
	if first(s.Events(), is("worker_ended", "phase", "P1", "suspended", "true")) < 0 {
		t.Errorf("the worker was not suspended")
	}
}
