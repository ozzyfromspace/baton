package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent starting fresh is briefed with the run's environment facts, which its sub-run does not
// keep, so a fact left for it before it started is not given again.
func TestAFreshAgentsBriefListsTheRunsFacts(t *testing.T) {
	r := newAgentRun(t)
	var fact state.Fact
	r.store.Update(func(st *state.State) error {
		fact, _ = state.AddFact(st, "leave host port 5432 free: the dev stack holds it", "P1", r.now)
		return nil
	})
	r.sub.Update(func(st *state.State) error {
		state.Tell(st, state.Message{Kind: state.MessageFact, Text: decide.FactTold(fact), Phase: "P2"}, r.now)
		return nil
	})
	r.fire("PreToolUse", spawnCall())
	if code, _, errs := r.worktree("agent-a1"); code != 0 {
		t.Fatalf("worktree: %s", errs)
	}
	out := r.fire("SubagentStart", asAgent(map[string]any{}, "a1", r.entry.Worktree))
	ctx, _ := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, decide.EnvFacts([]state.Fact{fact})) {
		t.Errorf("the brief lacks the run's facts:\n%s", ctx)
	}
	for _, m := range loaded(t, r.sub).Inbox {
		if m.Kind == state.MessageFact {
			t.Errorf("the fact is still to be given: %+v", m)
		}
	}
}
