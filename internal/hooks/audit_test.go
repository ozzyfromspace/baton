package hooks

import (
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// An approval owes a look at the session's background work before the plan starts (state.Runtime.Audit,
// which the host makes: loop/tasks.go), and so does the phase boundary it starts after. The lead's Stop lists
// the session's background work, baton's phase agents among it: an agent of baton's is none of the model's
// own work, and never a suspect for the reviewer, nor long-running work it names.
func TestAnApprovalOwesALookAtBackgroundWork(t *testing.T) {
	f := newFixture(t, false)
	f.approve(f.writePlan("humming-whistling-hare.md", approvedDoc))
	st := f.state()
	if st.Run.Audit == nil || !slices.Equal(st.Run.Audit.Why, []string{state.AuditAttach, state.AuditBoundary}) {
		t.Fatalf("audit %+v", st.Run.Audit)
	}
	f.fire("Stop", map[string]any{"background_tasks": []any{
		map[string]any{"id": "a9", "type": "subagent", "status": "running", "description": "P1 — Fixture", "agent_type": "baton:phase-high"},
		map[string]any{"id": "b1", "type": "shell", "status": "running", "description": "Wait for the build", "command": "sleep 99999"},
	}})
	later := f.now.Add(3 * time.Hour)
	st = f.state()
	var own []string
	for _, t := range st.OwnTasks(later) {
		own = append(own, t.Task.ID)
	}
	sus := state.Suspects(st, nil, state.TaskQuiet, later)
	if !slices.Equal(own, []string{"b1"}) || len(sus) != 1 || sus[0].Task.ID != "b1" || len(state.LongTasks(st, sus, later)) != 0 {
		t.Errorf("own %v, suspects %+v", own, sus)
	}
}
