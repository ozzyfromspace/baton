package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// finished puts a finished job of phase on record, with a log, and returns it.
func (f *fixture) finished(phase, status string) state.Job {
	f.t.Helper()
	log := filepath.Join(f.store.Dir, "jobs", "j1.log")
	os.MkdirAll(filepath.Dir(log), 0o755)
	if err := os.WriteFile(log, []byte("[baton] lane 1\n--- FAIL: TestRanking\nFAIL\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	j := state.Job{ID: "j1", Phase: phase, Kind: state.JobVerify, Command: "make test", Commit: "4f2a9c1d2e3f4a5b6c", Status: status, Exit: 1,
		Log: log, Started: f.now.Add(-12 * time.Minute), Ended: f.now}
	f.store.Update(func(st *state.State) error { st.Jobs = append(st.Jobs, j); return nil })
	return j
}

func additional(out map[string]any) string {
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	s, _ := hso["additionalContext"].(string)
	return s
}

func said(out map[string]any) string {
	s, _ := out["systemMessage"].(string)
	return s
}

const (
	resultHead = "[baton] Job j1 (P0) failed (exit 1) after 12m: `make test` on 4f2a9c1d2e3f."
	resultTold = "baton: job j1 (`make test`, P0) failed (exit 1) after 12m → told Claude"
)

// Mid-turn, a result reaches the model with the next tool call of the main agent, once.
func TestAResultReachesTheModelMidTurn(t *testing.T) {
	f := newFixture(t, true)
	f.finished("P0", state.JobFailed)
	bash := func(agent string) map[string]any {
		in := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go build ./..."}, "tool_response": map[string]any{}}
		if agent != "" {
			in["agent_id"] = agent
		}
		return f.fire("PostToolUse", in)
	}
	if out := bash("sub-1"); feedback(out) != "" {
		t.Fatalf("a subagent was given the result: %v", out)
	}
	out := bash("")
	ctx := feedback(out)
	for _, want := range []string{resultHead, "--- FAIL: TestRanking\nFAIL", "Fix what failed, commit, and verify again"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("result lacks %q:\n%s", want, ctx)
		}
	}
	if said(out) != resultTold {
		t.Errorf("told the human %q", said(out))
	}
	if !strings.Contains(f.eventLog(), `"jobs":["j1"],"kind":"results_delivered","ts":"`) || !strings.Contains(f.eventLog(), `"via":"tool"`) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if out := bash(""); out != nil {
		t.Errorf("given twice: %v", out)
	}
}

// A result waits while the model owes the human a question: that comes first.
func TestAResultWaitsForAQuestionOwed(t *testing.T) {
	f := newFixture(t, true)
	f.finished("P0", state.JobFailed)
	f.store.Update(func(st *state.State) error {
		_, err := state.AddProposal(st, state.Decision{What: "skip it", Question: "baton: x."}, f.now)
		return err
	})
	out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
	if feedback(out) != "" || len(f.state().Due()) != 1 {
		t.Fatalf("given ahead of the question: %v", out)
	}
}

// At a stop, the result is the answer: the stop is refused with it, once.
func TestAResultIsTheAnswerToAStop(t *testing.T) {
	f := newFixture(t, true)
	f.finished("P0", state.JobFailed)
	f.store.Update(func(st *state.State) error { st.Run.StopBlocks = 2; return nil })
	out := f.fire("Stop", map[string]any{})
	if out["decision"] != "block" || !strings.HasPrefix(out["reason"].(string), resultHead) || said(out) != resultTold {
		t.Fatalf("stop: %v", out)
	}
	if st := f.state(); st.Run.StopBlocks != 0 {
		t.Errorf("a result counted as a stop without status: %d", st.Run.StopBlocks)
	}
	out = f.fire("Stop", map[string]any{})
	if r, _ := out["reason"].(string); out["decision"] != "block" || strings.Contains(r, "Job j1") || !strings.Contains(r, "is not reported done") {
		t.Fatalf("the second stop: %v", out)
	}
}

// An idle session gets its result with the line the host types to wake it (or with the human's prompt).
func TestAResultComesWithThePrompt(t *testing.T) {
	f := newFixture(t, true)
	f.finished("P0", state.JobPassed)
	out := f.fire("UserPromptSubmit", map[string]any{"prompt": "[baton] Job j1 (P0) finished; its result is attached."})
	if ctx := additional(out); !strings.Contains(ctx, "[baton] Job j1 (P0) passed after 12m") || !strings.Contains(ctx, "It checked 4f2a9c1d2e3f") {
		t.Fatalf("result:\n%s", ctx)
	}
	if said(out) != "baton: job j1 (`make test`, P0) passed after 12m → told Claude" || !strings.Contains(f.eventLog(), `"via":"prompt"`) {
		t.Errorf("told the human %q; events:\n%s", said(out), f.eventLog())
	}
	if out := f.fire("UserPromptSubmit", map[string]any{"prompt": "and now?"}); additional(out) != "" {
		t.Errorf("given twice: %v", out)
	}
}

// What the host did on its own reaches the human with the next hook that answers the session, once.
func TestTheHostsAnnouncementsAreShown(t *testing.T) {
	f := newFixture(t, true)
	announce := func() {
		f.store.Update(func(st *state.State) error {
			st.Run.Announce = []string{"baton: P3 verified (job j4 passed) → P3 done"}
			return nil
		})
	}
	for _, c := range []struct {
		event string
		input map[string]any
	}{
		{"PostToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/x"}, "tool_response": map[string]any{}}},
		{"UserPromptSubmit", map[string]any{"prompt": "hi"}},
		{"Stop", map[string]any{}},
		{"SessionStart", map[string]any{"source": "resume"}},
	} {
		announce()
		if out := f.fire(c.event, c.input); !strings.HasPrefix(said(out), "baton: P3 verified (job j4 passed) → P3 done") {
			t.Errorf("%s said %q", c.event, said(out))
		}
		if st := f.state(); len(st.Run.Announce) != 0 {
			t.Errorf("%s left %q", c.event, st.Run.Announce)
		}
	}
}

// With no phase in hand, a stop is allowed: every phase left waits for a job.
func TestAStopWithNothingInHand(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error {
		st.Phases["P0"].Status, st.Current = state.PhaseVerifying, ""
		st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning}}
		return nil
	})
	out := f.fire("Stop", map[string]any{})
	if out["decision"] == "block" || said(out) != "baton: no phase can start until job j1 (`make test`, P0) finishes — nothing is needed from you: baton takes the next one up then" {
		t.Fatalf("stop: %v", out)
	}
}

// The verification that completed the plan reaches the model first, so it sums the run up; the human is
// told the plan is complete at the stop after that.
func TestAVerificationThatCompletesThePlan(t *testing.T) {
	f := newFixture(t, true)
	f.finished("P1", state.JobPassed)
	f.store.Update(func(st *state.State) error {
		st.Phases["P0"].Status, st.Phases["P1"].Status = state.PhaseDone, state.PhaseDone
		st.Mode, st.Current = state.ModeComplete, ""
		return nil
	})
	out := f.fire("Stop", map[string]any{})
	r, _ := out["reason"].(string)
	if out["decision"] != "block" || !strings.Contains(r, "[baton] Job j1 (P1) passed") || !strings.Contains(r, "the plan is complete. Summarize the results for the human") {
		t.Fatalf("stop: %v", out)
	}
	if f.state().Run.CompleteNotified {
		t.Fatal("the human was told before the model")
	}
	if out := f.fire("Stop", map[string]any{}); out["decision"] == "block" || !strings.Contains(said(out), "baton: plan complete") {
		t.Fatalf("the next stop: %v", out)
	}
}

// A phase reopened after its verification failed resumes with the result in its brief.
func TestAResumedPhaseGetsItsResultInTheBrief(t *testing.T) {
	f := newFixture(t, true)
	f.finished("P0", state.JobFailed)
	pl, _ := f.store.LoadPlan()
	f.store.Update(func(st *state.State) error {
		_, err := state.ParkPhase(st, pl, "P0", state.Park{Reason: state.ParkRed, Since: f.now, Job: "j1", Notes: "a.go done."})
		return err
	})
	f.fire("Stop", map[string]any{})
	f.store.Update(func(st *state.State) error { st.Run.Compaction.Status = state.CompactTyped; return nil })
	_, ctx := f.compacted("manual")
	if !strings.Contains(ctx, "The result of the job P0 was set aside for:\n\n"+resultHead) || !strings.Contains(ctx, "--- FAIL: TestRanking") {
		t.Fatalf("brief:\n%s", ctx)
	}
	if st := f.state(); st.Job("j1").Delivered.IsZero() || len(st.Due()) != 0 {
		t.Errorf("the result is still due: %+v", st.Job("j1"))
	}
	if !strings.Contains(f.eventLog(), `"via":"brief"`) {
		t.Errorf("events:\n%s", f.eventLog())
	}
}
