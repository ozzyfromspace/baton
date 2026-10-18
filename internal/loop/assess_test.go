package loop

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// assessRig is a ledger rig whose crossing is assessed by opus, the model stubbed: answer is what it says,
// and every call it got is kept.
type assessRig struct {
	*ledgerRig
	mu    sync.Mutex
	asked []Completion
	ctxs  []context.Context
}

func newAssessRig(t *testing.T, answer func(ctx context.Context) (Answer, error)) *assessRig {
	x := &assessRig{ledgerRig: newLedgerRig(t)}
	x.c.Risk.Model = "opus"
	x.c.rsk.call = func(ctx context.Context, s *state.Store, c Completion) (Answer, error) {
		x.mu.Lock()
		x.asked, x.ctxs = append(x.asked, c), append(x.ctxs, ctx)
		x.mu.Unlock()
		return answer(ctx)
	}
	return x
}

// cross scores P0 past the review line, which hands over to the assessment, and waits for its call.
func (x *assessRig) cross() {
	x.t.Helper()
	x.work("P0", "internal/a/a.go", 1000)
	x.s.AppendHandoff("P0", "The parser is done; the lexer keeps its tokens.")
	x.step(x.now)
	x.c.rsk.calls.Wait()
}

func says(text string) func(context.Context) (Answer, error) {
	return func(context.Context) (Answer, error) { return Answer{Text: text, Took: 3 * time.Second}, nil }
}

// Each verdict reaches the lead as the assessment's own words: continue and targeted as a look-in, which
// holds nothing, and full as the ledger's hold, which quotes it.
func TestAssessmentVerdicts(t *testing.T) {
	for _, c := range []struct {
		name, answer, verdict string
		held                  bool
		mail, say             []string
	}{
		{name: "continue", answer: "continue. P0 is one file, and it ran its tests.", verdict: decide.RiskContinue,
			mail: []string{"Risk assessment (Opus)", "(10): continue. P0 is one file, and it ran its tests.", "nothing is held: carry on"},
			say:  []string{"an independent assessment (Opus) is asked", "the risk assessment (Opus) says continue"}},
		{name: "targeted", answer: "Targeted: run go test -run TestLexer ./internal/a before P1 builds on it.", verdict: decide.RiskTargeted,
			mail: []string{"(10): targeted. run go test -run TestLexer ./internal/a before P1 builds on it.", "suggests those checks"},
			say:  []string{"the risk assessment (Opus) suggests targeted checks"}},
		{name: "full", answer: "full. 1000 lines with no test, and nothing checked them.", verdict: state.VerdictFull, held: true,
			mail: []string{"New phases are held for a full check: the risk assessment (Opus) says a full check is due now",
				"Risk assessment (Opus): full: 1000 lines with no test, and nothing checked them."},
			say: []string{"the assessment says a full check now → new phases are held"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newAssessRig(t, says(c.answer))
			x.cross()
			if !hasEvent(x.events(), "risk_crossed", map[string]any{"line": state.RiskReview, "assessed": true}) ||
				!hasEvent(x.events(), "risk_assessed", map[string]any{"verdict": c.verdict, "line": state.RiskReview, "model": "opus", "seconds": 3}) {
				t.Errorf("events:\n%s", x.events())
			}
			st := x.state()
			if st.Risk.Verdict != c.verdict || st.Risk.Asking != (time.Time{}) || st.Risk.Assessor != "opus" || st.RiskHeld() != c.held {
				t.Errorf("the ledger: %+v", st.Risk)
			}
			x.step(x.now.Add(riskEvery)) // the look-in, or the hold, goes once the lead holds a phase (P1 is in hand)
			mail := x.mail()
			if len(mail) != 1 {
				t.Fatalf("told %d times: %q", len(mail), mail)
			}
			for _, w := range c.mail {
				if !strings.Contains(mail[0], w) {
					t.Errorf("the lead's message lacks %q:\n%s", w, mail[0])
				}
			}
			if strings.Contains(mail[0], "Risk look-in") {
				t.Errorf("the score's look-in went too:\n%s", mail[0])
			}
			said := strings.Join(x.state().Run.Announce, "\n")
			for _, w := range c.say {
				if !strings.Contains(said, w) {
					t.Errorf("the human is not told %q:\n%s", w, said)
				}
			}
		})
	}
}

// Whatever stands in the assessment's way, the lead is told the score and its factors in its place: an
// error, an answer with no verdict, the deadline, a call that never returned, a host that is gone, no mod.
func TestAssessmentFallsBackToTheScore(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func(ctx context.Context) (Answer, error)
		setup  func(x *assessRig)
		after  func(x *assessRig) // what happens after the step that crossed
		why    string
	}{
		{name: "an API error", answer: func(context.Context) (Answer, error) {
			return Answer{}, &Unanswered{Why: "api-error", Detail: "api-error 529 overloaded"}
		}, why: "api-error"},
		{name: "no verdict", answer: says("I would keep going, mostly."), why: assessNoVerdict},
		{name: "the deadline", answer: func(ctx context.Context) (Answer, error) {
			<-ctx.Done()
			return Answer{}, &Unanswered{Why: TimedOut, Detail: ctx.Err().Error()}
		}, setup: func(x *assessRig) { x.c.Lead.Timing.RiskWait = 50 * time.Millisecond }, why: TimedOut},
		{name: "a host that is gone", answer: says("continue."), after: func(x *assessRig) {
			x.update(func(st *state.State) {
				st.Risk.Asking, st.Risk.Owed = x.now, decide.RiskLookIn(*st.Risk, state.RiskReview, 10)
			})
		}, why: assessRestart},
		{name: "a call that never returned", answer: says("continue."), after: func(x *assessRig) {
			x.update(func(st *state.State) {
				st.Risk.Asking, st.Risk.Owed = x.now, decide.RiskLookIn(*st.Risk, state.RiskReview, 10)
			})
			x.c.rsk.asking.Store(x.now.UnixNano())
			x.step(x.now.Add(RiskWait)) // within riskLapse past its deadline: still waited for
			if len(x.mail()) != 0 {
				x.t.Errorf("told before the lapse: %q", x.mail())
			}
			x.now = x.now.Add(RiskWait + riskLapse)
		}, why: TimedOut},
		{name: "no mod", setup: func(x *assessRig) { x.c.rsk.call = nil }, why: NoMod},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newAssessRig(t, c.answer)
			if c.setup != nil {
				c.setup(x)
			}
			x.cross()
			if c.after != nil {
				c.after(x)
			}
			x.step(x.now.Add(riskEvery))
			mail := x.mail()
			if len(mail) != 1 || !strings.Contains(mail[0], "Risk look-in") || !strings.Contains(mail[0], "P0, 13 points") {
				t.Fatalf("the score's look-in: %q", mail)
			}
			if !hasEvent(x.events(), "risk_assess_skipped", map[string]any{"why": c.why}) || hasEvent(x.events(), "risk_held", nil) {
				t.Errorf("events:\n%s", x.events())
			}
			if st := x.state(); st.RiskHeld() || st.Risk.Asking != (time.Time{}) || st.Risk.Owed != "" {
				t.Errorf("the ledger: %+v", st.Risk)
			}
			if said := strings.Join(x.state().Run.Announce, "\n"); !strings.Contains(said, "→ the lead is told the score and its factors") {
				t.Errorf("the human is not told: %s", said)
			}
		})
	}
}

// While the call runs, the score's look-in is held from the lead, and no step waits on it; an answer that
// comes after a full check reset the ledger goes nowhere.
func TestAssessmentWhileItRuns(t *testing.T) {
	release := make(chan struct{})
	x := newAssessRig(t, func(context.Context) (Answer, error) {
		<-release
		return Answer{Text: "full. Nothing checked P0."}, nil
	})
	x.work("P0", "internal/a/a.go", 1000)
	x.step(x.now)
	x.c.riskStep(x.now.Add(riskEvery), x.s) // the rig's clock stays still while the call reads it
	if st := x.state(); len(x.mail()) != 0 || st.Risk.Asking.IsZero() || st.Risk.Owed == "" {
		t.Errorf("not held while the call runs: %q, %+v", x.mail(), st.Risk)
	}
	x.update(func(st *state.State) { state.ResetRisk(st, "P9.j1", x.now, "abc", x.now.Add(time.Minute)) })
	close(release)
	x.c.rsk.calls.Wait()
	x.step(x.now.Add(2 * riskEvery))
	if st := x.state(); len(x.mail()) != 0 || st.RiskHeld() || !hasEvent(x.events(), "risk_assessed", map[string]any{"verdict": "full", "stale": true}) {
		t.Errorf("a stale answer held or told: %q, %+v\n%s", x.mail(), st.Risk, x.events())
	}
}

// The assessment is asked as the plan says: opus, at most 800 tokens, a 2-minute deadline, baton's system
// prompt, and an input of at most 30k characters with the phase's factors, notes and changes marked as data.
func TestAssessmentInput(t *testing.T) {
	x := newAssessRig(t, says("continue."))
	x.cross()
	if len(x.asked) != 1 {
		t.Fatalf("asked %d times", len(x.asked))
	}
	q := x.asked[0]
	if q.Model != "opus" || q.MaxTokens != 800 || q.System != decide.RiskAssessSystem || q.Timeout <= 0 || q.Timeout > RiskWait {
		t.Errorf("the call: %+v", q)
	}
	if d, ok := x.ctxs[0].Deadline(); !ok || time.Until(d) > RiskWait {
		t.Errorf("the deadline: %v %v", d, ok)
	}
	for _, w := range []string{"The plan: Demo", "13 points since the plan started", "The next full check: P3, after 2 more phases.",
		"## P0 (13 points:", "Its notes: The parser is done; the lexer keeps its tokens.", "(where the plan started)",
		"<data source=\"diffstat\">", "internal/a/a.go", "<data source=\"diff\">", "+x := 1"} {
		if !strings.Contains(q.Prompt, w) {
			t.Errorf("the input lacks %q:\n%s", w, q.Prompt)
		}
	}
	if len(q.Prompt) > decide.RiskInputMax {
		t.Errorf("%d characters", len(q.Prompt))
	}
	if !hasEvent(x.events(), "risk_assessed", map[string]any{"chars": len(q.Prompt)}) {
		t.Errorf("the input's size is not logged:\n%s", x.events())
	}
}

// With risk_model off, nothing is asked: the lead is told the score, as before there was an assessment.
func TestAssessmentOff(t *testing.T) {
	x := newAssessRig(t, says("full."))
	x.c.Risk.Model = ""
	x.cross()
	if len(x.asked) != 0 || !hasEvent(x.events(), "risk_assess_skipped", map[string]any{"why": assessOff}) ||
		!hasEvent(x.events(), "risk_crossed", map[string]any{"assessed": false}) {
		t.Errorf("asked %d, events:\n%s", len(x.asked), x.events())
	}
	if mail := x.mail(); len(mail) != 1 || !strings.Contains(mail[0], "Risk look-in") {
		t.Errorf("the score's look-in: %q", mail)
	}
}
