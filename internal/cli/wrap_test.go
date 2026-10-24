package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// humanTurn is a turn of the lead's session the human started: a prompt they typed, /baton wrap say.
func (s *session) humanTurn() {
	s.set(func(x *state.State) { x.Run.TurnOpen, x.Run.TurnBy, x.Run.TurnStarted = true, "human", s.now })
}

// wraps runs `baton wrap` in a turn the human started, and returns what it printed.
func (s *session) wraps(args ...string) string {
	s.t.Helper()
	s.humanTurn()
	return s.must("", append([]string{"wrap"}, args...)...)
}

// atWork marks the lead's worker on phase as at work in its agent.
func (s *session) atWork(phase string) {
	s.set(func(x *state.State) {
		w := x.WorkerOn(phase)
		w.Status, w.AgentID = state.WorkerWorking, "agent-"+phase
	})
}

// mail is the run's messages of kind not given yet.
func mail(st state.State, kind string) []state.Message {
	var out []state.Message
	for _, m := range st.Inbox {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// Only the human wraps the run up: a phase agent is refused, and so is a turn baton or a notification started,
// or no turn at all, --off too; a session no host is above is refused before anything is read.
func TestOnlyTheHumanWrapsTheRunUp(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		turn string // who started the lead's turn; "" for none open
		want string
		why  string
	}{
		{"a turn baton started", nil, "baton", decide.WrapTurnRefused("baton"), "not the human's turn"},
		{"a notification's turn", nil, "system", decide.WrapTurnRefused("system"), "not the human's turn"},
		{"no turn open", nil, "", decide.WrapTurnRefused(""), "not the human's turn"},
		{"--off in a turn baton started", []string{"--off"}, "baton", decide.WrapTurnRefused("baton"), "not the human's turn"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := attached(t)
			s.set(func(x *state.State) { x.Run.TurnOpen, x.Run.TurnBy = c.turn != "", c.turn })
			if errs := s.fails(append([]string{"wrap"}, c.args...)...); !strings.Contains(errs, c.want) {
				t.Errorf("refused with %q, want %q", errs, c.want)
			}
			if st := s.state(); st.Wrap != nil || len(st.Inbox) != 0 || !s.logged("wrap_refused", map[string]any{"why": c.why}) {
				t.Errorf("wrap %+v, inbox %+v; events:\n%s", st.Wrap, st.Inbox, s.events())
			}
		})
	}

	t.Run("a phase agent", func(t *testing.T) {
		w, lead := asWorker(t)
		lead.humanTurn()
		w.mustFail(decide.WorkerRefused("wrap", "P1"), "wrap")
		if lead.state().Wrap != nil || !w.logged("wrap_refused", map[string]any{"why": "phase agent"}) {
			t.Errorf("wrap %+v; the agent's events:\n%s", lead.state().Wrap, w.events())
		}
	})

	t.Run("not hosted", func(t *testing.T) {
		s := attached(t)
		s.humanTurn()
		delete(s.env, "BATON_HOST")
		s.mustFail(decide.WrapNotHosted, "wrap")
		if s.state().Wrap != nil {
			t.Error("wrapped from a session no host is above")
		}
	})
}

// While the run wraps up, a gate whose command already ran whole in its phase is refused, with or without
// --why, saying what ran whole and what to run instead, and so is --whole of a QA phase's red gate. Its first
// run, --failed, a QA phase's slim rerun and await are queued as ever, and so is a phase agent's first run. A
// phase agent's whole repeat is refused by the lead's wrap too.
func TestAWholeRepeatIsRefusedWhileTheRunWrapsUp(t *testing.T) {
	const notes = "--notes=the parser is fixed"
	refusedWith := "not queued — the human is wrapping the run up, so no gate runs whole a second time: job j1 ran `make e2e` whole for P0 (1m, ending at "
	for _, c := range []struct {
		name    string
		qa      bool
		args    []string
		refused string // "" for queued
		reruns  string // what a queued j2 reruns
	}{
		{"a whole repeat", false, []string{"verify", "make", "e2e", notes}, refusedWith, ""},
		{"a whole repeat with --why", false, []string{"verify", "--why", "the fix touches every page", "make", "e2e", notes}, refusedWith, ""},
		{"--whole of a QA phase's red gate", true, []string{"verify", "--whole", "--why", "the fix touches every page", "make", "e2e", notes},
			"not queued — the human is wrapping the run up, so no gate runs whole a second time: --whole would run `make e2e` whole again after job j1. Leave out --whole", ""},
		{"--whole with no --why", true, []string{"verify", "--whole", "make", "e2e", notes}, "--whole would run `make e2e` whole again after job j1", ""},
		{"--failed", false, []string{"verify", "--failed", notes}, "", "j1"},
		{"a QA phase's slim rerun", true, []string{"verify", "make", "e2e", notes}, "", "j1"},
		{"another gate's first run", false, []string{"verify", "make", "lint", notes}, "", ""},
		{"an await", false, []string{"await", "gh", "run", "watch", notes}, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := redStagedGate(t)
			if c.qa {
				s.qaPhase("P0")
			}
			s.wraps()
			code, out, errs := s.run("", c.args...)
			j := s.state().Job("j2")
			if c.refused != "" {
				if code == 0 || !strings.Contains(errs, c.refused) || !strings.Contains(errs, "only the human lifts it, with /baton wrap --off") || j != nil {
					t.Fatalf("exit %d, queued %+v, said:\n%s%s", code, j, out, errs)
				}
				if !s.logged("refused", map[string]any{"command": c.args[0], "why": "wrapping up"}) {
					t.Errorf("events:\n%s", s.events())
				}
				return
			}
			if code != 0 || j == nil || j.Reruns != c.reruns {
				t.Fatalf("exit %d, queued %+v, said:\n%s%s", code, j, out, errs)
			}
		})
	}

	t.Run("the refusal of a whole repeat offers the rerun of what failed", func(t *testing.T) {
		s, _, _ := redStagedGate(t)
		s.wraps()
		errs := s.fails("verify", "make", "e2e", notes)
		for _, want := range []string{"), and it failed.", "Rerun only what failed instead: `baton verify --failed --notes \"…\"`, which carries the stages that passed.",
			"Where what fails is not P0's to fix, `baton done P0 --despite-red` reports it done as it stands.", "--why does not change this"} {
			if !strings.Contains(errs, want) {
				t.Errorf("refused with:\n%s\nwant %q", errs, want)
			}
		}
	})

	t.Run("after a whole run that passed", func(t *testing.T) {
		s, _ := gitSession(t)
		s.must("", "verify", "make", "test", notes)
		s.ended("j1", state.JobPassed, 0)
		s.wraps()
		if errs := s.fails("verify", "make", "test", notes); !strings.Contains(errs, "and it passed. That is P0's evidence: report P0 done on it.") ||
			strings.Contains(errs, "--despite-red") {
			t.Errorf("refused with:\n%s", errs)
		}
	})

	t.Run("a phase agent's whole repeat, by the lead's wrap", func(t *testing.T) {
		w, lead := asWorker(t)
		w.set(func(x *state.State) {
			x.Jobs = []state.Job{{ID: "P1.j1", Phase: "P1", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1}}
		})
		if errs := w.fails("verify", "make", "test", notes); strings.Contains(errs, "wrapping") {
			t.Fatalf("refused before the run wraps up:\n%s", errs)
		}
		lead.wraps()
		if errs := w.fails("verify", "make", "test", notes); !strings.Contains(errs, "job P1.j1 ran `make test` whole for P1") {
			t.Errorf("refused with:\n%s", errs)
		}
	})
}

// Wrapping up stops a QA phase's confirmation run under way and settles the phase on the rerun that passed,
// a decision with its undo; one owed while its rerun runs is dropped, and the phase is done once the rerun
// passes; and a QA phase's done while the run wraps up owes none.
func TestWrappingUpSettlesConfirmations(t *testing.T) {
	code := map[string]string{"src/parse.go": "package src // fixed\n"}
	const decision = "stopped P0's confirmation run (job j3, `make e2e`) as the human wrapped the run up: P0 is done on its rerun j2, which passed"

	t.Run("under way", func(t *testing.T) {
		s, _ := qaRedGate(t, true, code)
		s.must("", "verify", "make", "e2e", "--notes", "TestA fixed")
		s.staged("j2", state.JobPassed, 0, passedRerun...)
		s.must("", "done", "P0", "--notes", "every red fixed")
		if j := s.state().Job("j3"); j == nil || !j.Requeue || j.Finished() {
			t.Fatalf("no confirmation under way: %+v", j)
		}
		out := s.wraps()
		if !strings.Contains(out, "- "+decision+" (decision d1; undo: reopen P0 and run `make e2e`)\n") {
			t.Errorf("wrap says:\n%s", out)
		}
		st := s.state()
		ps, j3 := st.Phases["P0"], st.Job("j3")
		if j3.Status != state.JobInterrupted || j3.Reason != decide.WrapReason || ps.Confirm != nil || !slices.Equal(ps.On, []string{"j2"}) {
			t.Fatalf("j3 %+v, P0 %+v", j3, ps)
		}
		if d := st.Decisions; len(d) != 1 || d[0].What != decision || d[0].Undo != "reopen P0 and run `make e2e`" || d[0].Phase != "P0" {
			t.Errorf("decisions %+v", d)
		}
		if !s.logged("wrap_started", nil) || !s.logged("job_stopped", map[string]any{"job": "j3", "by": "wrap"}) ||
			!s.logged("note", map[string]any{"id": "d1", "by": "wrap"}) {
			t.Errorf("events:\n%s", s.events())
		}
		if ev := wrapStarted(t, s); !slices.Equal(ev["stopped"], []string{"j3"}) || !slices.Equal(ev["settled"], []string{"P0"}) {
			t.Errorf("wrap_started %+v", ev)
		}
		s.settle()
		if ps := s.state().Phases["P0"]; ps.Status != state.PhaseDone {
			t.Errorf("P0 %+v", ps)
		}
	})

	t.Run("owed", func(t *testing.T) {
		s, _ := qaRedGate(t, true, code)
		s.must("", "verify", "make", "e2e", "--notes", "TestA fixed")
		s.must("", "done", "P0", "--notes", "every red fixed")
		if c := s.state().Phases["P0"].Confirm; c == nil || c.After != "j2" {
			t.Fatalf("no confirmation owed: %+v", c)
		}
		out := s.wraps()
		const dropped = "dropped the full run of `make e2e` owed to confirm P0's reruns, as the human wrapped the run up: P0 is done once its rerun j2 passes"
		if !strings.Contains(out, "- "+dropped+" (decision d1; undo: reopen P0 and run `make e2e`)\n") {
			t.Errorf("wrap says:\n%s", out)
		}
		if ev := wrapStarted(t, s); len(ev["stopped"]) != 0 || !slices.Equal(ev["settled"], []string{"P0"}) {
			t.Errorf("wrap_started %+v", ev)
		}
		s.staged("j2", state.JobPassed, 0, passedRerun...)
		got := s.settle()
		if st := s.state(); st.Phases["P0"].Status != state.PhaseDone || len(got.Confirms) != 0 || st.Job("j3") != nil {
			t.Errorf("settled %+v, P0 %+v, j3 %+v", got, st.Phases["P0"], st.Job("j3"))
		}
	})

	t.Run("a QA phase's done owes none", func(t *testing.T) {
		s, _ := qaRedGate(t, true, code)
		s.must("", "verify", "make", "e2e", "--notes", "TestA fixed")
		s.staged("j2", state.JobPassed, 0, passedRerun...)
		s.wraps()
		out := s.must("", "done", "P0", "--notes", "every red fixed")
		if !strings.Contains(out, "baton: "+decide.ConfirmWrapSkipped("P0", "j1")) || strings.Contains(out, "confirms it") {
			t.Errorf("done says:\n%s", out)
		}
		st := s.state()
		if st.Job("j3") != nil || st.Phases["P0"].Status != state.PhaseDone || st.Phases["P0"].Confirm != nil {
			t.Errorf("j3 %+v, P0 %+v", st.Job("j3"), st.Phases["P0"])
		}
		if !s.logged("qa_confirm_skipped", map[string]any{"phase": "P0", "why": "wrapping up"}) {
			t.Errorf("events:\n%s", s.events())
		}
	})
}

// wrapStarted is the lists of the session's latest wrap_started event, by field.
func wrapStarted(t *testing.T, s *session) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range strings.Split(s.events(), "\n") {
		ev := map[string]any{}
		if json.Unmarshal([]byte(line), &ev) != nil || ev["kind"] != "wrap_started" {
			continue
		}
		out = map[string][]string{}
		for _, k := range []string{"stopped", "settled", "told", "kept"} {
			list, _ := ev[k].([]any)
			out[k] = []string{}
			for _, x := range list {
				out[k] = append(out[k], x.(string))
			}
		}
	}
	return out
}

// Wrapping up stops a whole repeat under way, the lead's or an agent's, and leaves a phase's first run of its
// gate, a rerun and an await running; each live agent is told once, and the lead is mailed, and a second wrap
// tells nobody again.
func TestWrappingUpStopsWholeRepeatsAndTellsEachAgentOnce(t *testing.T) {
	w, lead := asWorker(t)
	lead.atWork("P1")
	lead.set(func(x *state.State) {
		x.Jobs = []state.Job{
			{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1},
			{ID: "j2", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobRunning},
			{ID: "j3", Phase: "P0", Kind: state.JobAwait, Command: "gh run watch", Status: state.JobRunning},
		}
	})
	w.set(func(x *state.State) {
		x.Jobs = []state.Job{
			{ID: "P1.j1", Phase: "P1", Kind: state.JobVerify, Command: "make lint", Status: state.JobFailed, Exit: 1},
			{ID: "P1.j2", Phase: "P1", Kind: state.JobVerify, Command: "make lint", Reruns: "P1.j1", Status: state.JobRunning},
			{ID: "P1.j3", Phase: "P1", Kind: state.JobVerify, Command: "make test", Status: state.JobRunning},
		}
	})
	out := lead.wraps()
	for _, want := range []string{"- stopped job j2, a whole rerun of `make test` for P0 (it ran whole as job j1)\n",
		"- kept job P1.j3, P1's first run of `make test`\n", "- told you and the agent on P1: finish on the evidence you have"} {
		if !strings.Contains(out, want) {
			t.Errorf("wrap says:\n%s\nwant %q", out, want)
		}
	}
	ls, ws := lead.state(), w.state()
	if j := ls.Job("j2"); j.Status != state.JobInterrupted || j.Reason != decide.WrapReason {
		t.Errorf("j2 %+v", j)
	}
	for _, j := range []*state.Job{ls.Job("j3"), ws.Job("P1.j2"), ws.Job("P1.j3")} {
		if j.Finished() {
			t.Errorf("stopped %+v", *j)
		}
	}
	if ls.Wrap == nil || ls.Wrap.By != state.WrapByHuman || !ls.Wrap.At.Equal(lead.now) || ws.Wrap != nil {
		t.Errorf("the lead's wrap %+v, the agent's %+v", ls.Wrap, ws.Wrap)
	}
	if m := mail(ls, state.MessageWrap); len(m) != 1 || m[0].Text != decide.WrapUp || m[0].Phase != "" {
		t.Errorf("the lead's mail %+v", m)
	}
	if m := mail(ws, state.MessageWrap); len(m) != 1 || m[0].Text != decide.WrapUp || m[0].Phase != "P1" {
		t.Errorf("the agent's mail %+v", m)
	}
	if ev := wrapStarted(t, lead); !slices.Equal(ev["stopped"], []string{"j2"}) || !slices.Equal(ev["told"], []string{"P1"}) || !slices.Equal(ev["kept"], []string{"P1.j3"}) {
		t.Errorf("wrap_started %+v", ev)
	}
	if !lead.logged("job_stopped", map[string]any{"job": "j2", "by": "wrap"}) || !strings.Contains(strings.Join(ls.Run.Announce, "\n"), "baton: wrapping the run up → no gate runs whole a second time; stopped j2; told the agent on P1") {
		t.Errorf("announced %q; events:\n%s", ls.Run.Announce, lead.events())
	}

	// Told once: given, and not told again by a second wrap.
	lead.set(func(x *state.State) { state.TakeMail(x) })
	w.set(func(x *state.State) { state.TakeMail(x) })
	out = lead.wraps()
	if !strings.Contains(out, "the run is wrapping up already") || strings.Contains(out, "told") {
		t.Errorf("wrap again says:\n%s", out)
	}
	if m, n := mail(lead.state(), state.MessageWrap), mail(w.state(), state.MessageWrap); len(m)+len(n) != 0 {
		t.Errorf("told again: %+v, %+v", m, n)
	}
	if ev := wrapStarted(t, lead); len(ev["told"]) != 0 || !lead.logged("wrap_started", map[string]any{"again": true}) {
		t.Errorf("wrap_started %+v", ev)
	}
}

// --off lifts the wrap: the message no agent took yet is dropped, a whole repeat is queued again, and the
// human is told. With no wrap, it says there is nothing to lift.
func TestWrapOff(t *testing.T) {
	s, _, _ := redStagedGate(t)
	if out := s.wraps("--off"); !strings.Contains(out, decide.WrapNotOn) {
		t.Errorf("--off with no wrap says:\n%s", out)
	}
	s.wraps()
	out := s.wraps("--off")
	if !strings.Contains(out, decide.WrapEnded) {
		t.Errorf("--off says:\n%s", out)
	}
	st := s.state()
	if st.Wrap != nil || len(mail(st, state.MessageWrap)) != 0 || !slices.Contains(st.Run.Announce, decide.WrapEnded) || !s.logged("wrap_ended", nil) {
		t.Errorf("wrap %+v, inbox %+v, announced %q; events:\n%s", st.Wrap, st.Inbox, st.Run.Announce, s.events())
	}
	s.must("", "verify", "make", "e2e", "--why", "a fix in every stage", "--notes", "the parser is fixed")
	if j := s.state().Job("j2"); j == nil || j.Reruns != "" {
		t.Errorf("j2 %+v", j)
	}

	t.Run("an agent's message not given yet", func(t *testing.T) {
		w, lead := asWorker(t)
		lead.atWork("P1")
		lead.wraps()
		lead.wraps("--off")
		if m := mail(w.state(), state.MessageWrap); len(m) != 0 {
			t.Errorf("the agent's mail %+v", m)
		}
	})
}
