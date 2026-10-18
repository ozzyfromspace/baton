package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/risk"
	"github.com/ozzyfromspace/baton/internal/state"
)

// ledgerRig is a run whose risk ledger the test drives: P0, then P1 and P2 after it, then P3 (QA) after
// both, in a git checkout; the ledger's lines at 10 and 20 points.
type ledgerRig struct {
	t     *testing.T
	now   time.Time
	dir   string
	start string
	s     *state.Store
	c     *Coordinator
	pl    plan.Plan
}

func newLedgerRig(t *testing.T) *ledgerRig {
	gittest.Isolate(t)
	x := &ledgerRig{t: t, now: time.Now().Add(time.Hour).Truncate(time.Second), dir: t.TempDir()}
	gittest.Git(t, x.dir, "init", "-q", "-b", "main")
	gittest.Write(t, x.dir, ".gitignore", ".baton/\n")
	gittest.Commit(t, x.dir, "init")
	x.start = gittest.Git(t, x.dir, "rev-parse", "HEAD")
	p, err := state.OpenProject(filepath.Join(x.dir, ".baton"), func() time.Time { return x.now })
	if err != nil {
		t.Fatal(err)
	}
	if x.s, err = p.Bind(state.Binding{Session: "sess-1", Instance: "inst"}); err != nil {
		t.Fatal(err)
	}
	x.pl = plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", After: []string{}}, {ID: "P1", After: []string{"P0"}},
		{ID: "P2", After: []string{"P0"}}, {ID: "P3", After: []string{"P1", "P2"}, Type: plan.TypeQA}}}
	x.s.WritePlan(x.pl)
	x.s.Update(func(st *state.State) error {
		*st = state.Attach(x.pl, x.now, state.Origin{Head: x.start})
		st.RunBranch = "main"
		return nil
	})
	timing := DefaultTiming
	timing.EscalationTimeout = 10 * time.Minute
	x.c = &Coordinator{Lead: &Loop{Store: x.s, Notify: &bomb{}, Project: "demo", Timing: timing}, Project: p, Risk: risk.Lines{On: true, Review: 10, Full: 20}}
	return x
}

// work commits lines lines of code at path, with no test, and finishes the phase in hand with no check run:
// 5, 3, 4 and 1 points for 1000 lines, no test changed, no check and no deviation given.
func (x *ledgerRig) work(phase, path string, lines int) {
	x.t.Helper()
	gittest.Write(x.t, x.dir, path, strings.Repeat("x := 1\n", lines))
	gittest.Commit(x.t, x.dir, phase)
	head := gittest.Git(x.t, x.dir, "rev-parse", "HEAD")
	x.update(func(st *state.State) {
		if _, err := state.Done(st, x.pl, phase, x.now, state.DoneOpts{Force: true}); err != nil {
			x.t.Fatal(err)
		}
		if next := st.Current; next != "" {
			state.Start(st, x.now, state.Origin{Head: head})
		}
	})
}

func (x *ledgerRig) update(fn func(*state.State)) {
	x.t.Helper()
	if _, err := x.s.Update(func(st *state.State) error { fn(st); return nil }); err != nil {
		x.t.Fatal(err)
	}
}

func (x *ledgerRig) state() state.State {
	st, err := x.s.Load()
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

func (x *ledgerRig) step(at time.Time) {
	x.now = at
	x.c.riskStep(at, x.s)
}

func (x *ledgerRig) events() string {
	b, _ := os.ReadFile(filepath.Join(x.s.Dir, "events.jsonl"))
	return string(b)
}

// mail is what baton has for the lead's model about the ledger.
func (x *ledgerRig) mail() []string {
	var out []string
	for _, m := range x.state().Inbox {
		if m.Kind == state.MessageRisk {
			out = append(out, m.Text)
		}
	}
	return out
}

// A phase done is scored once from its commits and record; past a line, the lead is told the score and
// its factors as soon as it holds a phase, and nothing is held: a score alone never holds.
func TestLedgerScoresAndLooksIn(t *testing.T) {
	x := newLedgerRig(t)
	x.work("P0", "internal/a/a.go", 1000)
	x.step(x.now)
	st := x.state()
	if st.RiskTotal() != 13 || !st.Scored("P0") || len(st.Risk.Phases) != 1 || st.Risk.Phases[0].At.IsZero() {
		t.Fatalf("after P0: %+v", st.Risk)
	}
	if !hasEvent(x.events(), "risk_scored", map[string]any{"phase": "P0", "points": 13, "total": 13, "git": true}) {
		t.Errorf("events:\n%s", x.events())
	}
	if !hasEvent(x.events(), "risk_crossed", map[string]any{"line": state.RiskReview, "points": 10, "total": 13, "assessed": false}) {
		t.Errorf("no crossing of the review line:\n%s", x.events())
	}
	// P1 is in hand (state.Done took it up, and work started it): the look-in goes to its inbox.
	if mail := x.mail(); len(mail) != 1 || !strings.Contains(mail[0], "nothing is held") || !strings.Contains(mail[0], "P0, 13 points") {
		t.Errorf("the look-in: %q", mail)
	}
	if st := x.state(); st.RiskHeld() || st.Risk.Owed != "" || st.Risk.Crossed != state.RiskReview {
		t.Errorf("after the look-in: %+v", st.Risk)
	}
	if !strings.Contains(strings.Join(x.state().Run.Announce, "\n"), "past its review line (10)") {
		t.Errorf("the human is not told: %q", x.state().Run.Announce)
	}
	if _, why, held := (&Fleet{Max: 3}).next(x.state(), x.pl, x.now, room{}); held == state.HoldRisk {
		t.Errorf("a score alone held a new phase: %s", why)
	}

	// Scored once; the review line is not crossed again; the hard line is.
	x.step(x.now.Add(time.Minute))
	if n := strings.Count(x.events(), `"kind":"risk_scored"`); n != 1 {
		t.Errorf("scored %d times", n)
	}
	x.work("P1", "internal/b/b.go", 1000)
	x.step(x.now.Add(2 * time.Minute))
	if st := x.state(); st.RiskTotal() != 26 || st.Risk.Crossed != state.RiskFull {
		t.Errorf("after P1: %+v", st.Risk)
	}
	if !hasEvent(x.events(), "risk_crossed", map[string]any{"line": state.RiskFull, "points": 20, "total": 26}) {
		t.Errorf("no crossing of the hard line:\n%s", x.events())
	}
}

// A look-in waits for the lead to hold a phase: with none in hand, it could queue no check.
func TestLedgerLookInWaitsForAPhase(t *testing.T) {
	x := newLedgerRig(t)
	x.work("P0", "internal/a/a.go", 1000)
	x.update(func(st *state.State) { st.Current = "" })
	x.step(x.now)
	if mail := x.mail(); len(mail) != 0 || x.state().Risk.Owed == "" {
		t.Fatalf("told with no phase in hand: %q", mail)
	}
	x.update(func(st *state.State) { st.Current = "P1" })
	x.step(x.now.Add(riskEvery))
	if mail := x.mail(); len(mail) != 1 || x.state().Risk.Owed != "" || !hasEvent(x.events(), "risk_told", map[string]any{"what": "look-in"}) {
		t.Errorf("not told once in hand: %q", mail)
	}
}

// A passing full check starts the ledger again: a QA phase done, or a --qa gate of the lead's that passed;
// a QA phase finished over a red verification is scored like any other.
func TestLedgerResets(t *testing.T) {
	t.Run("a --qa gate", func(t *testing.T) {
		x := newLedgerRig(t)
		x.work("P0", "internal/a/a.go", 1000)
		x.step(x.now)
		queued := x.now.Add(time.Minute)
		x.update(func(st *state.State) {
			st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P1", Kind: state.JobVerify, QA: "risk", Status: state.JobPassed, Commit: "abc", Queued: queued, Ended: queued.Add(time.Hour)})
		})
		x.step(x.now.Add(2 * time.Hour))
		st := x.state()
		if st.RiskTotal() != 0 || st.Risk.Crossed != "" || st.Risk.Commit != "abc" || !st.Cleared("j1") {
			t.Errorf("after the gate: %+v", st.Risk)
		}
		if !hasEvent(x.events(), "risk_reset", map[string]any{"by": "job", "job": "j1", "total": 0}) {
			t.Errorf("events:\n%s", x.events())
		}
	})
	for _, red := range []bool{false, true} {
		t.Run(map[bool]string{false: "a QA phase done", true: "a QA phase done over red"}[red], func(t *testing.T) {
			x := newLedgerRig(t)
			x.work("P0", "internal/a/a.go", 1000)
			x.work("P1", "internal/b/b.go", 10)
			x.work("P2", "internal/c/c.go", 10)
			x.step(x.now)
			if x.state().Current != "P3" {
				t.Fatalf("P3 is not in hand: %+v", x.state().Current)
			}
			x.update(func(st *state.State) {
				if red {
					state.MarkDespiteRed(st, "P3")
				}
			})
			x.work("P3", "docs/qa.md", 5)
			x.step(x.now.Add(time.Minute))
			st := x.state()
			if reset := st.Cleared("qa:P3"); reset == red || st.Scored("P3") != true {
				t.Errorf("reset %v, scored %v: %+v", reset, st.Scored("P3"), st.Risk)
			}
			if !red && (st.RiskTotal() != 0 || !hasEvent(x.events(), "risk_reset", map[string]any{"by": "qa", "phase": "P3"})) {
				t.Errorf("after QA: %+v\n%s", st.Risk, x.events())
			}
		})
	}
}

// Only a "full now" verdict holds new phases: no new phase agent starts, and the lead is told why once it
// holds a phase. The hold lifts when the lead queues the full check, or notes why not once told, or at
// escalation_timeout; each is logged and announced.
func TestLedgerHoldAndItsLifts(t *testing.T) {
	for _, c := range []struct {
		name string
		lift func(x *ledgerRig, told time.Time) time.Time // what the lead does; when the next step runs
		by   string
	}{
		{"queued", func(x *ledgerRig, told time.Time) time.Time {
			x.update(func(st *state.State) {
				st.Jobs = append(st.Jobs, state.Job{ID: "j4", Phase: "P1", Kind: state.JobVerify, QA: "the risk ledger asked", Status: state.JobQueued, Queued: told.Add(time.Minute)})
			})
			return told.Add(time.Minute)
		}, decide.RiskByVerify},
		{"noted", func(x *ledgerRig, told time.Time) time.Time {
			x.update(func(st *state.State) {
				st.Decisions = append(st.Decisions, state.Decision{ID: "d1", Kind: state.Note, Phase: "P1", What: "P3 runs the full suite next", At: told.Add(time.Minute)})
			})
			return told.Add(time.Minute)
		}, decide.RiskByNote},
		{"deadline", func(x *ledgerRig, told time.Time) time.Time { return told.Add(10 * time.Minute) }, decide.RiskByDeadline},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newLedgerRig(t)
			x.work("P0", "internal/a/a.go", 1000)
			x.step(x.now)
			// A note made before the hold, or before the lead was told of it, lifts nothing.
			x.update(func(st *state.State) {
				st.Decisions = append(st.Decisions, state.Decision{ID: "d0", Kind: state.Note, Phase: "P1", What: "earlier", At: x.now})
			})
			x.c.riskVerdict(x.now, x.s, state.VerdictFull)
			if !x.state().RiskHeld() || !hasEvent(x.events(), "risk_held", map[string]any{"verdict": "full", "total": 13}) {
				t.Fatalf("no hold: %+v\n%s", x.state().Risk, x.events())
			}
			if phase, why, held := (&Fleet{Max: 3}).next(x.state(), x.pl, x.now, room{}); phase != "P2" || held != state.HoldRisk || why != decide.RiskHoldsAgents {
				t.Errorf("the start gate: %q, %q, %q", phase, why, held)
			}
			told := x.now.Add(riskEvery)
			x.step(told)
			mail := x.mail()
			if len(mail) != 2 || !strings.Contains(mail[1], "New phases are held for a full check") || !strings.Contains(mail[1], "the landings of") && !strings.Contains(mail[1], "every landing") {
				t.Fatalf("the lead's message: %q", mail)
			}
			if !x.state().RiskHeld() || x.state().Risk.Told.IsZero() || !hasEvent(x.events(), "risk_told", map[string]any{"what": "hold", "phase": "P1"}) {
				t.Fatalf("not told, or lifted early: %+v", x.state().Risk)
			}
			x.step(told.Add(time.Second)) // nothing yet: told once
			if n := strings.Count(x.events(), `"what":"hold"`); n != 1 || !x.state().RiskHeld() {
				t.Fatalf("told %d times, held %v", n, x.state().RiskHeld())
			}

			x.step(c.lift(x, told))
			st := x.state()
			if st.RiskHeld() || !hasEvent(x.events(), "risk_released", map[string]any{"by": c.by}) {
				t.Errorf("not lifted by %s: %+v\n%s", c.by, st.Risk, x.events())
			}
			if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "new phases may start again") {
				t.Errorf("the human is not told: %q", st.Run.Announce)
			}
			if _, _, held := (&Fleet{Max: 3}).next(st, x.pl, x.now, room{}); held == state.HoldRisk {
				t.Error("the start gate still holds")
			}
		})
	}
}

// A hold's message not given yet goes with the hold.
func TestLedgerHoldLiftedBeforeItIsTold(t *testing.T) {
	x := newLedgerRig(t)
	x.work("P0", "internal/a/a.go", 1000)
	x.update(func(st *state.State) { st.Current = "" })
	x.step(x.now)
	x.c.riskVerdict(x.now, x.s, state.VerdictFull)
	x.step(x.now.Add(riskEvery))
	if !x.state().Risk.Told.IsZero() {
		t.Fatal("told with no phase in hand")
	}
	x.step(x.now.Add(10 * time.Minute))
	if st := x.state(); st.RiskHeld() || !hasEvent(x.events(), "risk_released", map[string]any{"by": decide.RiskByDeadline}) {
		t.Errorf("the deadline lifted nothing: %+v", st.Risk)
	}
	for _, m := range x.state().Inbox {
		if strings.Contains(m.Text, "New phases are held") {
			t.Errorf("the hold's message stayed: %q", m.Text)
		}
	}
}

// With the ledger off, nothing is scored.
func TestLedgerOff(t *testing.T) {
	x := newLedgerRig(t)
	x.c.Risk.On = false
	x.work("P0", "internal/a/a.go", 1000)
	x.c.riskLedger(host.View{Now: x.now, Store: x.s, Owner: true}, x.s, x.state())
	x.c.fleetWG.Wait()
	if st := x.state(); st.Risk != nil {
		t.Errorf("an off ledger scored: %+v", st.Risk)
	}
}
