// Package statusline renders the status line of a baton-hosted session: baton's segment, which is
// always there, followed by the user's own status line, which baton runs unchanged. baton supplies its
// status line through --settings for its sessions only, so the user's own command has to be found in
// their settings files and run on their behalf.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Segment is baton's part of the status line.
func Segment(st state.State, pl plan.Plan, havePlan bool) string {
	const mark = "◆ baton"
	if !havePlan || st.Mode == state.ModeIdle {
		return mark
	}
	switch {
	case st.Mode == state.ModeComplete:
		return mark + " · ✓ plan complete"
	case st.Mode == state.ModePaused:
		return mark + " · paused"
	case st.Run.Escalation != nil:
		return mark + " · ⚠ waiting on you"
	case st.Blocked != nil:
		return mark + " · ⚠ blocked"
	case st.Run.Compaction.InFlight():
		return mark + " · compacting…"
	}
	s := mark
	if i := pl.Index(st.Current); i >= 0 {
		s += fmt.Sprintf(" · %s %d/%d %s", st.Current, i+1, len(pl.Phases), clip(pl.Phases[i].Title, 28))
	}
	if c := st.Run.Context; c != nil {
		s += fmt.Sprintf(" · ctx %.0f%%", c.UsedPct)
	}
	if st.Waiting != nil {
		s += " · waiting: " + clip(st.Waiting.What, 20)
	}
	return s
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// Input is the part of Claude Code's status line input baton reads.
type Input struct {
	Workspace struct {
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	ContextWindow struct {
		UsedPercentage    *float64 `json:"used_percentage"`
		ContextWindowSize int      `json:"context_window_size"`
	} `json:"context_window"`
}

// Parse decodes the status line input; unknown or missing fields are fine.
func Parse(raw []byte) Input {
	var in Input
	json.Unmarshal(raw, &in)
	return in
}

// UserCommand finds the status line command the user configured, in Claude Code's precedence order
// (local project settings, project settings, user settings), ignoring baton's own.
func UserCommand(projectDir, home string) string {
	files := []string{
		filepath.Join(projectDir, ".claude", "settings.local.json"),
		filepath.Join(projectDir, ".claude", "settings.json"),
		filepath.Join(home, ".claude", "settings.json"),
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			StatusLine *struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"statusLine"`
		}
		if json.Unmarshal(b, &s) != nil || s.StatusLine == nil || s.StatusLine.Command == "" {
			continue
		}
		if isBaton(s.StatusLine.Command) {
			continue
		}
		return s.StatusLine.Command
	}
	return ""
}

func isBaton(cmd string) bool {
	return strings.Contains(cmd, "baton") && strings.Contains(cmd, "statusline")
}

// RunUser runs the user's status line command with the same input Claude Code gave baton.
func RunUser(command string, input []byte, dir string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "BATON_STATUSLINE_NESTED=1")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// Compose puts baton's segment in front of the user's status line (first line, if it has several).
func Compose(segment, user string) string {
	if user == "" {
		return segment
	}
	lines := strings.Split(user, "\n")
	lines[0] = segment + "  " + lines[0]
	return strings.Join(lines, "\n")
}
