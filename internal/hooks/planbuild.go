package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// unbuilt answers the lead's ExitPlanMode, in a hosted session, for a plan with phase headings that baton
// could not attach as written (plan.Build of its suggestion fails: a duplicate id, an After: line naming a
// phase it cannot wait for, an anchor that occurs twice): it is turned back with what keeps it from
// attaching, so the model fixes the plan before the human approves it, rather than after. Once per plan
// text: the same text again goes on to the human, whose approval then says it was not attached, so it
// never loops. It comes before the directives turn-back, which asks about a plan baton can attach.
func (h *handlers) unbuilt(c Context) (Result, bool) {
	if str(c.Input, "tool_name") != "ExitPlanMode" || str(c.Input, "agent_id") != "" {
		return Result{}, false
	}
	ti, _ := c.Input["tool_input"].(map[string]any)
	text := str(ti, "plan")
	if text == "" {
		return Result{}, false
	}
	spec := plan.Suggest([]byte(text))
	if !plan.Shaped(spec) {
		return Result{}, false // not a baton plan
	}
	_, err := plan.Build("", []byte(text), spec)
	if err == nil {
		return Result{}, false
	}
	sum := sha256.Sum256([]byte(text))
	key := hex.EncodeToString(sum[:8])
	asked := false
	s, _, uerr := h.update(c, func(st *state.State, s *state.Store) error {
		if s.Worker() != "" || slices.Contains(st.Run.UnbuiltAsked, key) {
			return nil
		}
		st.Run.UnbuiltAsked = append(st.Run.UnbuiltAsked, key)
		st.Run.DirectivesPending = key // a stop before the plan comes again is sent back to put it to the human
		asked = true
		return nil
	})
	if uerr != nil || !asked {
		return Result{}, false
	}
	why := strings.Split(err.Error(), "\n")
	file := str(ti, "planFilePath")
	if file == "" {
		file = key
	}
	s.Event("plan_unbuilt", map[string]any{"plan": file, "why": why[0], "problems": len(why)})
	res := permission("deny", decide.PlanUnbuilt(why))
	res.Output["systemMessage"] = decide.PlanUnbuiltSaid(why[0])
	return res, true
}
