//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// pluginDir is this repository's plugin, loaded with --plugin-dir as a developer would.
func pluginDir(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "plugin")
}

// /baton run finds the phases, attaches the plan and starts it.
func TestSkillAttachesAndStartsAPlan(t *testing.T) {
	dir := NewProject(t)
	os.WriteFile(filepath.Join(dir, "plan.md"), []byte(twoPhasePlan), 0o644)
	s := Start(t, dir, "--model", "haiku", "--plugin-dir", pluginDir(t))
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	s.Type("/baton run plan.md")
	waitSequence(t, s, 4*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "attached" && e["phases"] == 2.0 },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
	)
}

// /baton plan runs a formal planning pass in plan mode, attaches the approved plan and starts it. The
// test plays the human: it approves entering plan mode, the plan, and any permission prompt.
func TestSkillPlansAttachesAndStarts(t *testing.T) {
	dir := NewProject(t)
	s := Start(t, dir, "--model", "haiku", "--plugin-dir", pluginDir(t))
	s.RemovePlansAfter()
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	s.AutoApprove()
	s.Type("/baton plan a two-phase plan for this empty repo: P0 runs `echo hello`, P1 runs `echo goodbye`. Keep it tiny.")
	waitSequence(t, s, 6*time.Minute,
		kind("attached"),
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
	)
}
