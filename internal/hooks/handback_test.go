package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// handbackCall is a PreToolUse's input for an agent's call of Claude Code's hand-back tool.
func handbackCall(id string) map[string]any {
	return map[string]any{"tool_name": HandbackTool, "tool_use_id": id, "tool_input": map[string]any{"message": "P2 is done."}}
}

// A phase agent's hand-back is its stop (Claude Code 2.1.302), and no hook can refuse a stop once a
// hand-back ended the turn: baton refuses the hand-back where it would refuse the stop. With its phase in
// hand, not reported, and nothing it waits on, it is told to report, with the stop's words; with a result
// or a message due, it is given that first; and it goes through while it waits on something that wakes it,
// or once its phase is done.
func TestAPhaseAgentsHandbackIsDecidedAsItsStop(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(st *state.State, now time.Time)
		want string // what the refusal says, "" for a hand-back let through
		why  string // handback_refused's why
	}{
		{"its phase not reported", func(*state.State, time.Time) {}, decide.HandbackHeld(decide.Words{Worker: true, Git: true}.NoStatus("P2", "P2 (Third)")), "no status"},
		{"a result due", func(st *state.State, now time.Time) {
			st.Jobs = append(st.Jobs, state.Job{ID: "P2.j1", Phase: "P2", Kind: state.JobAwait, Command: "sh hold.sh", Status: state.JobPassed, Started: now.Add(-time.Minute), Ended: now})
		}, decide.HandbackFirst + "\n\n[baton] Job P2.j1 (P2) passed", "results"},
		{"a message due", func(st *state.State, now time.Time) {
			state.Tell(st, state.Message{Kind: state.MessageWords, Text: "[baton] The human wrote to you."}, now)
		}, decide.HandbackFirst + "\n\n[baton] The human wrote to you.", "mail"},
		{"a job it waits on", func(st *state.State, now time.Time) {
			st.Jobs = append(st.Jobs, state.Job{ID: "P2.j1", Phase: "P2", Kind: state.JobAwait, Command: "sh hold.sh", Status: state.JobRunning, Started: now})
		}, "", ""},
		{"its proposal put to the human", func(st *state.State, now time.Time) {
			state.AddProposal(st, state.Decision{What: "use a fake", Because: "no clock", Relayed: true}, now)
		}, "", ""},
		{"its block", func(st *state.State, now time.Time) { state.SetBlocked(st, "x", now) }, "", ""},
		{"its phase done", func(st *state.State, now time.Time) {
			st.Phases["P2"].Status, st.Current, st.Mode = state.PhaseDone, "", state.ModeComplete
		}, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _, _ := newWorker(t)
			w.fire("SubagentStart", map[string]any{})
			w.set(func(st *state.State) { c.set(st, w.now) })
			out := w.fire("PreToolUse", handbackCall("toolu_1"))
			hso, _ := out["hookSpecificOutput"].(map[string]any)
			why, _ := hso["permissionDecisionReason"].(string)
			if c.want == "" {
				if hso["permissionDecision"] == "deny" {
					t.Fatalf("refused: %v", out)
				}
				if n := len(w.eventsOf("handback_refused")); n != 0 {
					t.Errorf("%d refusals logged:\n%s", n, w.eventLog())
				}
				return
			}
			if hso["permissionDecision"] != "deny" || !strings.HasPrefix(why, c.want) {
				t.Errorf("said %q, want it to start %q", why, c.want)
			}
			if said(out) == "" {
				t.Error("the human was told nothing")
			}
			if !w.logged("handback_refused", map[string]any{"phase": "P2", "why": c.why}) {
				t.Errorf("not logged:\n%s", w.eventLog())
			}
			switch c.why {
			case "results":
				if st := w.state(); st.Jobs[0].Delivered.IsZero() || !w.logged("results_delivered", map[string]any{"via": "handback"}) {
					t.Errorf("the result was not given: %+v\n%s", st.Jobs[0], w.eventLog())
				}
			case "mail":
				if st := w.state(); len(st.Inbox) != 0 || !w.logged("message_delivered", map[string]any{"via": "handback"}) {
					t.Errorf("the message was not given: %+v\n%s", st.Inbox, w.eventLog())
				}
			}
		})
	}
}

// A hand-back with the phase not reported is refused as many times as the stop would be, counted with
// the stop's refusals; then it goes through, and the agent's loop nudges it as for any agent gone quiet.
func TestAPhaseAgentsHandbackIsRefusedAsOftenAsItsStop(t *testing.T) {
	w, _, _ := newWorker(t)
	w.fire("SubagentStart", map[string]any{})
	if out := w.fire("SubagentStop", map[string]any{}); out["decision"] != "block" {
		t.Fatalf("the stop: %v", out)
	}
	for i := 2; i <= MaxStopBlocks; i++ {
		if why := denied(w, handbackCall("toolu_1")); why == "" {
			t.Fatalf("hand-back %d went through", i)
		}
	}
	if why := denied(w, handbackCall("toolu_1")); why != "" {
		t.Errorf("hand-back past the stop's refusals refused: %q", why)
	}
	if n := len(w.eventsOf("handback_refused")); n != MaxStopBlocks-1 {
		t.Errorf("%d refusals logged:\n%s", n, w.eventLog())
	}
}

// The lead's own agents, and the lead's model's, are none of baton's: their hand-backs go through.
func TestAHandbackNotAPhaseAgentsGoesThrough(t *testing.T) {
	_, lead, _ := newWorker(t)
	in := handbackCall("toolu_1")
	in["agent_id"], in["agent_type"] = "agent-explore", "Explore"
	if out := lead.fire("PreToolUse", in); len(out) != 0 {
		t.Errorf("the lead's own agent's hand-back: %v", out)
	}
	if n := len(lead.eventsOf("handback_refused")); n != 0 {
		t.Errorf("%d refusals logged:\n%s", n, lead.eventLog())
	}
}

// Deciding a hand-back fails open: a panic in it exits 0 and writes nothing.
func TestDecidingAHandbackFailsOpen(t *testing.T) {
	failsOpen(t, []failCase{
		{"a phase agent's hand-back", "PreToolUse", func(tree string) map[string]any { return phaseAgent(handbackCall("toolu_1"), tree) }, nil},
	})
}
