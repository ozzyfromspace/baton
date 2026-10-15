package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// ended reports the end of a turn of the agent on phase, as the mod does (its turn.complete), with why.
func (x *crew) ended(phase, why, refusal string) {
	x.report(bridge.Report{Kind: bridge.Turn, Agent: x.agent(phase).id, Reason: why, Refusal: refusal})
}

// failed fires the StopFailure of the agent on phase's turn, which an API error (kind) ended.
func (x *crew) failed(phase, kind string) {
	a := x.agent(phase)
	x.hook("StopFailure", map[string]any{"agent_id": a.id, "agent_type": state.PhaseAgent, "cwd": a.tree, "error": kind})
}

// A phase agent's turn that an API error ended is retried with the lead's backoff, by a message: an
// overload after OverloadRetry, a usage limit after RateLimitRetry, with the human told of the limit once.
func TestAnAgentsFailedTurnIsRetried(t *testing.T) {
	for _, c := range []struct {
		kind  string
		after time.Duration
	}{{"overloaded", DefaultTiming.OverloadRetry}, {"rate_limit", DefaultTiming.RateLimitRetry}} {
		t.Run(c.kind, func(t *testing.T) {
			x := newCrew(t, 1)
			p := &pushes{}
			x.c.Lead.Notify = p
			x.atWork("P1")
			x.failed("P1", c.kind)
			x.ended("P1", "error", "")
			x.wait(c.after - 10*time.Second)
			if len(x.sent) != 0 {
				t.Fatalf("retried before its time: %+v", x.sent)
			}
			x.wait(20 * time.Second)
			if len(x.sent) != 1 || !strings.Contains(x.sent[0].text, "The last turn ended with an API error ("+c.kind+"). Continue P1.") {
				t.Fatalf("sent %+v", x.sent)
			}
			if limit := strings.Contains(strings.Join(p.kinds, " "), "rate_limit"); limit != (c.kind == "rate_limit") {
				t.Errorf("the human told of a usage limit: %v (%q)", limit, p.kinds)
			}
		})
	}
}

// A phase agent's turn the model's safeguards declined is escalated for its phase, with the ways on,
// and never retried: nothing is sent to it, however long it waits.
func TestARefusedAgentIsEscalatedAndNeverRetried(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	x.ended("P1", "refusal", "cyber")
	x.wait(2 * DefaultTiming.IdleNudge)
	st, _ := x.sub("P1").Load()
	want := decide.Refused(decide.Stop{Phase: "P1", Worker: true, Dependents: []string{"P3"}}, "cyber")
	if e := st.Run.Escalation; e == nil || e.Kind != "refused" || e.Reason != want {
		t.Fatalf("escalation %+v, want %q", e, want)
	}
	if len(x.sent) != 0 {
		t.Errorf("sent %+v", x.sent)
	}
}

// A phase agent the human interrupted (Esc in its transcript) is told once to go on: its phase is not
// over. One baton stopped itself is not.
func TestAnInterruptedAgentIsToldToGoOn(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	x.ended("P1", "aborted", "")
	x.tick(time.Second)
	if len(x.sent) != 1 || x.sent[0].text != decide.AgentGoOn("P1") || !x.subLogged("P1", "agent_interrupted", nil) {
		t.Fatalf("sent %+v", x.sent)
	}
	x.wait(30 * time.Second)
	if len(x.sent) != 1 {
		t.Errorf("told again: %+v", x.sent)
	}
}

// A phase agent Claude Code cannot start again (after /clear, spike 32) is replaced by a fresh one, in its
// worktree, which takes the phase up where it stands.
func TestAnAgentThatCannotBeStartedAgainIsReplaced(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	first := x.agent("P1")
	id := x.waiting("P1")
	x.sendErr = "Agent " + first.id + " could not be resumed: No transcript found for agent ID"
	x.passed("P1", id)
	x.tick(5 * time.Second)
	x.sendErr = ""
	for range 4 {
		x.tick(time.Second)
	}
	next := x.agent("P1")
	if len(x.spawns()) != 2 || next.id == first.id || next.tree != first.tree || x.worker("P1").Status != state.WorkerWorking {
		t.Fatalf("spawns %v; the fresh agent %+v; P1's worker %+v", x.spawns(), next, x.worker("P1"))
	}
	if !x.logged("worker_resumed", map[string]any{"phase": "P1"}) || !slices.Contains(x.stopped, first.id) {
		t.Errorf("stopped %v\n%s", x.stopped, x.events())
	}
}
