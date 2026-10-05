//go:build !windows

package e2e

import (
	"strings"
	"testing"
	"time"
)

// The heart of baton: a two-phase plan runs to completion with one compaction at the boundary and no
// keystrokes from anyone. Every step is asserted from .baton/events.jsonl.
func TestPhaseBoundaryCompactsAndResumes(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := Start(t, dir, "--model", "haiku",
		"Start the attached plan. For phase P0, run `echo hello` with the Bash tool, then run "+
			"`baton done P0 --notes \"said hello\"` and end your turn. After that, follow baton's instructions.")
	s.Trust()

	steps := []struct {
		kind  string
		check func(map[string]any) bool
	}{
		{"phase_done", func(e map[string]any) bool { return e["phase"] == "P0" }},
		{"compact_queued", func(e map[string]any) bool { return e["reason"] == "boundary" }},
		{"compact_typed", nil},
		{"compact_started", func(e map[string]any) bool { return e["by_baton"] == true && e["trigger"] == "manual" }},
		{"phase_started", func(e map[string]any) bool { return e["phase"] == "P1" }},
		{"brief", func(e map[string]any) bool { return e["type"] == "boundary" && e["phase"] == "P1" }},
		{"rewake", nil},
		{"turn_started", func(e map[string]any) bool { return e["by"] == "baton" }},
		{"phase_done", func(e map[string]any) bool { return e["phase"] == "P1" }},
		{"plan_complete", nil},
	}
	next := 0
	s.Until("the plan to complete", 5*time.Minute, func() bool {
		events := s.Events()
		next = 0
		for _, e := range events {
			if next < len(steps) && e["kind"] == steps[next].kind && (steps[next].check == nil || steps[next].check(e)) {
				next++
			}
		}
		return next == len(steps)
	})
	if next != len(steps) {
		var kinds []string
		for _, e := range s.Events() {
			kinds = append(kinds, e["kind"].(string))
		}
		t.Fatalf("stopped before %q; events: %s", steps[next].kind, strings.Join(kinds, " → "))
	}
	for _, e := range s.Events() {
		if e["kind"] == "escalated" || e["kind"] == "compact_failed" || e["kind"] == "stop_refused" {
			t.Errorf("unexpected %v", e)
		}
	}
}
