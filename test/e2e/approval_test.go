//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Approving a plan in plan mode runs it, with no command from anyone: baton attaches the plan, compacts
// the planning conversation away, and P0 starts from a brief. The test plays the human: it switches to
// plan mode, asks for a plan, and approves it.
func TestAnApprovedPlanRunsByItself(t *testing.T) {
	dir := NewProject(t)
	s := Start(t, dir, "--model", "haiku")
	s.RemovePlansAfter()
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	if !s.PlanMode() {
		t.Fatal("could not switch to plan mode")
	}
	s.AutoApprove()
	s.Type(`Plan this tiny task. Write the plan to your plan file with exactly these two headings and nothing else as headings: ` +
		`"## P0 — Say hello" (step: run echo hello) and "## P1 — Say goodbye" (step: run echo goodbye). ` +
		`Do not explore the repository. Then call ExitPlanMode.`)
	waitSequence(t, s, 8*time.Minute,
		func(e map[string]any) bool {
			return e["kind"] == "attached" && e["by"] == "approval" && e["phases"] == 2.0
		},
		func(e map[string]any) bool {
			return e["kind"] == "compact_queued" && e["reason"] == "boundary" && e["next"] == "P0"
		},
		func(e map[string]any) bool { return e["kind"] == "phase_started" && e["phase"] == "P0" }, // from the compaction's brief
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
		kind("plan_complete"),
	)
}

// Two baton terminals in one project each run a plan of their own, at the same time.
func TestTwoTerminalsRunTwoPlans(t *testing.T) {
	dir := NewProject(t)
	writePlan(t, dir, "a.md", "# Plan A\n\n## P0 — Say alpha\nRun echo alpha.\n\n## P1 — Say beta\nRun echo beta.\n")
	writePlan(t, dir, "b.md", "# Plan B\n\n## P0 — Say gamma\nRun echo gamma.\n\n## P1 — Say delta\nRun echo delta.\n")
	start := func(file string) *Session {
		s := Start(t, dir, "--model", "haiku",
			"Run exactly this one command with the Bash tool: baton attach "+file+" --suggested\nThen do the phase it names, report it with baton done, and end your turn.")
		s.Trust()
		return s
	}
	a := start("a.md")
	b := start("b.md")
	for _, c := range []struct {
		s     *Session
		title string
	}{{a, "a.md"}, {b, "b.md"}} {
		s := c.s
		if !s.Until(c.title+" completes", 8*time.Minute, func() bool {
			for _, e := range s.OwnEvents() {
				if e["kind"] == "plan_complete" {
					return true
				}
			}
			return false
		}) {
			t.Fatalf("%s did not complete: %s", c.title, eventLog(s))
		}
	}
	own := func(s *Session) string {
		for _, e := range s.OwnEvents() {
			if p, _ := e["plan"].(string); e["kind"] == "attached" {
				return p
			}
		}
		return ""
	}
	if pa, pb := own(a), own(b); pa == "" || pb == "" || pa == pb {
		t.Fatalf("the sessions' runs: %q and %q", pa, pb)
	}
}

// writePlan writes a plan document into the project and commits it, as a plan kept in the repository.
func writePlan(t *testing.T, dir, name, doc string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o644)
	Git(t, dir, "add", name)
	Git(t, dir, "commit", "-q", "-m", "docs: "+name)
}
