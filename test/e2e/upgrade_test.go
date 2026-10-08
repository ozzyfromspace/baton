//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installVersions builds baton as each release version into a throwaway baton home, the way the plugin's
// launcher installs them, and links the first as the installed one.
func installVersions(t *testing.T, versions ...string) (home string) {
	t.Helper()
	home = t.TempDir()
	for _, v := range versions {
		out := filepath.Join(home, "bin", v, "baton")
		build := exec.Command("go", "build", "-ldflags", "-X github.com/ozzyfromspace/baton/internal/version.Version="+v, "-o", out, "../../cmd/baton")
		if b, err := build.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", v, err, b)
		}
	}
	linkVersion(t, home, versions[0])
	return home
}

// linkVersion makes v the installed baton, as the launcher does once it has downloaded it.
func linkVersion(t *testing.T, home, v string) {
	t.Helper()
	link := filepath.Join(home, "bin", "baton")
	os.Remove(link)
	if err := os.Symlink(filepath.Join(v, "baton"), link); err != nil {
		t.Fatal(err)
	}
}

// eventAfter finds the first event of a kind after the event at index i, and its index.
func eventAfter(events []map[string]any, i int, kind string, check func(map[string]any) bool) (map[string]any, int) {
	for j := i + 1; j < len(events); j++ {
		if events[j]["kind"] == kind && (check == nil || check(events[j])) {
			return events[j], j
		}
	}
	return nil, -1
}

// A newer compatible baton is installed while a session sits idle: the session restarts on it by
// itself, in the same process, and goes on with the same conversation. The /baton instructions the
// conversation loaded before are the old version's, so the first prompt after it is handed the current ones.
func TestAnIdleSessionRestartsOnANewerBaton(t *testing.T) {
	dir := NewProject(t)
	home := installVersions(t, "v9.1.0", "v9.1.1")
	s := StartProgram(t, dir, []string{"BATON_HOME=" + home, "BATON_RESTART_IDLE=3s"}, filepath.Join(home, "bin", "v9.1.0", "baton"),
		"--model", "haiku", "--plugin-dir", pluginDir(t))
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	s.Type("/baton help")
	s.WaitEvent("turn_started", time.Minute)
	linkVersion(t, home, "v9.1.1")

	restarting := s.WaitEvent("restarting", 2*time.Minute)
	if restarting == nil || restarting["from"] != "v9.1.0" || restarting["to"] != "v9.1.1" {
		t.Fatalf("restarting: %v", restarting)
	}
	restarted := s.WaitEvent("restarted", time.Minute)
	if restarted == nil || restarted["version"] != "v9.1.1" {
		t.Fatalf("restarted: %v", restarted)
	}
	// The same process (an exec, not a child), hosting the same conversation.
	events := s.Events()
	_, i := eventAfter(events, -1, "restarting", nil)
	host, _ := eventAfter(events, i, "host_started", func(e map[string]any) bool { return e["version"] == "v9.1.1" })
	if pid, _ := host["pid"].(float64); host == nil || int(pid) != s.cmd.Process.Pid {
		t.Fatalf("host_started after the restart: %v (baton's pid %d)", host, s.cmd.Process.Pid)
	}
	if start, _ := eventAfter(events, i, "session_start", nil); start == nil || start["source"] != "resume" {
		t.Fatalf("the session was not resumed: %v", start)
	}
	if notes, _ := eventAfter(events, -1, "newer_baton", nil); notes != nil {
		// Installed after the only turn ended: nobody was told before the restart, which is fine.
		t.Logf("told the human first: %v", notes)
	}

	s.WaitQuiet(2*time.Second, 30*time.Second)
	s.Type("Reply with the single word again, and nothing else.")
	s.Until("a turn after the restart", time.Minute, func() bool {
		evs := s.Events()
		_, j := eventAfter(evs, -1, "restarted", nil)
		e, _ := eventAfter(evs, j, "turn_started", func(e map[string]any) bool { return e["by"] == "human" })
		return e != nil
	})
	refreshed := s.WaitEvent("skill_refreshed", 30*time.Second)
	if refreshed == nil || refreshed["from"] != "v9.1.0" || refreshed["to"] != "v9.1.1" {
		t.Fatalf("skill_refreshed: %v", refreshed)
	}
	sessions, _ := filepath.Glob(filepath.Join(dir, ".baton", "sessions", "*"))
	if len(sessions) != 1 || filepath.Base(sessions[0]) != restarting["session"] {
		t.Fatalf("sessions after the restart: %v, want only %v", sessions, restarting["session"])
	}
}

// With a plan running, the restart waits for the phase boundary and takes the place of the compaction:
// the restarted session compacts and the plan carries on to the end.
func TestARunningPlanRestartsAtThePhaseBoundary(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	home := installVersions(t, "v9.1.0", "v9.1.1")
	linkVersion(t, home, "v9.1.1") // installed before the run starts: the restart still waits for the boundary
	s := StartProgram(t, dir, []string{"BATON_HOME=" + home}, filepath.Join(home, "bin", "v9.1.0", "baton"), "--model", "haiku",
		"Start the attached plan. For phase P0, run `echo hello` with the Bash tool, then run "+
			"`baton done P0 --notes \"said hello\"` and end your turn. After that, follow baton's instructions.")
	s.Trust()

	steps := []struct {
		kind  string
		check func(map[string]any) bool
	}{
		{"phase_done", func(e map[string]any) bool { return e["phase"] == "P0" }},
		{"compact_queued", func(e map[string]any) bool { return e["reason"] == "boundary" }},
		{"restarting", func(e map[string]any) bool { return e["from"] == "v9.1.0" && e["to"] == "v9.1.1" }},
		{"host_started", func(e map[string]any) bool { return e["version"] == "v9.1.1" }},
		{"restarted", func(e map[string]any) bool { return e["version"] == "v9.1.1" }},
		{"compact_typed", nil},
		{"compact_started", func(e map[string]any) bool { return e["by_baton"] == true }},
		{"phase_started", func(e map[string]any) bool { return e["phase"] == "P1" }},
		{"phase_done", func(e map[string]any) bool { return e["phase"] == "P1" }},
		{"plan_complete", nil},
	}
	next := 0
	s.Until("the plan to complete", 6*time.Minute, func() bool {
		next = 0
		for _, e := range s.Events() {
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
		switch {
		case e["kind"] == "escalated", e["kind"] == "compact_failed", e["kind"] == "notified" && e["notice"] == "session_ended":
			t.Errorf("unexpected %v", e)
		case e["kind"] == "restarting":
			if e["mode"] != "running" {
				t.Errorf("restarting: %v", e)
			}
		}
	}
	var restarts int
	for _, e := range s.Events() {
		if e["kind"] == "restarting" {
			restarts++
		}
	}
	if restarts != 1 {
		t.Errorf("restarted %d times", restarts)
	}
}
