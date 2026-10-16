package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// turn starts a turn of the lead's, as by says who started it, and moves the clock on a second.
func (f *fixture) turn(by string) {
	f.t.Helper()
	prompt := map[string]string{
		"human":  "fix the typo in the README while you are at it",
		"baton":  NudgePrefix + " Job j1 (P0) finished; its result is attached.",
		"system": "<task-notification>\n<status>completed</status>\n</task-notification>",
	}[by]
	f.fire("UserPromptSubmit", map[string]any{"prompt": prompt})
	if got := f.state().Run.TurnBy; got != by {
		f.t.Fatalf("the turn is %q's, want %q's", got, by)
	}
	f.now = f.now.Add(time.Second)
}

// takenUp has the host take P1 up as its settle does once P0's verification passed with no phase in hand:
// P1 is owed a boundary, whose compaction the host queues itself.
func (f *fixture) takenUp() {
	f.set(func(st *state.State) {
		st.Phases["P0"] = &state.PhaseState{Status: state.PhaseDone, DoneAt: f.now}
		st.Current = "P1"
		st.OweBoundary(f.now)
		st.Run.Compaction.Queue("boundary", f.now)
	})
	f.now = f.now.Add(time.Second)
}

// A boundary baton queued itself never cuts a turn short: one under way when it was queued, or one the
// human started, runs to its end, and the model is told once, with a call's result, not to start the phase
// in it; the compaction waits for that turn's stop. The boundary the model's own `baton done` owes holds
// the rest of its turn, as does one baton queued before a turn it or Claude Code started. A held boundary
// says what comes next: a phase begins, or one whose verification failed reopens.
func TestABoundaryBatonQueuedLetsTheTurnRunOn(t *testing.T) {
	red := func(f *fixture) {
		f.set(func(st *state.State) {
			st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1,
				Queued: f.now.Add(-2 * time.Minute), Started: f.now.Add(-2 * time.Minute), Ended: f.now})
			st.Phases["P0"] = &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: state.ParkRed, Since: f.now, Job: "j1"}}
			st.Current = "P0"
			st.OweBoundary(f.now)
			st.Run.Compaction.Queue("boundary", f.now)
		})
		f.now = f.now.Add(time.Second)
	}
	switched := func(f *fixture) {
		f.set(func(st *state.State) {
			st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Status: state.JobRunning, Queued: f.now.Add(-10 * time.Minute)})
			st.Phases["P0"] = &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: state.ParkJob, Since: f.now, Job: "j1"}}
			st.Current = "P1"
			st.OweBoundary(f.now)
			st.Run.Compaction.Queue("switch", f.now)
			st.Run.Compaction.Switch = &state.Switched{From: "P0", To: "P1", Job: "j1"}
		})
		f.now = f.now.Add(time.Second)
	}
	for _, c := range []struct {
		name  string
		steps func(f *fixture)
		held  string // the hold's reason; "" when the turn runs on
	}{
		{"queued while a turn baton started runs", func(f *fixture) { f.turn("baton"); f.takenUp() }, ""},
		{"queued while a turn Claude Code started runs", func(f *fixture) { f.turn("system"); f.takenUp() }, ""},
		{"the human's turn, started while it was queued", func(f *fixture) { f.takenUp(); f.turn("human") }, ""},
		{"the human's turn after a switch", func(f *fixture) { switched(f); f.turn("human") }, ""},
		{"the human's turn after a reopened phase was taken up", func(f *fixture) { red(f); f.turn("human") }, ""},
		{"a turn baton started while it was queued", func(f *fixture) { f.takenUp(); f.turn("baton") },
			"[baton] End your turn now: P1 begins after baton compacts the context. Do not start further work in this turn."},
		{"a reopened phase, in a turn baton started", func(f *fixture) { red(f); f.turn("baton") },
			"[baton] End your turn now: P0's verification failed (exit 1) after 2m (job j1), so P0 reopens after baton compacts the context. Do not start further work in this turn."},
		{"a switch, in a turn Claude Code started", func(f *fixture) { switched(f); f.turn("system") },
			"[baton] End your turn now: baton set P0 aside while its job j1 runs, and P1 begins after baton compacts the context. Do not start further work in this turn."},
		{"the model's own done, in the human's turn", func(f *fixture) {
			f.turn("human")
			pl, _ := f.store.LoadPlan()
			f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, state.DoneOpts{}); return err })
		}, "[baton] End your turn now: P1 begins after baton compacts the context. Do not start further work in this turn."},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			c.steps(f)
			if got := denyReason(f.fire("PreToolUse", map[string]any{"tool_name": "Edit", "tool_use_id": "toolu_1"})); got != c.held {
				t.Fatalf("the call's hold:\n got %q\nwant %q", got, c.held)
			}
			if c.held != "" {
				if out := f.fire("PostToolUse", bash("ls")); strings.Contains(feedback(out), "once this turn ends") || f.logged("boundary_waits", nil) {
					t.Errorf("a held turn is told it runs on: %v", out)
				}
				f.fire("Stop", map[string]any{})
				if comp := f.state().Run.Compaction; comp.Status != state.CompactQueued || !comp.StartsPhase() {
					t.Errorf("the stop left the compaction %+v", comp)
				}
				return
			}
			st := f.state()
			want, says := decide.BoundaryWaits(st), decide.BoundaryWaitsSay(st)
			out := f.fire("PostToolUse", map[string]any{"tool_name": "Edit", "tool_use_id": "toolu_1"})
			if !strings.Contains(feedback(out), want) || !strings.Contains(say(out), says) {
				t.Fatalf("the model is told %q and the human %q, want %q and %q", feedback(out), say(out), want, says)
			}
			if !f.logged("boundary_waits", map[string]any{"phase": st.Current, "reason": st.Run.Compaction.Reason, "turn_by": st.Run.TurnBy}) {
				t.Errorf("events: %v", f.events())
			}
			if out := f.fire("PostToolUse", bash("ls")); strings.Contains(feedback(out), "once this turn ends") {
				t.Errorf("told again: %v", out)
			}
			epoch := st.Run.Compaction.Epoch
			if out := f.fire("Stop", map[string]any{}); out["decision"] == "block" {
				t.Fatalf("the turn's stop was refused: %v", out)
			}
			if comp := f.state().Run.Compaction; comp.Status != state.CompactQueued || comp.Epoch != epoch || f.state().Run.TurnOpen {
				t.Errorf("after the stop: compaction %+v, turn open %v", comp, f.state().Run.TurnOpen)
			}
		})
	}
}

// Wherever baton's sentences name the phase in hand and the lead holds none (every phase left is a phase
// agent's or waits on a job), they name the plan instead of leaving a blank.
func TestNoSentenceHasABlankForThePhase(t *testing.T) {
	none := func(st *state.State) { st.Current = "" }
	t.Run("a block, and the human's Continue", func(t *testing.T) {
		f := newFixture(t, true)
		f.set(none)
		q := f.blocked("need a key")
		if q != "baton: blocked on the plan: need a key" {
			t.Fatalf("question %q", q)
		}
		out := f.fire("PostToolUse", answer(ask(q, blockedLabels...), "Continue"))
		if got := feedback(out); !strings.HasSuffix(got, "Carry on with the plan.") {
			t.Fatalf("told %q", got)
		}
	})
	t.Run("a proposal's Go ahead", func(t *testing.T) {
		f := newFixture(t, true)
		call := f.propose(false)
		f.fire("PermissionRequest", call)
		f.set(none)
		out := f.fire("PostToolUse", answer(call, "Go ahead"))
		if got := feedback(out); !strings.Contains(got, "Then carry on with the plan.") {
			t.Fatalf("told %q", got)
		}
	})
	t.Run("a review's Continue", func(t *testing.T) {
		f := newFixture(t, true)
		f.set(func(st *state.State) {
			state.AddNote(st, "skipped the flaky test", "re-enable it", f.now)
			state.DueReview(st, "P0", f.now)
			st.Current = ""
		})
		f.fire("Stop", map[string]any{})
		q := ask(f.state().Run.Escalation.Question, "Continue", "Pause baton")
		f.fire("PermissionRequest", q)
		out := f.fire("PostToolUse", answer(q, "Continue"))
		if got := feedback(out); !strings.HasSuffix(got, "Carry on with the plan.") {
			t.Fatalf("told %q", got)
		}
	})
	t.Run("a new plan approved, and the run kept", func(t *testing.T) {
		f := newFixture(t, false)
		f.startRun()
		f.set(none)
		f.approve(f.writePlan("after-clear.md", approvedDoc))
		q := ask(f.state().PlanQuestion.Question, "Wait for me", "Start the new plan from P0", "Keep the current run")
		if strings.Contains(q["tool_input"].(map[string]any)["questions"].([]any)[0].(map[string]any)["question"].(string), "(at )") {
			t.Fatalf("asked %v", q)
		}
		out := f.fire("PostToolUse", answer(q, "Keep the current run"))
		if got := feedback(out); !strings.HasSuffix(got, "carry on with the plan.") {
			t.Fatalf("told %q", got)
		}
	})
	t.Run("the phase's title", func(t *testing.T) {
		f := newFixture(t, true)
		pl, _ := f.store.LoadPlan()
		if got := phaseTitle(pl, ""); got != "the plan" {
			t.Errorf("phaseTitle of none: %q", got)
		}
		if got := phaseTitle(pl, "P1"); got != "P1 (Second)" {
			t.Errorf("phaseTitle of P1: %q", got)
		}
	})
}

// Wait for me on baton's question about the run holds the run for the human: a hold, which their next
// message answers (an older proposal held for them is not what it answers), and which lapses with no word
// from them (loop's lapseHold). The model ends its turn, and its stop goes through.
func TestWaitForMeHoldsTheRun(t *testing.T) {
	f := newFixture(t, true)
	reason := "the model stopped 4 times on P0 without reporting status"
	f.set(func(st *state.State) {
		old, _ := state.AddProposal(st, state.Decision{What: "use a fake clock", Because: "b", Undo: "x", Deadline: f.now.Add(-time.Hour), Question: "baton: old?"}, f.now.Add(-2*time.Hour))
		state.Resolve(st, old.ID, state.ByHeld, "Wait for me", f.now.Add(-2*time.Hour))
		st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: reason, Since: f.now, Question: "baton: " + reason, Asked: true}
	})
	f.now = f.now.Add(time.Minute)
	q := ask("baton: "+reason, labelsOf(decide.EscalationOptions)...)
	f.fire("PermissionRequest", q)
	if d, _ := f.state().Run.Dialogs.Front(); d.Kind != "escalation" {
		t.Fatalf("dialog %+v", d)
	}
	out := f.fire("PostToolUse", answer(q, "Wait for me"))
	e := f.state().Run.Escalation
	if e == nil || e.Kind != "decision" || e.Reason != reason || !e.Since.Equal(f.now) || !e.Asked || e.Question != "" {
		t.Fatalf("escalation %+v", e)
	}
	if !strings.Contains(say(out), "holding the run for you") || !strings.Contains(feedback(out), "End your turn now") ||
		!f.logged("escalated", map[string]any{"type": "decision", "answer": "Wait for me", "by": state.ByHuman}) {
		t.Fatalf("out %v; events:\n%s", out, f.eventLog())
	}
	if stop := f.fire("Stop", map[string]any{}); stop["decision"] == "block" || !strings.Contains(say(stop), "waiting on you — "+reason) {
		t.Fatalf("stop %v", stop)
	}
	out = f.fire("UserPromptSubmit", map[string]any{"prompt": "go on, but skip the flaky test"})
	if f.state().Run.Escalation != nil || !strings.Contains(additionalContext(out), decide.HoldReleased(reason)) ||
		!f.logged("hold_released", map[string]any{"reason": reason, "by": state.ByHuman}) || f.logged("hold_released", map[string]any{"id": "d1"}) {
		t.Fatalf("after the human wrote: %v; events:\n%s", out, f.eventLog())
	}
}
