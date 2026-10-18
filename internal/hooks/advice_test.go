package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

const advice = "Advisor (Haiku): The change's, it seems: consider `go test -run TestLogin ./auth`."

// A look-in held for the advisor reaches no hook; released with the advisor's note, it goes with the next
// one, the note after it, and the human and the log are told the note went with it. Each kind: a gate's
// first failure, a look-in the host left, a red gate's result its environment failed.
func TestALookInHeldForTheAdvisorGoesWithItsNoteOnceReleased(t *testing.T) {
	bash := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}}
	for _, c := range []struct {
		name, kind string
		set        func(f *fixture)
		head, line string
		logged     string
		fields     map[string]any
	}{
		{"a first failure", state.LookFailing, func(f *fixture) { f.failingGate("j1", "P0", false) }, failHead,
			"baton: job j1 (`make e2e`, P0) printed a failure 8m in, and runs on → told Claude, with the advisor's note",
			"look_in_told", map[string]any{"type": state.LookFailing, "job": "j1", "advised": true}},
		{"a look-in left", state.LookSlow, func(f *fixture) {
			f.set(func(st *state.State) {
				st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Status: state.JobRunning, Started: f.now.Add(-time.Hour)})
				state.AddLookIn(st, state.LookIn{Kind: state.LookSlow, Job: "j1", Text: "[baton] Job j1 runs long.", Say: "baton: job j1 runs long", At: f.now})
			})
		}, "[baton] Job j1 runs long.", "baton: job j1 runs long → told Claude, with the advisor's note",
			"look_in_told", map[string]any{"type": state.LookSlow, "job": "j1", "advised": true}},
		{"a red gate its environment failed", state.LookEnv, func(f *fixture) {
			log := filepath.Join(f.t.TempDir(), "j1.log")
			os.WriteFile(log, []byte("Bind for 0.0.0.0:5432 failed: port is already allocated\n"), 0o644)
			f.set(func(st *state.State) {
				st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Status: state.JobFailed, Exit: 1, Log: log,
					Started: f.now.Add(-time.Hour), Ended: f.now.Add(-time.Minute)})
			})
		}, "This looks like the gate's environment, not the change", "baton: job j1 (`make e2e`, P0) failed (exit 1) after 59m → told Claude, with the advisor's note",
			"results_delivered", map[string]any{"via": "tool"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := independent(t)
			c.set(f)
			f.set(func(st *state.State) {
				if !state.HoldForAdvice(st, c.kind, "j1", f.now.Add(time.Minute)) {
					t.Fatalf("not held: %+v", st)
				}
			})
			if got := feedback(f.fire("PostToolUse", bash)); strings.Contains(got, c.head) {
				t.Fatalf("told while held:\n%s", got)
			}
			f.set(func(st *state.State) { state.ReleaseAdvice(st, c.kind, "j1", advice) })
			out := f.fire("PostToolUse", bash)
			got := feedback(out)
			if !strings.Contains(got, c.head) || !strings.Contains(got, "\n\n"+advice) || strings.Index(got, advice) < strings.Index(got, c.head) {
				t.Errorf("not told with the note after it:\n%s", got)
			}
			if !strings.Contains(said(out), c.line) {
				t.Errorf("told the human %q", said(out))
			}
			if !f.logged(c.logged, c.fields) || c.kind == state.LookEnv && !strings.Contains(f.eventLog(), `"advised":["j1"]`) {
				t.Errorf("not logged:\n%s", f.eventLog())
			}
			if st := f.state(); st.Advice != nil {
				t.Errorf("advice left once told: %+v", st.Advice)
			}
			if got := feedback(f.fire("PostToolUse", bash)); strings.Contains(got, advice) {
				t.Errorf("told twice:\n%s", got)
			}
		})
	}
}

// A hold past its deadline by state.AdviceGrace is released by the next hook, which tells the look-in
// without a note: a session never waits on a host that stopped looking.
func TestAHoldPastItsDeadlineGoesWithTheNextHook(t *testing.T) {
	f, _ := independent(t)
	f.failingGate("j1", "P0", false)
	f.set(func(st *state.State) { state.HoldForAdvice(st, state.LookFailing, "j1", f.now.Add(-state.AdviceGrace)) })
	out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
	if got := feedback(out); !strings.Contains(got, failHead) || strings.Contains(got, "Advisor (") {
		t.Errorf("told %q", got)
	}
	if strings.Contains(said(out), "advisor") || f.state().Advice != nil {
		t.Errorf("said %q, advice %+v", said(out), f.state().Advice)
	}
}

// A stop while a red result waits on the advisor goes through with no refusal, as one that waits on a job
// does: the host gives the result within its deadline, and wakes the session for it. So does a phase
// agent's hand-back.
func TestAStopWhileALookInWaitsOnTheAdvisorGoesThrough(t *testing.T) {
	f, _ := independent(t)
	f.set(func(st *state.State) {
		st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Status: state.JobFailed, Exit: 1,
			Started: f.now.Add(-time.Hour), Ended: f.now.Add(-time.Minute)})
		state.HoldForAdvice(st, state.LookEnv, "j1", f.now.Add(40*time.Second))
	})
	out := f.fire("Stop", map[string]any{})
	if out["decision"] == "block" {
		t.Fatalf("refused: %v", out)
	}
	if !strings.Contains(said(out), "within 40s, once the advisor has had its say") || !f.logged("idle_on_advisor", map[string]any{"phase": "P0", "held": float64(1)}) {
		t.Errorf("said %q\n%s", said(out), f.eventLog())
	}
	if st := f.state(); st.Run.StopBlocks != 0 || !st.Job("j1").Delivered.IsZero() {
		t.Errorf("stop blocks %d, job %+v", st.Run.StopBlocks, st.Job("j1"))
	}

	w, _, _ := newWorker(t)
	w.fire("SubagentStart", map[string]any{})
	w.set(func(st *state.State) {
		st.Jobs = append(st.Jobs, state.Job{ID: "P2.j1", Phase: "P2", Kind: state.JobVerify, Command: "make e2e", Status: state.JobFailed, Exit: 1,
			Started: w.now.Add(-time.Hour), Ended: w.now.Add(-time.Minute)})
		state.HoldForAdvice(st, state.LookEnv, "P2.j1", w.now.Add(40*time.Second))
	})
	hso, _ := w.fire("PreToolUse", handbackCall("toolu_1"))["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] == "deny" {
		t.Errorf("hand-back refused: %v", hso)
	}
}
