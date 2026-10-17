package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// failingGate puts on record gate id of phase, running for 10 minutes, which printed its first failure 8
// minutes in.
func (f *fixture) failingGate(id, phase string, here bool) {
	f.t.Helper()
	f.set(func(st *state.State) {
		st.Jobs = append(st.Jobs, state.Job{ID: id, Phase: phase, Kind: state.JobVerify, Command: "make e2e", Commit: "4f2a9c1d2e3f", Status: state.JobRunning,
			Here: here, Started: f.now.Add(-10 * time.Minute), Failing: &state.Failing{At: f.now.Add(-2 * time.Minute), Line: "--- FAIL: TestLogin (0.20s)", Stage: "e2e"}})
	})
}

// failHead is how the look-in at gate j1 of P0 (failingGate) starts.
const failHead = "[baton] Job j1 (P0) is failing as it runs: `make e2e` printed this 8m in, in its stage e2e:\n\n    --- FAIL: TestLogin (0.20s)"

// movedOn puts the lead where its `baton done P0`, with its gate running, and the compaction after it
// leave it: P0 verifying on j1, and P1 in hand. P1 must not wait for P0 (independent).
func (f *fixture) movedOn() {
	f.t.Helper()
	pl, err := f.store.LoadPlan()
	if err != nil {
		f.t.Fatal(err)
	}
	f.set(func(st *state.State) {
		if _, err := state.Done(st, pl, "P0", f.now, state.DoneOpts{}); err != nil {
			f.t.Fatal(err)
		}
		st.BoundaryOwed = false
		state.Start(st, f.now, state.Origin{})
	})
	if st := f.state(); st.Phases["P0"].Status != state.PhaseVerifying || st.Current != "P1" {
		f.t.Fatalf("not moved on: P0 %+v, in hand %q", st.Phases["P0"], st.Current)
	}
}

// A gate's first failure reaches the lead once, by whichever hook comes first, whether its phase is in
// hand or `baton done` moved the run on: a tool call, its stop, a prompt.
func TestAFirstFailureReachesTheLead(t *testing.T) {
	bash := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}}
	for _, c := range []struct {
		name, via string
		moved     bool
	}{
		{"mid-turn", "tool", false},
		{"mid-turn, after done", "tool", true},
		{"at its stop, after done", "stop", true},
		{"with a prompt, after done", "prompt", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := independent(t)
			f.failingGate("j1", "P0", false)
			if c.moved {
				f.movedOn()
			}
			var got string
			var out map[string]any
			switch c.via {
			case "tool":
				out = f.fire("PostToolUse", bash)
				got = feedback(out)
			case "stop":
				out = f.fire("Stop", map[string]any{})
				got = feedback(out)
			case "prompt":
				out = f.fire("UserPromptSubmit", map[string]any{"prompt": "[baton] A gate of yours is failing as it runs."})
				got = additional(out)
			}
			for _, want := range []string{failHead, "The gate runs on", "`baton stop j1`", "`baton verify --failed`"} {
				if !strings.Contains(got, want) {
					t.Errorf("lacks %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "holds landings") {
				t.Errorf("a gate in a lane holds no landing:\n%s", got)
			}
			if want := "baton: job j1 (`make e2e`, P0) printed a failure 8m in, and runs on → told Claude"; !strings.Contains(said(out), want) {
				t.Errorf("told the human %q", said(out))
			}
			if !f.logged("look_in_told", map[string]any{"type": state.LookFailing, "job": "j1", "via": c.via, "stage": "e2e", "seconds": float64(480)}) {
				t.Errorf("not logged:\n%s", f.eventLog())
			}
			if j := f.state().Job("j1"); !j.FailTold.Equal(f.now) {
				t.Errorf("not recorded told: %+v", j)
			}
			if out := f.fire("PostToolUse", bash); strings.Contains(feedback(out), "failing as it runs") {
				t.Errorf("told twice: %v", out)
			}
		})
	}
}

// While a compaction is owed, a first failure waits for it, and comes in the brief the next phase starts
// from, which the rewake gives.
func TestAFirstFailureComesInTheBriefAfterTheBoundary(t *testing.T) {
	f, pl := independent(t)
	f.failingGate("j1", "P0", false)
	f.set(func(st *state.State) { state.Done(st, pl, "P0", f.now, state.DoneOpts{}) })
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}}); strings.Contains(feedback(out), failHead) {
		t.Fatalf("told while a compaction is owed: %v", out)
	}
	if out := f.fire("Stop", map[string]any{}); strings.Contains(feedback(out), failHead) {
		t.Fatalf("its stop was refused for it while a boundary is owed: %v", out)
	}
	code, msg := f.compacted("manual")
	if code != 2 || !strings.Contains(msg, "Now: P1 — Second") || !strings.Contains(msg, failHead) {
		t.Fatalf("rewake %d:\n%s", code, msg)
	}
	if !f.logged("look_in_told", map[string]any{"type": state.LookFailing, "via": "brief"}) {
		t.Errorf("not logged:\n%s", f.eventLog())
	}
	if !strings.Contains(strings.Join(f.state().Run.Announce, "\n"), "printed a failure 8m in, and runs on → told Claude") {
		t.Errorf("the human is not told: %q", f.state().Run.Announce)
	}
}

// A failing gate in the lead's checkout names the landings it holds, by phase.
func TestAFailingGateHereNamesTheLandingsItHolds(t *testing.T) {
	f := newFixture(t, true)
	f.failingGate("j1", "P0", true)
	f.set(func(st *state.State) {
		st.Workers = []state.Worker{{Phase: "P1", Ref: "r/P1", Status: state.WorkerLanding, Merge: &state.Merge{Queued: f.now}}}
	})
	got := feedback(f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}}))
	if want := "While it runs in the working tree, it holds landings: P1 waits to land"; !strings.Contains(got, want) {
		t.Errorf("lacks %q:\n%s", want, got)
	}
}

// A look-in the host left reaches the model with the next hook, as a first failure does, and goes once.
func TestALookInTheHostLeftReachesTheModel(t *testing.T) {
	f, _ := independent(t)
	f.set(func(st *state.State) {
		st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Status: state.JobRunning, Started: f.now.Add(-time.Hour)})
		state.AddLookIn(st, state.LookIn{Kind: state.LookSlow, Job: "j1", Text: "[baton] Job j1 runs long.", Say: "baton: job j1 runs long", At: f.now})
	})
	f.movedOn()
	out := f.fire("Stop", map[string]any{})
	if got := feedback(out); !strings.Contains(got, "[baton] Job j1 runs long.") {
		t.Fatalf("its stop gave %q", got)
	}
	if !strings.Contains(said(out), "baton: job j1 runs long → told Claude") || !f.logged("look_in_told", map[string]any{"type": state.LookSlow, "job": "j1", "via": "stop"}) {
		t.Errorf("said %q\n%s", said(out), f.eventLog())
	}
	if f.logged("results_delivered", nil) {
		t.Errorf("a look-in logged as results:\n%s", f.eventLog())
	}
	if st := f.state(); len(st.LookIns) != 0 || st.LookInsDue() {
		t.Errorf("left %+v", st.LookIns)
	}
}

// A phase agent's first failure is in the brief a fresh agent starts from; one that comes after it reaches
// the agent at its hand-back, which is refused for it, and at its stop: once each, after its `baton done`
// too.
func TestAFirstFailureReachesAPhaseAgent(t *testing.T) {
	const head = "[baton] Job P2.j1 (P2) is failing as it runs"
	t.Run("in its brief", func(t *testing.T) {
		w, _, _ := newWorker(t)
		w.failingGate("P2.j1", "P2", false)
		out := w.fire("SubagentStart", map[string]any{})
		if got := additional(out); !strings.Contains(got, "Now: P2") || !strings.Contains(got, head) {
			t.Fatalf("brief:\n%s", got)
		}
		if !w.logged("look_in_told", map[string]any{"type": state.LookFailing, "job": "P2.j1", "via": "brief"}) {
			t.Errorf("not logged:\n%s", w.eventLog())
		}
		if got := feedback(w.fire("SubagentStop", map[string]any{"stop_hook_active": false})); strings.Contains(got, head) {
			t.Errorf("given again at its stop: %q", got)
		}
	})
	for _, via := range []string{"handback", "stop"} {
		t.Run("at its "+via+", after done", func(t *testing.T) {
			w, _, _ := newWorker(t)
			w.fire("SubagentStart", map[string]any{})
			w.failingGate("P2.j1", "P2", false)
			pl, _ := w.store.LoadPlan()
			w.set(func(st *state.State) {
				if _, err := state.Done(st, pl, "P2", w.now, state.DoneOpts{}); err != nil {
					t.Fatal(err)
				}
			})
			if st := w.state(); st.Current != "" || st.Phases["P2"].Status != state.PhaseVerifying {
				t.Fatalf("in hand %q, P2 %+v", st.Current, st.Phases["P2"])
			}
			var got string
			if via == "handback" {
				out := w.fire("PreToolUse", handbackCall("toolu_1"))
				hso, _ := out["hookSpecificOutput"].(map[string]any)
				got, _ = hso["permissionDecisionReason"].(string)
				if hso["permissionDecision"] != "deny" || !strings.HasPrefix(got, decide.HandbackFirst) {
					t.Fatalf("hand-back: %v", out)
				}
				if !w.logged("handback_refused", map[string]any{"phase": "", "why": "look_in"}) {
					t.Errorf("not logged:\n%s", w.eventLog())
				}
			} else {
				got = feedback(w.fire("SubagentStop", map[string]any{"stop_hook_active": false}))
			}
			if !strings.Contains(got, head) {
				t.Fatalf("given %q", got)
			}
			if !w.logged("look_in_told", map[string]any{"type": state.LookFailing, "via": via}) {
				t.Errorf("not logged:\n%s", w.eventLog())
			}
			out := w.fire("PreToolUse", handbackCall("toolu_2"))
			if hso, _ := out["hookSpecificOutput"].(map[string]any); hso["permissionDecision"] == "deny" {
				t.Errorf("its hand-back is refused again: %v", out)
			}
		})
	}
}
