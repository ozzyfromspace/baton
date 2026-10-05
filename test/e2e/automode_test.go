//go:build !windows

package e2e

import (
	"os"
	"testing"
	"time"
)

// baton's premise is auto mode, which Haiku lacks. This runs the boundary scenario on Sonnet in auto
// mode with no help: no dialog may open (the allow rule baton passes covers its CLI), and the plan must
// complete with one boundary compaction. Costs more than the Haiku suite, so it has its own switch.
func TestAutoModeOnSonnet(t *testing.T) {
	if os.Getenv("BATON_E2E_SONNET") != "1" {
		t.Skip("set BATON_E2E_SONNET=1 to run the Sonnet auto-mode scenario")
	}
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := Start(t, dir, "--model", "sonnet", "--permission-mode", "auto", "Start the attached plan.")
	s.Trust()
	waitSequence(t, s, 6*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
		func(e map[string]any) bool { return e["kind"] == "compact_started" && e["by_baton"] == true },
		func(e map[string]any) bool { return e["kind"] == "brief" && e["phase"] == "P1" },
		kind("rewake"),
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P1" },
		kind("plan_complete"),
	)
	for _, e := range s.Events() {
		if e["kind"] == "dialog_open" || e["kind"] == "stop_refused" || e["kind"] == "escalated" {
			t.Errorf("unexpected in auto mode: %v", e)
		}
	}
}
