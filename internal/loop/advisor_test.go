package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// advRig is a running plan's run, the lead's, with the coordinator's advisor on and its model call stubbed:
// each call is put on asked, and answered by what reply is sent (or never, until the call's deadline).
type advRig struct {
	t     *testing.T
	now   time.Time
	s     *state.Store
	c     *Coordinator
	asked chan Completion
	reply chan advReply
}

type advReply struct {
	text string
	err  error
}

func newAdvRig(t *testing.T) *advRig {
	t.Helper()
	r := &advRig{t: t, now: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC), asked: make(chan Completion, 8), reply: make(chan advReply, 8)}
	s, err := state.Open(filepath.Join(t.TempDir(), ".baton"), "inst", func() time.Time { return r.now })
	if err != nil {
		t.Fatal(err)
	}
	r.s = s
	timing := DefaultTiming
	r.c = &Coordinator{Lead: &Loop{Store: s, Timing: timing, Logf: func(string, ...any) {}}, Advisor: Advisor{On: true}}
	r.c.adv.call = func(ctx context.Context, _ *state.Store, q Completion) (Answer, error) {
		r.asked <- q
		select {
		case a := <-r.reply:
			return Answer{Text: a.text, Took: 2 * time.Second}, a.err
		case <-ctx.Done():
			return Answer{}, &Unanswered{Why: TimedOut}
		}
	}
	s.Update(func(st *state.State) error {
		st.Mode, st.Current = state.ModeRunning, "P1"
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive}
		return nil
	})
	helloFrom(t, s)
	return r
}

// tick runs the advisor's part of the lead's tick, advance from the last.
func (r *advRig) tick(advance time.Duration) {
	r.now = r.now.Add(advance)
	st, err := r.s.Load()
	if err != nil {
		r.t.Fatal(err)
	}
	r.c.advise(r.now, r.s, st)
}

func (r *advRig) state() state.State {
	st, _ := r.s.Load()
	return st
}

// gate puts a running gate of P1 on record, j1, `make e2e`, with its log, started 9 minutes ago.
func (r *advRig) gate(log string, set func(j *state.Job)) {
	r.t.Helper()
	path := filepath.Join(r.t.TempDir(), "j1.log")
	os.WriteFile(path, []byte(log), 0o644)
	r.s.Update(func(st *state.State) error {
		j := state.Job{ID: "j1", Phase: "P1", Kind: state.JobVerify, Command: "make e2e", Status: state.JobRunning, Log: path,
			Notes: "Login rewritten; the e2e suite checks it.", Started: r.now.Add(-9 * time.Minute), Ran: r.now.Add(-9 * time.Minute)}
		if set != nil {
			set(&j)
		}
		st.Jobs = append(st.Jobs, j)
		return nil
	})
}

// failing records j1's first failure, a minute ago.
func (r *advRig) failing() {
	r.s.Update(func(st *state.State) error {
		st.Job("j1").Failing = &state.Failing{At: r.now.Add(-time.Minute), Line: "--- FAIL: TestLogin (0.20s)", Stage: "e2e"}
		return nil
	})
}

// call waits for the advisor's call.
func (r *advRig) call() Completion {
	r.t.Helper()
	select {
	case q := <-r.asked:
		return q
	case <-time.After(5 * time.Second):
		r.t.Fatal("the advisor was not asked")
	}
	return Completion{}
}

// answered waits until the advisor's call has returned and recorded what came of it.
func (r *advRig) answered() {
	r.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		r.c.adv.mu.Lock()
		done := true
		for _, a := range r.c.adv.asks {
			done = done && a.done
		}
		r.c.adv.mu.Unlock()
		if done {
			return
		}
	}
	r.t.Fatal("the advisor's call did not return")
}

// logged reports whether the run's events hold one of kind with fields.
func (r *advRig) logged(kind string, fields map[string]any) bool {
	return eventIn(readEvents(r.s), kind, fields)
}

func (r *advRig) count(kind string) int {
	n := 0
	for _, line := range strings.Split(readEvents(r.s), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["kind"] == kind {
			n++
		}
	}
	return n
}

func eventIn(events, kind string, fields map[string]any) bool {
	for _, line := range strings.Split(events, "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil || e["kind"] != kind {
			continue
		}
		match := true
		for k, v := range fields {
			if fmt.Sprint(e[k]) != fmt.Sprint(v) {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

// told is what the session is given now of j1: the look-ins and results due, and the advisor's notes on
// them, as its hooks take them.
func (r *advRig) told() (looks int, note string) {
	r.s.Update(func(st *state.State) error {
		failing, left := state.TakeLookIns(st, r.now)
		for _, kind := range []string{state.LookFailing, state.LookSlow} {
			note += state.TakeAdvice(st, kind, "j1")
		}
		due := state.TakeDue(st, r.now)
		note += state.TakeAdvice(st, state.LookEnv, "j1")
		looks = len(failing) + len(left) + len(due)
		return nil
	})
	return looks, note
}

// Each kind of look-in at a gate is held while the advisor is asked about it, once, with what baton has on
// record; its answer is released with it, as "Advisor (Haiku): …", and logged.
func TestTheAdvisorsNoteGoesWithTheLookIn(t *testing.T) {
	const port = "Bind for 0.0.0.0:5432 failed: port is already allocated"
	for _, c := range []struct {
		name, kind string
		set        func(r *advRig)
		facts      string
	}{
		{"a first failure", state.LookFailing, func(r *advRig) {
			r.gate("ok  pkg/a\n--- FAIL: TestLogin (0.20s)\n", nil)
			r.failing()
		}, "job j1 (P1) printed a failure 8m in, in its stage e2e, and runs on: --- FAIL: TestLogin (0.20s)"},
		{"a gate past its history", state.LookSlow, func(r *advRig) {
			r.gate("still going\n", nil)
			r.s.Update(func(st *state.State) error {
				state.AddLookIn(st, state.LookIn{Kind: state.LookSlow, Job: "j1", Text: "[baton] Job j1 runs long.", Say: "baton: job j1 (`make e2e`, P1) has run 9m, past its slowest earlier pass (4m)", At: r.now})
				return nil
			})
		}, "job j1 (`make e2e`, P1) has run 9m, past its slowest earlier pass (4m)"},
		{"a red gate its environment failed", state.LookEnv, func(r *advRig) {
			r.gate("compose up\n"+port+"\n", func(j *state.Job) { j.Status, j.Exit, j.Ended = state.JobFailed, 1, r.now })
		}, "job j1 (P1) failed (exit 1) after 9m; no test failed, and its log shows a port already in use: " + port},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAdvRig(t)
			r.s.Update(func(st *state.State) error {
				st.Jobs = append(st.Jobs, state.Job{ID: "j0", Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Status: state.JobPassed,
					Started: r.now.Add(-time.Hour), Ended: r.now.Add(-56 * time.Minute)})
				return nil
			})
			c.set(r)
			r.tick(time.Second)
			q := r.call()
			if !r.state().Advising(c.kind, "j1") {
				t.Fatalf("not held while asked: %+v", r.state().Advice)
			}
			if looks, _ := r.told(); looks != 0 {
				t.Fatalf("told while held")
			}
			if q.Model != "haiku" || q.System != decide.AdvisorSystem || q.MaxTokens != decide.AdvisorMaxTokens || q.Timeout != AdvisorWait {
				t.Errorf("call %+v", q)
			}
			for _, want := range []string{"What baton saw: " + c.facts, "`make e2e`, for phase P1", "Earlier passing runs of this command took 4m.",
				"Login rewritten; the e2e suite checks it."} {
				if !strings.Contains(q.Prompt, want) {
					t.Errorf("the prompt lacks %q:\n%s", want, q.Prompt)
				}
			}
			if len(q.Prompt) > decide.AdvisorMaxInput {
				t.Errorf("a prompt of %d characters", len(q.Prompt))
			}
			if !r.logged("advisor_asked", map[string]any{"type": c.kind, "job": "j1", "phase": "P1", "model": "haiku", "wait_seconds": 60}) {
				t.Errorf("not logged:\n%s", readEvents(r.s))
			}
			r.tick(5 * time.Second)
			if !r.state().Advising(c.kind, "j1") {
				t.Fatal("released before an answer or the deadline")
			}
			r.reply <- advReply{text: "**The environment's**, it seems.\nConsider rerunning TestLogin alone."}
			r.answered()
			r.tick(time.Second)
			const note = "Advisor (Haiku): The environment's, it seems. Consider rerunning TestLogin alone."
			if looks, got := r.told(); looks != 1 || got != note {
				t.Errorf("told %d, note %q", looks, got)
			}
			if !r.logged("advisor_said", map[string]any{"type": c.kind, "job": "j1", "chars": len(note), "seconds": 2}) {
				t.Errorf("not logged:\n%s", readEvents(r.s))
			}
			for range 3 {
				r.tick(time.Minute)
			}
			if r.count("advisor_asked") != 1 || len(r.asked) != 0 {
				t.Errorf("asked again:\n%s", readEvents(r.s))
			}
		})
	}
}

// The look-in goes at Timing.AdvisorWait whatever the advisor does: the host's own tick releases it with no
// note, and an answer after that is dropped and logged late.
func TestTheAdvisorsWaitIsBounded(t *testing.T) {
	r := newAdvRig(t)
	r.gate("--- FAIL: TestLogin\n", nil)
	r.failing()
	r.tick(time.Second)
	r.call()
	r.tick(AdvisorWait - time.Second)
	if !r.state().Advising(state.LookFailing, "j1") {
		t.Fatal("released before its deadline")
	}
	r.tick(time.Second)
	if looks, note := r.told(); looks != 1 || note != "" {
		t.Fatalf("at the deadline: told %d, note %q", looks, note)
	}
	if !r.logged("advisor_skipped", map[string]any{"type": state.LookFailing, "job": "j1", "why": TimedOut}) {
		t.Errorf("not logged:\n%s", readEvents(r.s))
	}
	r.reply <- advReply{text: "Too late."}
	r.answered()
	if !r.logged("advisor_late", map[string]any{"type": state.LookFailing, "job": "j1", "chars": len("Advisor (Haiku): Too late.")}) {
		t.Errorf("the late answer not logged:\n%s", readEvents(r.s))
	}
	r.tick(time.Second)
	if st := r.state(); st.Advice != nil || r.count("advisor_said") != 0 {
		t.Errorf("a late answer was given: %+v", st.Advice)
	}
}

// With no mod in the lead's claude, in a serial run, with the advisor off, on the model's own error or with
// no words, the look-in goes alone, and the log says why.
func TestALookInGoesAloneWhenTheAdvisorCannotSay(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   func(r *advRig)
		reply *advReply // the call's answer; nil: no call is made
		why   string    // advisor_skipped's; "" for none logged
	}{
		{"no mod", func(r *advRig) { os.RemoveAll(filepath.Join(r.s.Dir, "mod")) }, nil, NoMod},
		{"a serial run", func(r *advRig) {
			say(r.t, r.s, bridge.HelloInfo{Instance: "inst", Protocol: 1, Refused: "baton's mod is another version"})
		}, nil, SerialRun},
		{"the advisor off", func(r *advRig) { r.c.Advisor.On = false }, nil, ""},
		{"an API error", nil, &advReply{err: &Unanswered{Why: "api-error", Detail: "api-error 529 overloaded"}}, "api-error"},
		{"an old mod", nil, &advReply{err: fmt.Errorf("no such command")}, "failed"},
		{"no words", nil, &advReply{text: " \n"}, adviceEmpty},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAdvRig(t)
			if c.set != nil {
				c.set(r)
			}
			r.gate("--- FAIL: TestLogin\n", nil)
			r.failing()
			r.tick(time.Second)
			if c.reply != nil {
				r.call()
				r.reply <- *c.reply
				r.answered()
				r.tick(time.Second)
			}
			if looks, note := r.told(); looks != 1 || note != "" {
				t.Errorf("told %d, note %q", looks, note)
			}
			if c.reply == nil && (len(r.asked) != 0 || r.count("advisor_asked") != 0) {
				t.Errorf("asked:\n%s", readEvents(r.s))
			}
			if c.why == "" {
				if strings.Contains(readEvents(r.s), "advisor_") {
					t.Errorf("logged:\n%s", readEvents(r.s))
				}
				return
			}
			if !r.logged("advisor_skipped", map[string]any{"type": state.LookFailing, "job": "j1", "why": c.why}) {
				t.Errorf("not logged:\n%s", readEvents(r.s))
			}
			r.tick(time.Minute)
			if r.count("advisor_skipped") != 1 {
				t.Errorf("skipped twice:\n%s", readEvents(r.s))
			}
		})
	}
}

// A hold no call in memory stands for, an earlier host's, is released at once with no note, advisor off or
// on; and a call whose hold a hook released past its deadline is let go, its answer logged late.
func TestAHoldTheHostDoesNotKnowGoesAtOnce(t *testing.T) {
	r := newAdvRig(t)
	r.c.Advisor.On = false
	r.gate("--- FAIL: TestLogin\n", nil)
	r.failing()
	r.s.Update(func(st *state.State) error {
		state.HoldForAdvice(st, state.LookFailing, "j1", r.now.Add(time.Minute))
		return nil
	})
	r.tick(time.Second)
	if looks, note := r.told(); looks != 1 || note != "" {
		t.Errorf("an earlier host's hold: told %d, note %q", looks, note)
	}
	if !r.logged("advisor_skipped", map[string]any{"type": state.LookFailing, "job": "j1", "why": adviceRestart}) {
		t.Errorf("not logged:\n%s", readEvents(r.s))
	}

	r = newAdvRig(t)
	r.gate("--- FAIL: TestLogin\n", nil)
	r.failing()
	r.tick(time.Second)
	r.call()
	r.s.Update(func(st *state.State) error { // a hook, long past the deadline, the host's tick stalled
		state.LapseAdvice(st, r.now.Add(AdvisorWait+state.AdviceGrace))
		return nil
	})
	r.tick(time.Second)
	if !r.logged("advisor_skipped", map[string]any{"type": state.LookFailing, "job": "j1", "why": adviceLapsed}) {
		t.Errorf("not logged:\n%s", readEvents(r.s))
	}
	r.reply <- advReply{text: "Late."}
	r.answered()
	if !r.logged("advisor_late", map[string]any{"type": state.LookFailing, "job": "j1"}) {
		t.Errorf("the late answer not logged:\n%s", readEvents(r.s))
	}
}

// A phase agent's look-in is held in its sub-run, by its own loop, while the advisor is asked through the
// lead's bridge: nothing wakes the idle agent for it until the answer comes; then it is sent the line, and
// given the look-in with the note as it ends that turn.
func TestAnAgentsLookInGoesWithTheAdvisorsNote(t *testing.T) {
	x := newCrew(t, 1)
	x.c.Advisor = Advisor{On: true}
	answers := make(chan string, 1)
	vias := make(chan string, 1)
	x.c.adv.call = func(ctx context.Context, s *state.Store, q Completion) (Answer, error) {
		vias <- s.Dir
		select {
		case a := <-answers:
			return Answer{Text: a}, nil
		case <-ctx.Done():
			return Answer{}, &Unanswered{Why: TimedOut}
		}
	}
	x.atWork("P1")
	id := x.waiting("P1")
	x.tick(5 * time.Second)
	x.failing("P1", id)
	x.tick(5 * time.Second)
	if st, _ := x.sub("P1").Load(); !st.Advising(state.LookFailing, id) || len(x.sent) != 0 {
		t.Fatalf("not held: advice %+v, sent %+v", st.Advice, x.sent)
	}
	if via := <-vias; via != x.lead.Dir {
		t.Errorf("asked through %s, not the lead's run", via)
	}
	if !x.subLogged("P1", "advisor_asked", map[string]any{"type": state.LookFailing, "job": id, "phase": "P1"}) {
		t.Fatalf("not asked:\n%s", x.subEvents("P1"))
	}
	answers <- "Consider rerunning TestLogin alone."
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		x.c.adv.mu.Lock()
		done := true
		for _, a := range x.c.adv.asks {
			done = done && a.done
		}
		x.c.adv.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the call did not return")
		}
	}
	x.tick(time.Second)
	if len(x.sent) != 1 || !strings.Contains(x.sent[0].text, "[baton] A gate of yours is failing as it runs") {
		t.Fatalf("sent %+v", x.sent)
	}
	got := x.endTurn("P1")
	if !strings.Contains(got, "[baton] Job "+id+" (P1) is failing as it runs") || !strings.Contains(got, "\n\nAdvisor (Haiku): Consider rerunning TestLogin alone.") {
		t.Errorf("its stop gave it %q", got)
	}
	if !x.subLogged("P1", "advisor_said", map[string]any{"type": state.LookFailing, "job": id}) || !x.subLogged("P1", "look_in_told", map[string]any{"advised": true}) {
		t.Errorf("not logged:\n%s", x.subEvents("P1"))
	}
}

// The idle lead is not woken for a look-in held for the advisor, even in the tick that holds it; once the
// deadline passes with no answer, it is woken, and its prompt gives the look-in with no note.
func TestTheIdleLeadIsWokenOnceTheAdvisorHasHadItsSay(t *testing.T) {
	x := newCrew(t, 0)
	x.leadIdle = true
	x.c.Advisor = Advisor{On: true}
	asked := make(chan struct{}, 1)
	x.c.adv.call = func(ctx context.Context, _ *state.Store, _ Completion) (Answer, error) {
		asked <- struct{}{}
		<-ctx.Done() // the model never answers
		return Answer{}, &Unanswered{Why: TimedOut}
	}
	x.c.Lead.Timing.AdvisorWait = 300 * time.Millisecond
	x.lead.Update(func(st *state.State) error {
		q, _, _ := state.QueueJob(st, state.Job{Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Commit: "c1"}, x.now)
		j := st.Job(q.ID)
		j.Status, j.Started, j.PID = state.JobRunning, x.now, 4242
		st.Run.TurnOpen, st.Run.LastStop, st.Run.LastActivity = false, x.now, x.now
		return nil
	})
	x.tick(time.Second)
	x.lead.Update(func(st *state.State) error {
		st.Jobs[0].Failing = &state.Failing{At: x.now, Line: "--- FAIL: TestLogin (0.20s)"}
		return nil
	})
	x.tick(5 * time.Second)
	<-asked
	if x.logged("look_in_typed", nil) || !x.state().Advising(state.LookFailing, x.state().Jobs[0].ID) {
		t.Fatalf("typed for while held:\n%s", x.events())
	}
	x.tick(time.Second)
	if !x.logged("advisor_skipped", map[string]any{"type": state.LookFailing, "why": TimedOut}) || x.count("look_in_typed", map[string]any{"via": "mod"}) != 1 {
		t.Fatalf("not released and typed at the deadline:\n%s", x.events())
	}
	_, out := x.hook("UserPromptSubmit", map[string]any{"prompt": "[baton] A gate of yours is failing as it runs: baton's look-in, attached, says what it printed and what you can do."})
	if !strings.Contains(out, "is failing as it runs: `make e2e` printed this") || strings.Contains(out, "Advisor (") {
		t.Errorf("the prompt gave %s", out)
	}
}

// The advisor's call goes through the lead's bridge to baton's mod, whatever run the look-in is in, and its
// answer comes back on the mod's ack.
func TestTheAdvisorAsksThroughTheMod(t *testing.T) {
	s := completeStore(t)
	helloFrom(t, s)
	mod := runMod(t, s, func(bridge.Command) *bridge.Report {
		return &bridge.Report{OK: true, Result: "The change's: consider go test -run TestLogin ./auth.", Ms: 900}
	})
	c := &Coordinator{Lead: &Loop{Store: s, Timing: DefaultTiming, Logf: func(string, ...any) {}}, Advisor: Advisor{On: true, Model: "claude-haiku-4-5"}}
	now := s.Now()
	s.Update(func(st *state.State) error {
		st.Mode, st.Current = state.ModeRunning, "P1"
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive}
		st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Kind: state.JobVerify, Command: "make e2e", Status: state.JobRunning, Started: now,
			Failing: &state.Failing{At: now, Line: "--- FAIL: TestLogin"}}}
		return nil
	})
	st, _ := s.Load()
	c.advise(now, s, st)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		c.adv.mu.Lock()
		done := len(c.adv.asks) == 1 && c.adv.asks[adviceKey(s, state.LookFailing, "j1")].done
		c.adv.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no answer")
		}
	}
	st, _ = s.Load()
	c.advise(now.Add(time.Second), s, st)
	st, _ = s.Load()
	if len(st.Advice) != 1 || !st.Advice[0].Released || st.Advice[0].Text != "Advisor (Haiku): The change's: consider go test -run TestLogin ./auth." {
		t.Errorf("advice %+v", st.Advice)
	}
	if got := mod.commands(); len(got) != 1 || got[0].Model != "claude-haiku-4-5" || got[0].MaxTokens != decide.AdvisorMaxTokens ||
		got[0].TimeoutMs != AdvisorWait.Milliseconds() || !strings.Contains(got[0].Prompt, "--- FAIL: TestLogin") {
		t.Errorf("commands %+v", got)
	}
}
