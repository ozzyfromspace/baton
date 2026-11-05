package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// foregroundDefault is how long Claude Code lets a foreground command run when the call sets no timeout: a
// foreground wait loop given longer holds the model's turn while it polls (backgroundCheck).
const foregroundDefault = 2 * time.Minute

// backgroundCheck refuses, once, a shell call of the model's own that spends a task or a turn on what baton or
// Claude Code does better, while its plan runs: a Bash or PowerShell call from the lead or a phase agent (a
// subagent of its model's is left alone, as the holds leave it). Two kinds:
//   - wait_loop: a loop that only waits by polling (lane.IsWaitLoop), started with run_in_background, or in
//     the foreground with a timeout over foregroundDefault. Claude Code wakes the model when a background
//     task ends, so nothing need poll for it, and one that waits for something that never comes runs as long
//     as the session (decide.WaitLoop). In a real run one polled for 22 hours.
//   - check: a check (lane.IsCheck) started with run_in_background. baton sees nothing of such a task, so
//     nothing records it, it holds no lane, and its failures reach the model only when it ends: it is
//     pointed to baton verify (decide.BackgroundCheck).
//
// The same command again goes through, so nothing waits on anyone; a call a hold of the run's would refuse
// anyway is left to that hold, so the one question is not spent on it. Logged background_questioned with
// its kind as type, as gate_questioned has it (an event's kind is its name). It reports whether it refused,
// and fails open: a run it cannot read refuses nothing.
func (h *handlers) backgroundCheck(c Context) (Result, bool) {
	tool := str(c.Input, "tool_name")
	if tool != "Bash" && tool != "PowerShell" || c.Agent != "" {
		return Result{}, false
	}
	ti, _ := c.Input["tool_input"].(map[string]any)
	raw := str(ti, "command")
	bg, _ := ti["run_in_background"].(bool)
	var timeout time.Duration // a foreground call's own, when it is over foregroundDefault
	if ms, ok := ti["timeout"].(float64); ok && !bg && time.Duration(ms)*time.Millisecond > foregroundDefault {
		timeout = time.Duration(ms) * time.Millisecond
	}
	if raw == "" || !bg && timeout == 0 {
		return Result{}, false
	}
	if baton, _ := batonShell(raw); baton {
		return Result{}, false
	}
	var kind string
	switch {
	case (bg || timeout > 0) && lane.IsWaitLoop(raw):
		kind = "wait_loop"
	case bg && lane.IsCheck(raw):
		kind = "check"
	default:
		return Result{}, false
	}
	cmd := strings.Join(strings.Fields(raw), " ") // the same command, however it is spaced
	sum := sha256.Sum256([]byte(cmd))
	key := hex.EncodeToString(sum[:8])
	asked := false
	var phase string
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Mode != state.ModeRunning || holdCall(st, false, decide.Words{}) != "" || slices.Contains(st.Run.BackgroundQuestioned, key) {
			return nil
		}
		st.Run.BackgroundQuestioned = append(st.Run.BackgroundQuestioned, key)
		asked, phase = true, st.Current
		return nil
	})
	if err != nil || !asked {
		return Result{}, false
	}
	w := words(c, s)
	f := map[string]any{"phase": phase, "tool": tool, "command": recorded(raw), "type": kind}
	var res Result
	if kind == "wait_loop" {
		if timeout > 0 {
			f["timeout_seconds"] = int(timeout.Seconds())
		}
		res = permission("deny", w.WaitLoop(clipWhat(cmd), timeout))
		res.Output["systemMessage"] = w.WaitLoopSaid(clipWhat(cmd), timeout)
	} else {
		res = permission("deny", w.BackgroundCheck(clipWhat(cmd)))
		res.Output["systemMessage"] = w.BackgroundCheckSaid(clipWhat(cmd))
	}
	s.Event("background_questioned", f)
	return res, true
}
