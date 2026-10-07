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

// /baton start, end to end: in a real zsh with baton's init line, a plain claude session (baton's plugin
// loaded) runs `baton start`; when its turn ends baton stops it, the shell relaunches the same
// conversation under baton, and the model gets the pending step and runs the plan.
func TestPlainSessionStartsBaton(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	batonHome := t.TempDir()
	zdot := t.TempDir()
	rc := "PS1='e2e%# '\n" +
		"export BATON_BIN=" + shq(batonBin) + "\n" +
		`eval "$(` + shq(batonBin) + ` init zsh)"` + "\n"
	os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte(rc), 0o644)
	s := StartProgram(t, dir, []string{"ZDOTDIR=" + zdot, "BATON_HOME=" + batonHome, "BATON_SHELL_HOOK=1"}, "zsh", "-d", "-i")
	s.WaitScreen("e2e%", 20*time.Second)
	s.Type(`claude --model haiku --plugin-dir ` + shq(pluginDir(t)) +
		` 'Run exactly this one command with the Bash tool: baton start "Run echo hello, then baton done P0, then end your turn." Then end your turn immediately.'`)
	s.Trust()
	// A plain session has no allow rule for baton (Haiku has no auto mode): approve the one prompt, as
	// the human would.
	s.Until("baton start requested", 2*time.Minute, func() bool {
		for _, e := range s.Events() {
			if e["kind"] == "start_requested" {
				return true
			}
		}
		if strings.Contains(s.Screen(), "Doyouwanttoproceed?") && !strings.Contains(s.Screen(), "[approved]") {
			time.Sleep(time.Second)
			s.pty.Write([]byte("\r"))
			s.mu.Lock()
			s.screen.WriteString("[approved]")
			s.mu.Unlock()
		}
		return false
	})
	waitSequence(t, s, 5*time.Minute,
		kind("start_requested"),
		kind("host_started"),
		kind("pending_delivered"), // the resume rewake (async) and the sync session_start log in either order
		func(e map[string]any) bool { return e["kind"] == "turn_started" && e["by"] == "baton" },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
	)
	resumed := false
	for _, e := range s.Events() {
		resumed = resumed || e["kind"] == "session_start" && e["source"] == "resume"
	}
	if !resumed {
		t.Error("no resumed session_start")
	}
	if entries, _ := os.ReadDir(filepath.Join(batonHome, "elevate")); len(entries) != 0 {
		t.Errorf("elevation record not consumed: %v", entries)
	}
}

func shq(s string) string { return "'" + s + "'" }

// Leaving baton, end to end: in a hosted session the model runs `baton exit`; when its turn ends baton
// stops claude, and the shell resumes the same conversation as plain claude.
func TestLeavingBatonResumesPlainClaude(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	dir := NewProject(t)
	batonHome, zdot := t.TempDir(), t.TempDir()
	rc := "PS1='e2e%# '\n" +
		"export BATON_BIN=" + shq(batonBin) + "\n" +
		`eval "$(` + shq(batonBin) + ` init zsh)"` + "\n"
	os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte(rc), 0o644)
	s := StartProgram(t, dir, []string{"ZDOTDIR=" + zdot, "BATON_HOME=" + batonHome, "BATON_SHELL_HOOK=1"}, "zsh", "-d", "-i")
	s.WaitScreen("e2e%", 20*time.Second)
	s.Type(shq(batonBin) + ` --model haiku 'Run exactly this one command with the Bash tool: baton exit. Then end your turn immediately.'`)
	s.Trust()
	waitSequence(t, s, 4*time.Minute, kind("exit_requested"), kind("left_baton"), kind("host_stopped"))
	var session string
	for _, e := range s.Events() {
		if e["kind"] == "host_started" {
			logs, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*"))
			if len(logs) == 1 {
				session = filepath.Base(logs[0])
			}
		}
	}
	if session == "" {
		t.Fatal("no run to tell the session by")
	}
	t.Cleanup(func() { exec.Command("pkill", "-f", "claude --resume "+session).Run() })
	var plain string
	s.Until("plain claude resumes the session", time.Minute, func() bool {
		out, _ := exec.Command("pgrep", "-fl", "claude --resume "+session).Output()
		plain = string(out)
		return plain != ""
	})
	if plain == "" || strings.Contains(plain, "--settings") {
		t.Fatalf("resumed as %q", plain)
	}
	if entries, _ := os.ReadDir(filepath.Join(batonHome, "elevate")); len(entries) != 0 {
		t.Errorf("the record to resume was not consumed: %v", entries)
	}
}
