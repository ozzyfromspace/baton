package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// While the human wraps the run up, a fresh phase agent's brief says so, read from the lead's run, and the
// message left for the agent before it is not given again; so does the lead's brief at a phase boundary. With
// no wrap, neither brief says so.
func TestAFreshAgentsBriefCarriesTheWrap(t *testing.T) {
	for _, c := range []struct {
		name  string
		brief func(t *testing.T, wrapped bool) (text, phase string, inbox []state.Message)
	}{
		{"a fresh agent's", func(t *testing.T, wrapped bool) (string, string, []state.Message) {
			w, lead, _ := newWorker(t)
			if wrapped {
				lead.set(func(st *state.State) { st.Wrap = &state.Wrap{At: lead.now, By: state.WrapByHuman} })
				w.set(func(st *state.State) {
					state.Tell(st, state.Message{Kind: state.MessageWrap, Text: decide.WrapUp, Phase: "P2", About: "wrap"}, w.now)
				})
			}
			text := additionalContext(w.fire("SubagentStart", map[string]any{}))
			return text, "P2", w.state().Inbox
		}},
		{"the lead's, at a boundary", func(t *testing.T, wrapped bool) (string, string, []state.Message) {
			f, pl := planned(t, "# Two\n\n## P0 — A\n\nDo a.\n\n## P1 — B\n\nDo b.\n")
			f.vars = map[string]string{"BATON_AUTO_REPLAN": "0"}
			f.set(func(st *state.State) {
				state.Done(st, pl, "P0", f.now, state.DoneOpts{})
				state.Start(st, f.now, state.Origin{})
				st.Run.Briefing = &state.Briefing{Kind: "boundary", Phase: "P1", At: f.now}
				if wrapped {
					st.Wrap = &state.Wrap{At: f.now, By: state.WrapByHuman}
				}
			})
			return feedback(f.fire("PostToolUse", bash("ls"))), "P1", nil
		}},
	} {
		for _, wrapped := range []bool{true, false} {
			name := c.name
			if wrapped {
				name += ", wrapping up"
			}
			t.Run(name, func(t *testing.T) {
				text, phase, inbox := c.brief(t, wrapped)
				if !strings.Contains(text, "Phase "+phase+", from the plan:") {
					t.Fatalf("no brief:\n%s", text)
				}
				if got := strings.Contains(text, decide.WrapBrief(phase)); got != wrapped {
					t.Errorf("the brief says the run wraps up: %v, want %v\n%s", got, wrapped, text)
				}
				for _, m := range inbox {
					if m.Kind == state.MessageWrap {
						t.Errorf("the message is still to be given: %+v", m)
					}
				}
			})
		}
	}
}
