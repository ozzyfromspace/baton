package hooks

import (
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// resumeCancelling runs as a prompt cancels the run's resume timer (a test makes it panic).
var resumeCancelling = func() {}

// cancelResume cancels the run's resume timer when the human writes at the lead while it is armed
// (state/resume.go), and so the weekly wind-down's going on after the reset (state/winddown.go): the human
// takes over, so the run pauses, and /baton run gives it back. Words for a phase's agent (@P3 …) cancel
// it too, before they are routed. A /baton command that only looks (status, board) or gives the run back
// (run) cancels nothing. It returns what the human and the lead's model are told ("" for nothing), once,
// as the timer is armed no more. Whatever goes wrong, even a panic, leaves the prompt as it was.
func (h *handlers) cancelResume(c Context) (say, tell string) {
	defer func() {
		if r := recover(); r != nil {
			say, tell = "", ""
		}
	}()
	if keepsResume(str(c.Input, "prompt")) {
		return "", ""
	}
	s, err := h.d.Open(c)
	if err != nil || s == nil || s.Worker() != "" {
		return "", ""
	}
	if peek, err := s.Peek(); err != nil || peek.Usage == nil || peek.Usage.ResumeAt.IsZero() && !peek.WindArmed() {
		return "", ""
	}
	resumeCancelling()
	var u state.Usage
	limit, wind := false, false
	if _, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, c.Now) {
			return nil
		}
		wind = st.WindArmed()
		if limit = state.CancelResume(st, state.CancelPrompt, c.Now); !limit && !wind {
			return nil
		}
		state.Pause(st)
		if wind {
			state.WindPause(st, c.Now) // told here
		}
		u = *st.Usage
		return nil
	}); err != nil || !limit && !wind {
		return "", ""
	}
	if !limit {
		s.Event("usage_wait_cancelled", map[string]any{"by": state.CancelPrompt, "window": state.WindowWeekly, "paused": true, "wind_down": true})
		return decide.WindCancelled(state.CancelPrompt), decide.WindCancelledTell
	}
	fields := map[string]any{"by": state.CancelPrompt, "window": u.Window, "paused": true}
	if wind {
		fields["wind_down"] = true
	}
	s.Event("usage_wait_cancelled", fields)
	return decide.ResumeCancelled(state.CancelPrompt, false), decide.ResumeCancelledTell
}

// keepsResume reports a prompt that leaves the resume timer armed: a /baton command that only looks at the
// run, or gives it back.
func keepsResume(prompt string) bool {
	f := strings.Fields(prompt)
	if len(f) == 0 {
		return false
	}
	if f[0] == "/baton-board" {
		return true
	}
	if f[0] != "/baton" && f[0] != "/baton:baton" {
		return false
	}
	if len(f) == 1 {
		return true // help
	}
	switch f[1] {
	case "status", "board", "version", "drafts", "help", "run":
		return true
	}
	return false
}

// withCancelled is the block of the human's words for a phase's agent (addressed) with what cancelling the
// resume timer said: beside a block, its reason is all the human sees.
func withCancelled(res Result, say string) Result {
	if say == "" || res.Output == nil {
		return res
	}
	out := map[string]any{}
	for k, v := range res.Output {
		out[k] = v
	}
	reason, _ := out["reason"].(string)
	out["reason"] = strings.TrimSpace(reason + "\n" + say)
	res.Output = out
	return res
}
