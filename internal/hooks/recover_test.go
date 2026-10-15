package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A conversation its host resumed after claude exited by itself mid-plan wakes its model once, and only
// in the host started to bring it back (BATON_RECOVER): the turn under way was lost, and the phase in
// hand goes on. With no phase in hand there is nothing to go on with: the host drives the run.
func TestAResumedLeadIsWokenOnce(t *testing.T) {
	const why = "claude exited with code 137"
	for _, c := range []struct {
		name    string
		source  string
		vars    map[string]string
		recov   []state.Recovery
		set     func(*state.State, plan.Plan)
		wake    string // what the rewake says ("": none)
		resumed bool   // lead_resumed is logged, and the recovery told
	}{
		{name: "brought back", source: "resume", vars: map[string]string{"BATON_RECOVER": why},
			recov: []state.Recovery{{Why: why}}, resumed: true,
			wake: "[baton] claude exited with code 137 mid-plan, and baton resumed this conversation: the turn that was under way was lost. Continue P0: some of it may be done already"},
		{name: "a boundary owed", source: "resume", vars: map[string]string{"BATON_RECOVER": why},
			recov: []state.Recovery{{Why: why}}, resumed: true,
			set: func(st *state.State, pl plan.Plan) {
				state.Done(st, pl, "P0", st.Phases["P0"].StartedAt, state.DoneOpts{})
			}},
		{name: "not a host that brings it back", source: "resume", recov: []state.Recovery{{Why: why}}},
		{name: "told already", source: "resume", vars: map[string]string{"BATON_RECOVER": why}, recov: []state.Recovery{{Why: why, Told: true}}},
		{name: "a fresh session's", source: "resume", vars: map[string]string{"BATON_RECOVER": "fresh: " + why}, recov: []state.Recovery{{Why: why, Fresh: true}}},
		{name: "a session that started anew", source: "startup", vars: map[string]string{"BATON_RECOVER": why}, recov: []state.Recovery{{Why: why}}},
		{name: "an elevation as well", source: "resume", vars: map[string]string{"BATON_RECOVER": why, "BATON_PENDING": "run the tests"},
			recov: []state.Recovery{{Why: why, Told: true}}, wake: "[baton] This session is now hosted by baton (same conversation). Continue with: run the tests"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.vars = c.vars
			pl, _ := f.store.LoadPlan()
			f.set(func(st *state.State) {
				for _, r := range c.recov {
					r.At = f.now.Add(-time.Minute)
					st.Run.Recoveries = append(st.Run.Recoveries, r)
				}
				if c.set != nil {
					c.set(st, pl)
				}
			})
			code, stderr := f.fireCode("SessionStartRewake", map[string]any{"source": c.source})
			if c.wake == "" && (code != 0 || stderr != "") || c.wake != "" && (code != 2 || !strings.HasPrefix(stderr, c.wake)) {
				t.Errorf("the rewake: exit %d, %q; want %q", code, stderr, c.wake)
			}
			if c.wake != "" && c.resumed && !strings.Contains(stderr, "check what is (`git log`, `git status`, and the notes the phase left)") && !strings.Contains(stderr, "the files the phase is about") {
				t.Errorf("the rewake does not say how to check what is done: %q", stderr)
			}
			if got := f.logged("lead_resumed", map[string]any{"why": why}); got != c.resumed {
				t.Errorf("lead_resumed logged: %v, want %v\n%s", got, c.resumed, f.eventLog())
			}
			if rs := f.state().Run.Recoveries; c.resumed && !rs[len(rs)-1].Told {
				t.Error("the recovery is not marked told")
			}
			if code, stderr := f.fireCode("SessionStartRewake", map[string]any{"source": c.source}); c.vars["BATON_PENDING"] == "" && (code != 0 || stderr != "") {
				t.Errorf("woken again: exit %d, %q", code, stderr)
			}
			if n := strings.Count(f.eventLog(), `"kind":"lead_resumed"`); n > 1 {
				t.Errorf("lead_resumed logged %d times", n)
			}
		})
	}
}

// The fresh session a host starts when the conversation could not be resumed is linked to the run
// first: its SessionStart binds the run, gives the primer and the phase's brief, once, and says what
// happened.
func TestAFreshLeadGetsItsPhasesBrief(t *testing.T) {
	const why = "claude exited with code 137"
	line := "baton: claude exited with code 137 mid-plan, and the conversation could not be resumed → started a fresh session from P0's brief"
	for _, c := range []struct {
		name   string
		vars   map[string]string
		set    func(*state.State, plan.Plan)
		brief  bool
		fresh  bool // lead_fresh is logged
		primer string
	}{
		{name: "a phase in hand", vars: map[string]string{"BATON_RECOVER": "fresh: " + why}, brief: true, fresh: true, primer: "Current phase: P0 — First"},
		{name: "a boundary owed", vars: map[string]string{"BATON_RECOVER": "fresh: " + why}, fresh: true,
			set: func(st *state.State, pl plan.Plan) {
				state.Done(st, pl, "P0", st.Phases["P0"].StartedAt, state.DoneOpts{})
			},
			primer: "P1 has not started: baton compacts the context first"},
		{name: "not a host that brings it back", primer: "Current phase: P0 — First"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.vars = c.vars
			pl, _ := f.store.LoadPlan()
			f.set(func(st *state.State) {
				st.Run.Recoveries = append(st.Run.Recoveries, state.Recovery{At: f.now, Why: why, Fresh: true})
				st.Run.Announce = append(st.Run.Announce, line)
				if c.set != nil {
					c.set(st, pl)
				}
			})
			f.store.AppendHandoff("P0 (checkpoint)", "Half of a in abc123.")
			// The host that ran it lets go; the next one links the fresh session to the run, and binds it.
			p, _ := state.OpenProject(f.dir, f.clock)
			p.Unbind(f.inst)
			if err := p.Link("sess-2", f.store.Ref); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Bind(state.Binding{Session: "sess-2", Instance: "inst-2"}); err != nil {
				t.Fatal(err)
			}
			out := f.fireAs("inst-2", "SessionStart", map[string]any{"source": "startup", "session_id": "sess-2"})
			if say, _ := out["systemMessage"].(string); !strings.HasPrefix(say, line+"\nbaton: hosting · Demo") {
				t.Errorf("systemMessage = %q", say)
			}
			ctx := additionalContext(out)
			if !strings.HasPrefix(ctx, "This session is hosted by baton") || !strings.Contains(ctx, c.primer) {
				t.Errorf("the primer lacks %q:\n%s", c.primer, ctx)
			}
			briefed := f.logged("brief", map[string]any{"type": "fresh", "phase": "P0"})
			hasBrief := strings.Contains(ctx, "[baton] This conversation started without the phase's context") &&
				strings.Contains(ctx, "Now: continue P0 — First. Some of it may be done already") && strings.Contains(ctx, "Half of a in abc123.")
			if briefed != c.brief || hasBrief != c.brief {
				t.Errorf("briefed: logged %v, in the context %v; want %v\n%s", briefed, hasBrief, c.brief, ctx)
			}
			if got := f.logged("lead_fresh", map[string]any{"why": why, "session": "sess-2"}); got != c.fresh {
				t.Errorf("lead_fresh logged: %v, want %v\n%s", got, c.fresh, f.eventLog())
			}
			if st := f.state(); st.Run.SessionID != "sess-2" || st.Owner == nil || st.Owner.Instance != "inst-2" || st.Run.Recoveries[0].Told != c.fresh {
				t.Errorf("the run: session %q, owner %+v, recoveries %+v", st.Run.SessionID, st.Owner, st.Run.Recoveries)
			}
			if !c.fresh {
				return
			}
			again := f.fireAs("inst-2", "SessionStart", map[string]any{"source": "startup", "session_id": "sess-2"})
			if strings.Contains(additionalContext(again), "This conversation started without") || strings.Count(f.eventLog(), `"kind":"lead_fresh"`) != 1 {
				t.Errorf("briefed twice:\n%s", additionalContext(again))
			}
		})
	}
}

// Once claude runs again, the escalation that asked the human to resume it is moot.
func TestTheCrashEscalationClearsOnceClaudeRunsAgain(t *testing.T) {
	for _, c := range []struct {
		source string
		clears bool
	}{{"resume", true}, {"startup", true}, {"compact", false}, {"clear", false}} {
		t.Run(c.source, func(t *testing.T) {
			f := newFixture(t, true)
			f.set(func(st *state.State) {
				st.Run.Escalation = &state.Escalation{Kind: "crashed", Reason: "claude exited unexpectedly 3 times in 30 minutes", Since: f.now, Watchdog: true}
			})
			in := map[string]any{"source": c.source}
			if c.source == "clear" {
				in["session_id"] = "sess-2"
			}
			f.fire("SessionStart", in)
			if cleared := f.state().Run.Escalation == nil; cleared != c.clears {
				t.Errorf("cleared: %v, want %v", cleared, c.clears)
			}
			if got := f.logged("escalation_cleared", map[string]any{"type": "crashed"}); got != c.clears {
				t.Errorf("escalation_cleared logged: %v, want %v", got, c.clears)
			}
		})
	}
}

// A conversation resumed while its last turn stands refused is left asleep: nothing is typed into a refused
// conversation, and how it goes on is the human's. The recovery is told all the same, once.
func TestAQuietResumeLeavesTheModelAsleep(t *testing.T) {
	const why = "claude exited with code 137"
	f := newFixture(t, true)
	f.vars = map[string]string{"BATON_RECOVER": why}
	f.set(func(st *state.State) {
		st.Run.Recoveries = []state.Recovery{{At: f.now.Add(-time.Minute), Why: why, Quiet: true}}
	})
	if code, stderr := f.fireCode("SessionStartRewake", map[string]any{"source": "resume"}); code != 0 || stderr != "" {
		t.Fatalf("woken: exit %d, %q", code, stderr)
	}
	if !f.logged("lead_resumed", map[string]any{"why": why, "woken": false, "refused": true}) || !f.state().Run.Recoveries[0].Told {
		t.Fatalf("events:\n%s", f.eventLog())
	}
}

// StopFailure records what Claude Code said of the error, and logs whether a fresh session avoids it.
func TestStopFailureSaysWhetherAFreshSessionAvoidsIt(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     map[string]any
		afresh bool
	}{
		{"too long", map[string]any{"error": "invalid_request", "error_details": "prompt is too long: 213456 tokens > 200000 maximum", "last_assistant_message": "Prompt is too long"}, true},
		{"too long, said only on screen", map[string]any{"error": "invalid_request", "last_assistant_message": "Prompt is too long"}, true},
		{"another invalid request", map[string]any{"error": "invalid_request", "error_details": "bad request", "last_assistant_message": "API Error: 400 bad request"}, false},
		{"an expired login", map[string]any{"error": "authentication_failed", "last_assistant_message": "Please run /login"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.fire("StopFailure", c.in)
			e := f.state().Run.LastError
			if e == nil || e.Message != c.in["last_assistant_message"] || e.Afresh() != c.afresh {
				t.Fatalf("recorded %+v", e)
			}
			if got := f.logged("stop_failure", map[string]any{"error": c.in["error"], "afresh": true}); got != c.afresh {
				t.Fatalf("logged afresh %v, want %v:\n%s", got, c.afresh, f.eventLog())
			}
		})
	}
}

// The hook paths recovery changed fail open: a panic in one exits 0 and writes nothing.
func TestRecoveryPathsFailOpen(t *testing.T) {
	failsOpen(t, []failCase{
		{"a turn a fresh session avoids", "StopFailure", func(string) map[string]any {
			return map[string]any{"error": "invalid_request", "error_details": "prompt is too long: 213456 tokens > 200000 maximum", "last_assistant_message": "Prompt is too long"}
		}, nil},
		{"a quiet resume", "SessionStartRewake", func(string) map[string]any { return map[string]any{"source": "resume"} }, func(f *fixture) {
			f.vars = map[string]string{"BATON_RECOVER": "claude exited with code 137"}
			f.set(func(st *state.State) {
				st.Run.Recoveries = []state.Recovery{{At: f.now, Why: "claude exited with code 137", Quiet: true}}
			})
		}},
	})
}
