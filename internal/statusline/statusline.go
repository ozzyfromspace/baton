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

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
)

// Segment is baton's part of the status line, the lead session's own: the phase in hand, then a usage
// limit the run waits on, the jobs baton runs for the lead's phases, and the context's size; or what
// holds the lead (an escalation, a proposal, a compaction). The phase agents beside it are on baton's band
// (internal/board).
func Segment(st state.State, pl plan.Plan, havePlan bool, now time.Time) string {
	const mark = "◆ baton"
	if !havePlan || st.Mode == state.ModeIdle {
		return mark
	}
	p := st.PendingProposal()
	switch {
	case st.Mode == state.ModeComplete:
		return mark + " · ✓ plan complete"
	case st.Mode == state.ModePaused:
		return mark + " · paused"
	case st.ReviewDue != "":
		return mark + " · ⚠ review due"
	case p != nil && p.Untimed == "" && !p.Deadline.IsZero():
		return mark + " · ? proposal · goes ahead " + p.Deadline.Local().Format("15:04")
	case p != nil || st.Run.Escalation != nil && st.Run.Escalation.Kind == "decision":
		return mark + " · ? proposal · waiting on you"
	case st.Run.Escalation != nil && st.Run.Escalation.Kind == "refused":
		return mark + " · ⚠ refused · waiting on you"
	case st.Run.Escalation != nil:
		return mark + " · ⚠ waiting on you"
	case st.Blocked != nil:
		return mark + " · ⚠ blocked"
	case st.Run.Compaction.InFlight() && st.Run.Gate != "":
		// Never claim a compaction is happening while something holds it back. That read
		// "compacting…" for 35 minutes once, which is indistinguishable from a hung session.
		return mark + " · compact held: " + clip(st.Run.Gate, 32)
	case st.Run.Compaction.InFlight():
		return mark + " · compacting…"
	}
	s := mark
	if i := pl.Index(st.Current); i >= 0 {
		s += fmt.Sprintf(" · %s %d/%d %s", st.Current, i+1, len(pl.Phases), clip(pl.Phases[i].Title, 28))
	}
	if u := st.Usage; u != nil && now.Before(u.Limited) {
		s += " · usage limit until " + u.Limited.Local().Format("15:04")
	}
	if j := jobs(st, now); j != "" {
		s += " · " + j
	}
	if c := st.Run.Context; c != nil {
		if c.Limit > 0 {
			s += " · ctx " + valve.Tokens(c.Used()) + "/" + valve.Tokens(c.Limit)
		} else {
			s += fmt.Sprintf(" · ctx %.0f%%", c.UsedPct)
		}
	}
	return s
}

// jobs is the run's jobs not finished yet: the oldest, as what its phase is doing and for how long, then
// how many more ("P3 verifying 12m +2"). "" when there are none.
func jobs(st state.State, now time.Time) string {
	open := st.Unfinished()
	if len(open) == 0 {
		return ""
	}
	j := open[0]
	s := fmt.Sprintf("%s %s %s", j.Phase, doing(st, j), decide.Span(max(now.Sub(j.Queued), 0)))
	if len(open) > 1 {
		s += fmt.Sprintf(" +%d", len(open)-1)
	}
	return s
}

// doing is what job j's phase is doing while j runs: verifying (done was claimed, or it is the phase in
// hand and j its gate), parked (set aside until j ends), or awaiting (something outside the project). The
// phase in hand whose session ended its turn "waits on its gate": nothing is needed from the human, though
// Claude Code's idle notice says otherwise.
func doing(st state.State, j state.Job) string {
	if ps := st.Phases[j.Phase]; ps != nil {
		switch ps.Status {
		case state.PhaseVerifying:
			return "verifying"
		case state.PhaseParked:
			return "parked"
		}
	}
	switch {
	case j.Kind == state.JobAwait:
		return "awaiting"
	case j.Phase == st.Current && !st.Run.TurnOpen:
		return "waits on its gate"
	}
	return "verifying"
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
	SessionID string `json:"session_id"`
	Workspace struct {
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	ContextWindow struct {
		UsedPercentage    *float64 `json:"used_percentage"`
		ContextWindowSize int      `json:"context_window_size"`
		// CurrentUsage is the last request's token usage; null until the first response.
		CurrentUsage *struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
	// RateLimits is how much of the account's plan limits is used: for subscribers only, from the first
	// response on. Claude Code leaves a window out once its reset has passed.
	RateLimits struct {
		FiveHour *Window `json:"five_hour"`
	} `json:"rate_limits"`
}

// Window is one of the account's usage limits.
type Window struct {
	UsedPercentage *float64 `json:"used_percentage"` // 0 to 100
	ResetsAt       *float64 `json:"resets_at"`       // Unix seconds
}

// FiveHour is the account's five-hour limit: how much of it is used, in percent, and when it resets
// (zero if not said). ok is false with no reading, and for one whose reset has passed: that use was of a
// window gone by.
func (in Input) FiveHour(now time.Time) (used float64, resets time.Time, ok bool) {
	w := in.RateLimits.FiveHour
	if w == nil || w.UsedPercentage == nil {
		return 0, time.Time{}, false
	}
	if w.ResetsAt != nil {
		resets = time.Unix(int64(*w.ResetsAt), 0)
		if !now.Before(resets) {
			return 0, time.Time{}, false
		}
	}
	return *w.UsedPercentage, resets, true
}

// Tokens is the context size as of the last request: everything it sent, cached or not. ok is false
// before the first response. (total_input_tokens is deliberately not used: it is documented as a
// session total, the very number the model mistakes for its context.)
func (in Input) Tokens() (tokens int, ok bool) {
	cw := in.ContextWindow
	if u := cw.CurrentUsage; u != nil {
		return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens, true
	}
	if cw.UsedPercentage != nil && cw.ContextWindowSize > 0 {
		return int(*cw.UsedPercentage * float64(cw.ContextWindowSize) / 100), true
	}
	return 0, false
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
	return strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\r\n")
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
