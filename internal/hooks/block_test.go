package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A block is put to the human with Look again third, where baton's default sits, and the option says when
// baton picks it.
func TestABlocksQuestionOffersLookAgain(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error { return state.SetBlocked(st, "need a key", f.now) })
	out := f.fire("Stop", map[string]any{})
	why, _ := out["reason"].(string)
	for _, w := range []string{`"baton: blocked on P0: need a key"`, `exactly 3 options, in this order: "Continue"`, `"Pause baton"`,
		`"Look again" (description: "Claude looks for a safe way around it and proposes it first; baton picks this if nobody answers in 30 minutes")`} {
		if !strings.Contains(why, w) {
			t.Errorf("the model is not told %q:\n%s", w, why)
		}
	}
	if !f.logged("escalated", map[string]any{"type": "blocked"}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
}

// Look again lifts the block for the model to look again, as the human's choice, or as baton's once nobody
// answered for the loop's BlockLook (typed by the host where it types, or settled by the mod): announced,
// logged (block_lapsed by timeout, look_again by the human), pushed only when nobody answered, and told to
// the model. Look again on anything but a block does nothing.
func TestLookAgainLiftsTheBlock(t *testing.T) {
	for _, c := range []struct {
		name  string
		how   string // "human", "typed" (the host answered it, serial mode) or "mod"
		event string
		by    string
		tell  string
	}{
		{"the human chose it", "human", "look_again", "human", "The human chose Look again on your block on P0 (need a key)."},
		{"typed by the host", "typed", "block_lapsed", "timeout", "nobody has answered for 30 minutes, so baton lifted the block"},
		{"settled by the mod", "mod", "block_lapsed", "timeout", "nobody has answered for 30 minutes, so baton lifted the block"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			q := f.blocked("need a key")
			f.fire("PermissionRequest", ask(q, blockedLabels...))
			var told string
			switch c.how {
			case "mod":
				if own, err := Settle(f.store, q, decide.LookAgain, f.now, f.env(f.inst)); !own || err != nil {
					t.Fatalf("settled %v, %v", own, err)
				}
				told = feedback(f.fire("PostToolUse", bash("ls"))) // told at its next call
			case "typed":
				f.set(func(st *state.State) { st.Run.Dialogs[0].AutoAnswered = f.now })
				fallthrough
			default:
				told = feedback(f.fire("PostToolUse", answer(ask(q, blockedLabels...), decide.LookAgain)))
			}
			st := f.state()
			if st.Blocked != nil || st.Run.Escalation != nil || st.Mode != state.ModeRunning || st.Run.Dialogs.AnyOpen() {
				t.Fatalf("not lifted: blocked %+v, escalation %+v, mode %s, dialogs %+v", st.Blocked, st.Run.Escalation, st.Mode, st.Run.Dialogs)
			}
			for _, w := range []string{c.tell, decide.LookAgainRule(10 * time.Minute)} {
				if !strings.Contains(told, w) {
					t.Errorf("the model is not told %q:\n%s", w, told)
				}
			}
			if !f.logged(c.event, map[string]any{"by": c.by, "phase": "P0", "reason": "need a key", "answer": decide.LookAgain}) {
				t.Errorf("events:\n%s", f.eventLog())
			}
			pushed := false
			for _, n := range st.Run.Notices {
				pushed = pushed || n.Kind == "block_lapsed"
			}
			if pushed != (c.by == "timeout") {
				t.Errorf("pushed %v: %+v", pushed, st.Run.Notices)
			}
			if human := !st.Run.HumanAt.IsZero(); human != (c.by == "human") {
				t.Errorf("the human took part: %v", human)
			}

			// Blocked again for the same reason, it is put to the human again, the same way.
			f.store.Update(func(st *state.State) error { return state.SetBlocked(st, "need a key", f.now.Add(time.Minute)) })
			out := f.fire("Stop", map[string]any{})
			if why, _ := out["reason"].(string); !strings.Contains(why, `"Look again"`) || f.state().Run.Escalation == nil {
				t.Errorf("not asked again: %v", out)
			}
		})
	}

	// Not a block's question: Look again is the human's own words to it, and changes nothing.
	f := newFixture(t, true)
	f.set(func(st *state.State) {
		st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "quiet", Since: f.now, Question: "baton: quiet", Asked: true}
	})
	if out := f.fire("PostToolUse", answer(ask("baton: quiet", "Continue", "Pause baton"), decide.LookAgain)); feedback(out) != "" || f.state().Run.Escalation == nil {
		t.Errorf("acted on a stall's question: %v", out)
	}
}
