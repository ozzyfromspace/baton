package hooks

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A block's question reaching its dialog logs whether it said that nothing else in the run can move
// (run_waits), as the host issued it; any other question's says nothing of it.
func TestABlockAskedSaysWhetherTheRunWaits(t *testing.T) {
	block := state.Relay{Kind: state.RelayBlocked, Since: time.Date(2026, 10, 23, 5, 36, 0, 0, time.UTC), Because: "need a key"}
	for _, c := range []struct {
		name string
		r    state.Relay
		w    decide.RunWait
	}{
		{"a block, the rest goes on", block, decide.RunWait{}},
		{"a block, nothing else can move", block, decide.RunWait{Stuck: true, Behind: []string{"P3"}}},
		{"a proposal", relayProposal, decide.RunWait{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead, _ := newWorker(t)
			queue(lead, c.r)
			lead.set(func(st *state.State) {
				q := st.FrontRelay()
				deadline := lead.now.Add(5 * time.Minute)
				state.IssueRelay(q, decide.RelayQuestion(*q, deadline, c.w).Text, 4, lead.inst, deadline, lead.now)
				q.RunWaits = c.w.Stuck
			})
			lead.fire("PermissionRequest", modAskOf(lead))
			var asked map[string]any
			for _, e := range lead.events() {
				if e["kind"] == "relay_asked" {
					asked = e
				}
			}
			waits, logged := asked["run_waits"]
			switch {
			case asked == nil:
				t.Fatalf("not asked:\n%s", lead.eventLog())
			case c.r.Kind != state.RelayBlocked && logged:
				t.Errorf("a %s logged run_waits: %v", c.r.Kind, asked)
			case c.r.Kind == state.RelayBlocked && waits != c.w.Stuck:
				t.Errorf("run_waits %v, want %v: %v", waits, c.w.Stuck, asked)
			}
		})
	}
}
