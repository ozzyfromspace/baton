//go:build !windows

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A plan attached in a conversation that had a turn before it starts after a compaction, as an approved
// plan does: P0 starts from a brief, not in the middle of that conversation.
func TestRunCompactsAConversationThatHadTurns(t *testing.T) {
	dir := NewProject(t)
	writePlan(t, dir, "plan.md", twoPhasePlan)
	s := Start(t, dir, "--model", "haiku", "Reply with the single word ready, and nothing else.")
	s.Trust()
	s.WaitEvent("turn_started", time.Minute)
	s.WaitQuiet(3*time.Second, time.Minute)
	s.Type("Run exactly this one command with the Bash tool: baton attach plan.md --suggested\nThen do exactly what it prints.")
	waitSequence(t, s, 5*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "attached" && e["compact_first"] == true },
		func(e map[string]any) bool {
			return e["kind"] == "compact_queued" && e["reason"] == "boundary" && e["next"] == "P0"
		},
		func(e map[string]any) bool { return e["kind"] == "compact_started" && e["by_baton"] == true },
		func(e map[string]any) bool { return e["kind"] == "phase_started" && e["phase"] == "P0" },
		func(e map[string]any) bool {
			return e["kind"] == "brief" && e["type"] == "boundary" && e["phase"] == "P0"
		},
		kind("rewake"),
	)
}

// The conversation grows by more than 20k tokens while baton is paused. /baton run picks the paused run
// back up: the model records where the phase stands, and baton compacts before the phase goes on.
func TestRunPicksUpAPausedRunAndCompactsFirst(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, longPhasePlan)
	for _, name := range []string{"a.txt", "b.txt"} { // about 14k tokens each
		var b strings.Builder
		for i := 1; i <= 800; i++ {
			fmt.Fprintf(&b, "%s line %04d: the quick brown fox jumps over the lazy dog, then naps.\n", name, i)
		}
		os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644)
	}
	s := Start(t, dir, "--model", "haiku", "--plugin-dir", pluginDir(t),
		"Run exactly this one command with the Bash tool: baton pause\nThen reply with the single word paused.")
	s.Trust()
	s.WaitEvent("paused", 2*time.Minute)
	s.WaitQuiet(3*time.Second, time.Minute)
	s.Type("Read the files a.txt and b.txt in full with the Read tool, then reply with the single word read.")
	s.Until("both files read", 3*time.Minute, func() bool { return strings.Contains(s.Screen(), "b.txt") })
	s.WaitQuiet(5*time.Second, 2*time.Minute)
	s.Type("/baton run")
	waitSequence(t, s, 5*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "resumed" && e["checkpoint_due"] == true },
		kind("checkpoint"),
		func(e map[string]any) bool { return e["kind"] == "compact_queued" && e["reason"] == "checkpoint" },
		func(e map[string]any) bool { return e["kind"] == "compact_started" && e["by_baton"] == true },
		func(e map[string]any) bool {
			return e["kind"] == "brief" && e["type"] == "checkpoint" && e["phase"] == "P0"
		},
		kind("rewake"),
	)
	handoff, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "handoff.md"))
	if len(handoff) == 1 {
		b, _ := os.ReadFile(handoff[0])
		t.Logf("handoff:\n%s", b)
	}
}

// A phase added to a plan that has finished runs on /baton run: after a compaction, from a brief.
func TestRunPicksUpAPhaseAddedToAFinishedPlan(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, "# One step\n\n## P0 — Say hello\nRun echo hello.\n")
	s := Start(t, dir, "--model", "haiku", "--plugin-dir", pluginDir(t),
		"Start the attached plan. For phase P0, run `echo hello` with the Bash tool, then run "+
			"`baton done P0 --notes \"said hello\"` and end your turn.")
	s.Trust()
	s.WaitEvent("plan_complete", 3*time.Minute)
	s.WaitQuiet(3*time.Second, time.Minute)
	f, _ := os.OpenFile(filepath.Join(dir, "plan.md"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n## P1 — Say goodbye\nRun echo goodbye.\n")
	f.Close()
	s.Type("/baton run")
	waitSequence(t, s, 5*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "plan_changed" && fmt.Sprint(e["added"]) == "[P1]" },
		func(e map[string]any) bool {
			return e["kind"] == "compact_queued" && e["reason"] == "boundary" && e["next"] == "P1"
		},
		func(e map[string]any) bool { return e["kind"] == "phase_started" && e["phase"] == "P1" },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P1" },
		kind("plan_complete"),
	)
}
