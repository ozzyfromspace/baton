package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// backgroundCheck refuses, once, a check (lane.IsCheck) the model starts as a background shell task of its
// own while its plan runs: a Bash or PowerShell call with run_in_background, from the lead or a phase agent
// (a subagent of its model's is left alone, as the holds leave it). baton sees nothing of such a task, so
// nothing records it, it holds no lane, and its failures reach the model only when it ends: it is pointed to
// baton verify (decide.BackgroundCheck). The same command again goes through, so nothing waits on anyone; a
// call a hold of the run's would refuse anyway is left to that hold, so the one question is not spent on
// it. Logged background_questioned. It reports whether it refused, and fails open: a run it cannot read
// refuses nothing.
func (h *handlers) backgroundCheck(c Context) (Result, bool) {
	tool := str(c.Input, "tool_name")
	if tool != "Bash" && tool != "PowerShell" || c.Agent != "" {
		return Result{}, false
	}
	ti, _ := c.Input["tool_input"].(map[string]any)
	raw := str(ti, "command")
	if bg, _ := ti["run_in_background"].(bool); !bg || raw == "" {
		return Result{}, false
	}
	if baton, _ := batonShell(raw); baton || !lane.IsCheck(raw) {
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
	s.Event("background_questioned", map[string]any{"phase": phase, "tool": tool, "command": clip(cmd, recordClip)})
	res := permission("deny", w.BackgroundCheck(clipWhat(cmd)))
	res.Output["systemMessage"] = w.BackgroundCheckSaid(clipWhat(cmd))
	return res, true
}
