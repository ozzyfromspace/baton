package brief

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// While the human wraps the run up, every brief says the phase finishes on the evidence it has, after the
// phase's section: a fresh agent hears of it there, since the message went to the agent before it. Without a
// wrap, no brief says so.
func TestABriefSaysTheRunWrapsUp(t *testing.T) {
	wrap := &state.Wrap{At: time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC), By: state.WrapByHuman}
	lead := func(t *testing.T, w *state.Wrap) (string, string) {
		pl, doc, st := setup(t)
		return Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st, Wrap: w}), "P1"
	}
	agent := func(t *testing.T, w *state.Wrap) (string, string) {
		pl, doc, st := worker(t)
		return Write(Input{Kind: Worker, Plan: pl, Doc: doc, State: st, Handoff: handoff, Words: decide.Words{Git: true, Timeout: 5 * time.Minute, Worker: true},
			Tree: "/home/me/.baton/worktrees/k/R1", Branch: "baton/run/R1", Needs: []string{"P0"}, Wrap: w}), "R1"
	}
	for _, c := range []struct {
		name  string
		brief func(*testing.T, *state.Wrap) (string, string)
		wrap  *state.Wrap
	}{
		{"the lead's, wrapping up", lead, wrap},
		{"the lead's", lead, nil},
		{"a fresh agent's, wrapping up", agent, wrap},
		{"a fresh agent's", agent, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, phase := c.brief(t, c.wrap)
			at, section := strings.Index(b, decide.WrapBrief(phase)), strings.Index(b, "Phase "+phase+", from the plan:")
			switch {
			case c.wrap == nil && strings.Contains(b, "wrapping the run up"):
				t.Errorf("the brief says the run wraps up:\n%s", b)
			case c.wrap != nil && (at < 0 || section < 0 || at < section):
				t.Errorf("the brief does not say the run wraps up after %s's section:\n%s", phase, b)
			}
		})
	}
}
