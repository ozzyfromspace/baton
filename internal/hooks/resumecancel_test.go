package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// armed is the fixture's run, owned by its host, with the usage limit's resume timer armed an hour on.
func (f *fixture) armed() {
	f.t.Helper()
	f.store.Update(func(st *state.State) error {
		if err := state.Claim(st, f.inst, 1, f.now); err != nil {
			return err
		}
		state.Limit(st, "lead", f.now, f.now.Add(time.Hour))
		state.ArmResume(st, f.now.Add(time.Hour), 0)
		return nil
	})
}

// The human's prompt cancels the armed timer and pauses the run, told beside it, once; a turn of baton's
// cancels nothing.
func TestAPromptCancelsTheResumeTimer(t *testing.T) {
	f := newFixture(t, true)
	f.armed()
	if out := f.fire("UserPromptSubmit", map[string]any{"prompt": "[baton] Continue P0."}); out != nil && strings.Contains(fmtOut(out), "resume") {
		t.Errorf("baton's own prompt said %v", out)
	}
	if u := f.state().Usage; u.ResumeAt.IsZero() {
		t.Fatalf("baton's prompt cancelled it: %+v", u)
	}
	out := f.fire("UserPromptSubmit", map[string]any{"prompt": "what is left?"})
	st := f.state()
	if st.Mode != state.ModePaused || st.Usage.Cancelled != state.CancelPrompt || !st.Usage.ResumeAt.IsZero() || st.Usage.CancelSaid.IsZero() {
		t.Fatalf("mode %s, usage %+v", st.Mode, st.Usage)
	}
	if say, _ := out["systemMessage"].(string); !strings.Contains(say, "baton will not resume by itself; /baton run resumes") {
		t.Errorf("the human was told %q", say)
	}
	if !strings.Contains(fmtOut(out), "baton paused the run and will not resume it by itself") {
		t.Errorf("the model was told %v", out)
	}
	again := f.fire("UserPromptSubmit", map[string]any{"prompt": "and then?"})
	if strings.Contains(fmtOut(again), "will not resume by itself") || strings.Count(f.eventLog(), `"kind":"usage_wait_cancelled"`) != 1 {
		t.Errorf("told again: %v\n%s", again, f.eventLog())
	}
}

// The prompt's new path fails open: a panic in it leaves the prompt as it was, and the timer armed.
func TestTheResumeCancelFailsOpen(t *testing.T) {
	f := newFixture(t, true)
	f.armed()
	defer func() { resumeCancelling = func() {} }()
	resumeCancelling = func() { panic("boom") }
	f.fire("UserPromptSubmit", map[string]any{"prompt": "what is left?"})
	if st := f.state(); st.Mode != state.ModeRunning || st.Usage.ResumeAt.IsZero() || !st.Run.TurnOpen {
		t.Errorf("mode %s, usage %+v, turn open %v", st.Mode, st.Usage, st.Run.TurnOpen)
	}
}

// A /baton command that only looks at the run, gives it back, or wraps it up leaves the timer armed.
func TestKeepsResume(t *testing.T) {
	for prompt, want := range map[string]bool{
		"/baton status": true, "/baton board": true, "/baton-board": true, "/baton": true, "/baton run": true, "/baton wrap": true, "/baton wrap --off": true,
		"/baton replan 'later'": false, "/baton pause": false, "how is it going?": false, "": false, "@P3 hi": false,
	} {
		if got := keepsResume(prompt); got != want {
			t.Errorf("%q: %v", prompt, got)
		}
	}
}

func fmtOut(out map[string]any) string {
	var b strings.Builder
	for k, v := range out {
		b.WriteString(k)
		b.WriteString("=")
		if m, ok := v.(map[string]any); ok {
			b.WriteString(fmtOut(m))
		} else if s, ok := v.(string); ok {
			b.WriteString(s)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// In the weekly wind-down, the human's prompt stops the run going on by itself after the reset as it
// cancels the resume timer: the run pauses, told beside it, once.
func TestAPromptPausesTheWindDown(t *testing.T) {
	f := newFixture(t, true)
	resets := f.now.Add(30 * time.Hour)
	f.store.Update(func(st *state.State) error {
		if err := state.Claim(st, f.inst, 1, f.now); err != nil {
			return err
		}
		st.Usage = &state.Usage{WeeklyPct: 98.5, WeeklyResets: resets}
		state.WindDown(st, 2, f.now)
		return nil
	})
	out := f.fire("UserPromptSubmit", map[string]any{"prompt": "what is left?"})
	st := f.state()
	if st.Mode != state.ModePaused || !st.WoundDown() || st.Usage.WindPaused.IsZero() || st.Usage.Cancelled != "" {
		t.Fatalf("mode %s, usage %+v", st.Mode, st.Usage)
	}
	if say, _ := out["systemMessage"].(string); !strings.Contains(say, "going on after the weekly reset cancelled") {
		t.Errorf("the human was told %q", say)
	}
	if !strings.Contains(fmtOut(out), "will not go on by itself after the weekly reset") {
		t.Errorf("the model was told %v", out)
	}
	f.fire("UserPromptSubmit", map[string]any{"prompt": "and then?"})
	if n := strings.Count(f.eventLog(), `"wind_down":true`); n != 1 {
		t.Errorf("told %d times:\n%s", n, f.eventLog())
	}
}
