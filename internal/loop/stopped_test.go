package loop

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/board"
	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// stopAgent has the human stop the latest agent on phase (x on its row in the agent panel): its turn ends
// aborted, and the message baton then sends it is refused, as Claude Code refuses one to an agent the
// human stopped (spike 36). Its worker is held.
func (x *crew) stopAgent(phase string) {
	x.t.Helper()
	a := x.agent(phase)
	x.sendErr = `Agent "` + a.id + `" was stopped by the user and was not resumed. Treat its work as cancelled; only start a new agent for it if the user explicitly asks.`
	x.ended(phase, "aborted", "")
	x.tick(time.Second)
	x.tick(time.Second)
	x.sendErr = ""
	if w := x.worker(phase); w.Status != state.WorkerHeld {
		x.t.Fatalf("%s's worker after its agent was stopped: %+v\n%s", phase, w, x.events())
	}
}

// asked is the latest question the mod was told to ask, which must be about the agent on phase and read
// want.
func (x *crew) asked(phase, want string) bridge.Command {
	x.t.Helper()
	if len(x.asks) == 0 {
		x.t.Fatalf("the mod was told to ask nothing\n%s", x.events())
	}
	c := x.asks[len(x.asks)-1]
	if c.Phase != phase || !strings.Contains(c.Question, want) {
		x.t.Fatalf("asked %q about %s, want %q about %s", c.Question, c.Phase, want, phase)
	}
	return c
}

// stand is how phase stands on the run's board.
func (x *crew) stand(phase string) decide.Stand {
	x.t.Helper()
	pl, _ := x.lead.LoadPlan()
	b := board.Build(board.Input{State: x.state(), Plan: pl, HavePlan: true, Subs: x.proj.Workers(x.lead.Ref), Now: x.now})
	for _, s := range b.Steps {
		for _, p := range s.Phases {
			if p.ID == phase {
				return p.Stand
			}
		}
	}
	x.t.Fatalf("no %s on the board", phase)
	return decide.Stand{}
}

// stoppingCrew is a crew whose worker on P1 is at work, with the human pushed, and git with an identity
// (a landing makes commits).
func stoppingCrew(t *testing.T) (*crew, *pushes) {
	t.Helper()
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	x := newCrew(t, 1)
	p := &pushes{}
	x.c.Lead.Notify = p
	x.atWork("P1")
	return x, p
}

// spawnsOf counts the agents the host gave the mod to start on phase.
func (x *crew) spawnsOf(phase string) int {
	n := 0
	for _, p := range x.spawns() {
		if p == phase {
			n++
		}
	}
	return n
}

// lapse ticks past the deadline of a question about a stopped agent queued now, and a few seconds more,
// for its answer to be done.
func (x *crew) lapse() {
	x.wait(DefaultTiming.EscalationTimeout + time.Minute)
	for range 4 {
		x.tick(time.Second)
	}
}

// of lists what the human was pushed of kind.
func (p *pushes) of(kind string) []string {
	var out []string
	for i, k := range p.kinds {
		if k == kind {
			out = append(out, p.details[i])
		}
	}
	return out
}

// The human's first stop of a phase agent holds its phase and asks them, on the lead's screen, whether to
// restart it; their answer is done as they chose, and with no answer by the deadline baton decides from the
// record: an agent whose work is done is let go, its work landing without it, and one whose work is not is
// restarted, which a second question tells them.
func TestAFirstStopAsksTheHuman(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer string // "" for none: the deadline
		done   bool   // its work is done (verified) before the deadline
		status string // its worker's, after
		fresh  bool   // a fresh agent took the phase up
		event  string // what baton did, logged
		said   string // and told the human
	}{
		{"restart it", decide.RestartIt, false, state.WorkerWorking, true, "agent_restarted", decide.AgentRestarted("P1", true)},
		{"leave it stopped", decide.LeaveStopped, false, state.WorkerHeld, false, "worker_held", decide.AgentLeft("P1")},
		{"pause baton", "Pause baton", false, state.WorkerHeld, false, "paused", decide.RelayPaused("P1")},
		{"no answer, its work not done", "", false, state.WorkerWorking, true, "agent_restarted", decide.AgentRestarted("P1", false)},
		{"no answer, its work done", "", true, state.WorkerLanded, false, "worker_retired", decide.AgentReleased("P1")},
	} {
		t.Run(c.name, func(t *testing.T) {
			x, p := stoppingCrew(t)
			first := x.agent("P1")
			x.stopAgent("P1")
			w := x.worker("P1")
			if w.Stops != 1 || w.Stopped != first.id || !x.logged("agent_stopped", map[string]any{"phase": "P1", "stops": 1}) ||
				!x.logged("agent_restart_asked", map[string]any{"phase": "P1", "stops": 1}) || !strings.Contains(x.announced(), decide.AgentStopped("P1", 1)) {
				t.Fatalf("worker %+v\n%s", w, x.events())
			}
			due := state.Relay{Queued: x.worker("P1").StoppedAt}.Due(DefaultTiming.EscalationTimeout)
			q := x.asked("P1", "baton: you stopped the agent on P1 (one). Restart it? Unless you answer by "+due.Local().Format("15:04")+
				", baton restarts it, or lets it go if its work is done.")
			if !slices.Equal(q.Options, []string{decide.RestartIt, decide.LeaveStopped, "Pause baton"}) {
				t.Errorf("options %q", q.Options)
			}
			if s := x.stand("P1"); s.Kind != decide.StandNeedsYou || s.Why != decide.NeedsAgentAsked {
				t.Errorf("stand %+v", s)
			}
			if len(p.of("worker_held")) != 0 || len(p.of("agent_stopped")) != 0 {
				t.Errorf("pushed %q: the human stopped it themselves", p.kinds)
			}
			if c.done {
				x.finish("P1", map[string]string{"one.txt": "one\n"}, true)
			}
			if c.answer != "" {
				x.answers(c.answer, false, false)
				x.tick(time.Second)
			} else {
				x.lapse()
				if !x.logged("relay_answered", map[string]any{"phase": "P1", "type": state.RelayStopped, "by": state.ByTimeout}) {
					t.Errorf("not answered at its deadline\n%s", x.events())
				}
			}
			for range 4 {
				x.tick(time.Second)
			}
			if got := x.status("P1"); got != c.status {
				t.Fatalf("P1's worker is %s, want %s: %+v\n%s", got, c.status, x.worker("P1"), x.events())
			}
			if fresh := x.agent("P1"); (fresh.id != first.id) != c.fresh || c.fresh && fresh.tree != first.tree {
				t.Errorf("the agent on P1 %+v, the first %+v", fresh, first)
			}
			if !x.logged(c.event, map[string]any{"phase": "P1"}) || !strings.Contains(x.announced(), c.said) {
				t.Errorf("want %s, announced %q\n%s", c.event, c.said, x.events())
			}
			if w := x.worker("P1"); w.Stops != 1 {
				t.Errorf("stops %d after the first", w.Stops)
			}
			by := state.ByHuman
			if c.answer == "" {
				by = state.ByTimeout
			}
			if c.event != "paused" && c.event != "worker_held" && !x.logged(c.event, map[string]any{"by": by}) {
				t.Errorf("%s not by %s\n%s", c.event, by, x.events())
			}
			switch c.name {
			case "leave it stopped", "pause baton":
				x.wait(30 * time.Minute)
				if x.status("P1") != state.WorkerHeld || x.spawnsOf("P1") != 1 {
					t.Errorf("P1 did not wait for the human: %+v, spawns %v", x.worker("P1"), x.spawns())
				}
				if s := x.stand("P1"); s.Why != decide.NeedsAgentStopped {
					t.Errorf("stand %+v", s)
				}
				if c.name == "pause baton" && x.state().Mode != state.ModePaused {
					t.Errorf("mode %s", x.state().Mode)
				}
			case "no answer, its work not done":
				// The human is pushed, and asked whether to keep the fresh agent, which is told why it started.
				if got := p.of("agent_stopped"); len(got) != 1 || got[0] != decide.AgentRestartedNotice("P1") {
					t.Errorf("pushed %q", got)
				}
				x.asked("P1", "so baton restarted it: a fresh agent takes P1 up in its worktree. Keep it going?")
				if got := x.endTurn("P1") + x.briefs[x.agent("P1").id]; !strings.Contains(got, "The human stopped the agent that worked on P1 before you") {
					t.Errorf("the fresh agent was not told: %q", got)
				}
				x.lapse()
				if x.status("P1") != state.WorkerWorking || !x.logged("relay_applied", map[string]any{"type": state.RelayRestarted, "by": state.ByTimeout, "result": decide.StopKeep}) {
					t.Errorf("the restart's question at its deadline: %+v\n%s", x.worker("P1"), x.events())
				}
			case "no answer, its work done":
				if got := p.of("agent_stopped"); len(got) != 1 || got[0] != decide.AgentReleasedNotice("P1") {
					t.Errorf("pushed %q", got)
				}
				if !x.has("landed", `"phase":"P1"`) || x.state().Phases["P1"].Status != state.PhaseDone || x.spawnsOf("P1") != 1 {
					t.Errorf("P1's work did not land without an agent\n%s", x.events())
				}
			}
		})
	}
}

// secondStop brings the crew's P1 to its second stop: its first stop's question went unanswered, its work
// not done, so baton restarted it, and the human stopped the fresh agent too.
func secondStop(t *testing.T) (*crew, *pushes) {
	t.Helper()
	x, p := stoppingCrew(t)
	x.stopAgent("P1")
	x.lapse()
	x.atWork("P1")
	x.stopAgent("P1")
	if w := x.worker("P1"); w.Stops != 2 || !x.logged("agent_stopped", map[string]any{"phase": "P1", "stops": 2}) {
		t.Fatalf("worker %+v\n%s", w, x.events())
	}
	x.asked("P1", "baton: you stopped the agent on P1 (one) again. Restart it? Unless you answer by")
	return x, p
}

// A second stop nobody answers takes the agent down. With its work not done, a question with no deadline
// asks the human what to do with the phase: only it, and the phases that need it, wait, and the rest of
// the run goes on; its dialog steps aside unanswered, to come back when the human next writes, and their
// Restart it counts the stops afresh, so that a third stop is a first again. With its work done, the agent
// is let go as at a first stop.
func TestASecondStopNobodyAnswers(t *testing.T) {
	t.Run("its work not done", func(t *testing.T) {
		x, p := secondStop(t)
		spawns := x.spawnsOf("P1")
		x.lapse()
		w := x.worker("P1")
		if w.Status != state.WorkerHeld || w.TakenDown.IsZero() || !x.logged("agent_taken_down", map[string]any{"phase": "P1", "stops": 2, "by": state.ByTimeout}) ||
			!strings.Contains(x.announced(), decide.AgentTakenDown("P1", DefaultTiming.EscalationTimeout)) {
			t.Fatalf("worker %+v\n%s", w, x.events())
		}
		if got := p.of("worker_held"); len(got) != 1 || got[0] != decide.AgentTakenDownNotice("P1") {
			t.Errorf("pushed %q", got)
		}
		q := x.asked("P1", "baton: you stopped the agent on P1 (one) twice, and nobody answered, so baton took it down with P1 not done. What should become of P1?")
		if !slices.Equal(q.Options, []string{decide.RestartIt, decide.LeaveStopped, "Pause baton"}) {
			t.Errorf("options %q", q.Options)
		}
		if s := x.stand("P1"); s.Kind != decide.StandNeedsYou || s.Why != "its agent was stopped twice; waiting for your answer" {
			t.Errorf("stand %+v", s)
		}
		// Nobody answers it for the human, however long: only P1 waits, and P2 is taken up meanwhile.
		x.wait(2 * time.Hour)
		if w := x.worker("P1"); w.Status != state.WorkerHeld || len(x.state().Relay) == 0 || x.logged("relay_answered", map[string]any{"type": state.RelayStopped, "answer": "Ask me later"}) {
			t.Fatalf("P1 after 2h: %+v\n%s", w, x.events())
		}
		if x.status("P2") != state.WorkerWorking || x.spawnsOf("P1") != spawns {
			t.Errorf("the rest of the run: P2 %q, spawns %v", x.status("P2"), x.spawns())
		}
		// Its dialog's time runs out (the mod settles it): it steps aside, unanswered, and is asked again
		// once the human writes in the lead's session.
		x.asked("P1", "What should become of P1?")
		x.answers("Ask me later", true, false)
		x.tick(time.Second)
		asks := len(x.asks)
		x.wait(30 * time.Minute)
		if len(x.asks) != asks || x.worker("P1").Status != state.WorkerHeld {
			t.Fatalf("asked again with nobody there: %d asks, %+v", len(x.asks)-asks, x.worker("P1"))
		}
		x.lead.Update(func(st *state.State) error { st.Run.HumanAt = x.now; return nil })
		x.tick(time.Second)
		x.tick(time.Second)
		if len(x.asks) == asks || !x.logged("relay_again", map[string]any{"phase": "P1", "why": "the human is back"}) {
			t.Fatalf("not asked again once the human wrote\n%s", x.events())
		}
		x.asked("P1", "What should become of P1?")
		// The human's Restart it starts a fresh agent and counts the stops afresh: a third stop is a first.
		x.answers(decide.RestartIt, false, false)
		x.tick(time.Second)
		x.atWork("P1")
		if w := x.worker("P1"); w.Stops != 0 || !w.TakenDown.IsZero() {
			t.Fatalf("after Restart it: %+v", w)
		}
		x.stopAgent("P1")
		if w := x.worker("P1"); w.Stops != 1 {
			t.Fatalf("the third stop counts %d", w.Stops)
		}
		x.asked("P1", "baton: you stopped the agent on P1 (one). Restart it?")
	})
	t.Run("its work done", func(t *testing.T) {
		x, _ := secondStop(t)
		x.finish("P1", map[string]string{"one.txt": "one\n"}, true)
		x.lapse()
		if x.status("P1") != state.WorkerLanded || !x.logged("worker_retired", map[string]any{"phase": "P1", "by": state.ByTimeout}) || x.logged("agent_taken_down", nil) {
			t.Errorf("P1's worker %+v\n%s", x.worker("P1"), x.events())
		}
	})
}

// The human's Restart it on a second stop starts a fresh agent, and counts the stops afresh: the next stop
// is a first one again, asked as one.
func TestRestartOnASecondStopCountsAfresh(t *testing.T) {
	x, _ := secondStop(t)
	x.answers(decide.RestartIt, false, false)
	x.tick(time.Second)
	x.atWork("P1")
	if w := x.worker("P1"); w.Stops != 0 || !x.logged("agent_restarted", map[string]any{"phase": "P1", "by": state.ByHuman, "stops": 0}) {
		t.Fatalf("worker %+v\n%s", w, x.events())
	}
	x.stopAgent("P1")
	if w := x.worker("P1"); w.Stops != 1 {
		t.Fatalf("stops %d", w.Stops)
	}
	x.asked("P1", "baton: you stopped the agent on P1 (one). Restart it? Unless you answer by")
}

// The human's Stop it on the question that tells them baton restarted the agent stops the fresh agent, and
// leaves the phase waiting for them; a held phase stays so in the next baton session for the run, until
// /baton run asks for a fresh agent.
func TestStopItOnARestartHoldsThePhase(t *testing.T) {
	x, _ := stoppingCrew(t)
	x.stopAgent("P1")
	x.lapse()
	x.atWork("P1")
	fresh := x.agent("P1")
	x.asked("P1", "Keep it going?")
	x.answers(decide.StopIt, false, false)
	x.tick(time.Second)
	x.tick(time.Second)
	if w := x.worker("P1"); w.Status != state.WorkerHeld || w.Stops != 1 || !slices.Contains(x.stopped, fresh.id) || slices.Contains(x.tracked(), fresh.id) {
		t.Fatalf("worker %+v; stopped %v, tracked %v\n%s", w, x.stopped, x.tracked(), x.events())
	}
	if !strings.Contains(x.announced(), decide.AgentStoppedAgain("P1")) {
		t.Errorf("announced %q", x.announced())
	}
	spawns := x.spawnsOf("P1")
	x.c.Ended("the baton session ended", nil)
	x.proj.Unbind("inst")
	x.c = x.coordinator("next", 1)
	x.wait(30 * time.Minute)
	if x.status("P1") != state.WorkerHeld || x.spawnsOf("P1") != spawns {
		t.Fatalf("the next host: %+v, spawns %v", x.worker("P1"), x.spawns())
	}
	x.lead.Update(func(st *state.State) error {
		e := st.WorkerOn("P1")
		e.Status, e.Since, e.Why = state.WorkerSuspended, x.now, "the human asked for a fresh agent: you stopped the agent before it"
		return nil
	})
	x.tick(time.Second)
	x.atWork("P1")
	if next := x.agent("P1"); next.id == fresh.id || next.tree != fresh.tree {
		t.Errorf("the fresh agent %+v, the one before %+v", next, fresh)
	}
}

// A question about a stopped agent nobody needs answered any more is withdrawn: /baton run took its phase
// up meanwhile.
func TestAStopQuestionTheHumanOvertookIsWithdrawn(t *testing.T) {
	x, _ := stoppingCrew(t)
	x.stopAgent("P1")
	x.lead.Update(func(st *state.State) error {
		e := st.WorkerOn("P1")
		e.Status, e.Since, e.Why = state.WorkerSuspended, x.now, "the human asked for a fresh agent: you stopped the agent before it"
		return nil
	})
	x.tick(time.Second)
	x.atWork("P1")
	if !x.logged("relay_withdrawn", map[string]any{"phase": "P1", "type": state.RelayStopped}) || x.logged("agent_restarted", nil) {
		t.Errorf("events:\n%s", x.events())
	}
	x.lapse()
	if x.status("P1") != state.WorkerWorking || x.spawnsOf("P1") != 2 {
		t.Errorf("P1 %+v, spawns %v", x.worker("P1"), x.spawns())
	}
}

// takenDown brings the crew's P1 to a take-down: its agent stopped twice, nobody answering, its work not
// done. ready sets the rest of the run first, while the second stop's question is open (nil: as it is).
// It returns the question with no deadline that waits for the human then.
func takenDown(t *testing.T, ready func(x *crew)) (*crew, *pushes, state.Relay) {
	t.Helper()
	x, p := secondStop(t)
	if ready != nil {
		ready(x)
	}
	x.lapse()
	st := x.state()
	r := st.StoppedTwice()
	if r == nil || x.worker("P1").TakenDown.IsZero() {
		t.Fatalf("not taken down: %+v, relay %+v\n%s", x.worker("P1"), st.Relay, x.events())
	}
	return x, p, *r
}

// leadDone has the lead done with all it can do: P0 and P2 are done, and P3 waits for P1.
func leadDone(x *crew) {
	x.lead.Update(func(st *state.State) error {
		st.Phases["P0"].Status, st.Phases["P2"].Status, st.Current = state.PhaseDone, state.PhaseDone, ""
		return nil
	})
}

// takenBack checks that the crew's P1 was taken back from its twice-stopped agent for the lead: its
// worker retired, the question closed, the lead holding P1 after a boundary, the decision recorded, the
// human told and pushed, and the lead's brief saying why; and that no agent takes P1 up after (spawns:
// the agents started on it before). It returns the decision.
func takenBack(x *crew, p *pushes, spawns int) state.Decision {
	t := x.t
	t.Helper()
	w := x.worker("P1")
	st := x.state()
	if w.Status != state.WorkerFailed || w.Why != decide.StoppedTakenWhy || isDir(w.Worktree) {
		t.Fatalf("P1's worker %+v\n%s", w, x.events())
	}
	if st.Current != "P1" || !st.BoundaryOwed || !x.logged("compact_queued", map[string]any{"reason": "boundary", "next": "P1", "by": "stopped_phase_taken"}) {
		t.Fatalf("the lead holds %q (owed %v)\n%s", st.Current, st.BoundaryOwed, x.events())
	}
	if q := st.StoppedTwice(); q != nil {
		t.Errorf("the question is still open: %+v", q)
	}
	for _, r := range st.Relay {
		if r.Persistent() && (r.Answered == nil || r.Answered.By != state.ByTimeout || r.Applied.IsZero()) {
			t.Errorf("the question did not close: %+v", r)
		}
	}
	d := st.Decisions[len(st.Decisions)-1]
	if d.Kind != state.Note || d.Phase != "P1" || !strings.Contains(d.What, "took P1 back from its agent") || !strings.HasPrefix(d.Undo, "/baton pause") {
		t.Errorf("decision %+v", d)
	}
	if !x.logged("stopped_phase_taken", map[string]any{"phase": "P1", "decision": d.ID, "stops": 2, "next": "P1"}) {
		t.Errorf("not logged\n%s", x.events())
	}
	if !strings.Contains(x.announced(), "baton: noted ("+d.ID+") — nobody answered about the agent you stopped twice on P1, and nothing else in the run could move") {
		t.Errorf("announced %q", x.announced())
	}
	handoff, _ := os.ReadFile(x.lead.HandoffPath())
	b := brief.Write(brief.Input{Kind: brief.Boundary, Plan: x.pl, State: st, Handoff: string(handoff), Words: decide.Words{Git: true, Timeout: DefaultTiming.EscalationTimeout}})
	for _, want := range []string{"Now: P1 — one. Begin it now.", "[baton] The human stopped the phase agent on P1 twice and did not answer baton's question about it", d.ID} {
		if !strings.Contains(b, want) {
			t.Errorf("the lead's brief lacks %q:\n%s", want, b)
		}
	}
	x.wait(15 * time.Minute)
	if got := p.of("agent_stopped"); !slices.Contains(got, decide.StoppedTakenNotice("P1")) {
		t.Errorf("pushed %q", got)
	}
	if x.spawnsOf("P1") != spawns || x.status("P1") != state.WorkerFailed {
		t.Errorf("an agent took P1 up again: %v, %+v", x.spawns(), x.worker("P1"))
	}
	return d
}

// A phase whose agent the human stopped twice, which the rest of the run needs, waits for their answer
// only while something else can move. Once nothing can, baton takes it back from its agent
// escalation_timeout later, and the lead takes it up at a boundary; the human's answer before then wins.
func TestATwiceStoppedPhaseTheRunNeeds(t *testing.T) {
	timeout := DefaultTiming.EscalationTimeout
	t.Run("nothing else can move", func(t *testing.T) {
		x, p, r := takenDown(t, leadDone)
		if r.Stuck.IsZero() {
			t.Fatalf("the moment nothing could move was not kept: %+v\n%s", r, x.events())
		}
		spawns := x.spawnsOf("P1")
		due := x.state().TakeUpAt(r, r.Stuck, timeout)
		x.wait(due.Sub(x.now) - time.Second)
		if x.logged("stopped_phase_taken", nil) || x.status("P1") != state.WorkerHeld {
			t.Fatalf("taken back before %s (now %s): %+v", due, x.now, x.worker("P1"))
		}
		x.tick(time.Second)
		x.tick(time.Second)
		takenBack(x, p, spawns)
	})
	t.Run("the lead holds a phase meanwhile", func(t *testing.T) {
		x, p, _ := takenDown(t, func(x *crew) {
			x.lead.Update(func(st *state.State) error { st.Phases["P2"].Status = state.PhaseDone; return nil })
		})
		spawns := x.spawnsOf("P1")
		x.wait(40 * time.Minute)
		if r := x.state().StoppedTwice(); r == nil || !r.Stuck.IsZero() || x.logged("stopped_phase_taken", nil) || x.status("P1") != state.WorkerHeld {
			t.Fatalf("P1 did not wait while the lead held P0: %+v, %+v\n%s", r, x.worker("P1"), x.events())
		}
		leadDone(x)
		x.tick(time.Second)
		x.tick(time.Second)
		if r := x.state().StoppedTwice(); r == nil || r.Stuck.IsZero() {
			t.Fatalf("the moment nothing could move was not kept: %+v", r)
		}
		x.wait(timeout - 2*time.Minute)
		if x.logged("stopped_phase_taken", nil) {
			t.Fatalf("taken back less than %s after nothing could move\n%s", timeout, x.events())
		}
		x.wait(3 * time.Minute)
		takenBack(x, p, spawns)
	})
	t.Run("another agent at work meanwhile", func(t *testing.T) {
		x, p, _ := takenDown(t, nil)
		// P2 can start, and starts once the question's dialog has stepped aside (no agent starts under one);
		// then the lead is done with P0.
		x.wait(15 * time.Minute)
		x.atWork("P2")
		x.lead.Update(func(st *state.State) error {
			st.Phases["P0"].Status, st.Current = state.PhaseDone, ""
			return nil
		})
		spawns := x.spawnsOf("P1")
		x.wait(30 * time.Minute)
		if r := x.state().StoppedTwice(); r == nil || !r.Stuck.IsZero() || x.logged("stopped_phase_taken", nil) {
			t.Fatalf("P1 did not wait while P2's agent worked: %+v\n%s", r, x.events())
		}
		x.finish("P2", map[string]string{"two.txt": "two\n"}, true)
		for range 5 {
			x.tick(time.Second)
		}
		if x.status("P2") != state.WorkerLanded {
			t.Fatalf("P2 did not land: %+v\n%s", x.worker("P2"), x.events())
		}
		x.wait(timeout - 2*time.Minute)
		if x.logged("stopped_phase_taken", nil) {
			t.Fatalf("taken back less than %s after nothing could move\n%s", timeout, x.events())
		}
		x.wait(3 * time.Minute)
		takenBack(x, p, spawns)
	})
	for _, c := range []struct {
		answer, status string
	}{{decide.RestartIt, state.WorkerWorking}, {decide.LeaveStopped, state.WorkerHeld}} {
		t.Run("the human answers first: "+c.answer, func(t *testing.T) {
			x, _, r := takenDown(t, leadDone)
			x.asked("P1", "What should become of P1?")
			x.wait(x.state().TakeUpAt(r, r.Stuck, timeout).Sub(x.now) - 2*time.Minute)
			x.answers(c.answer, false, false)
			x.tick(time.Second)
			x.wait(30 * time.Minute)
			if x.logged("stopped_phase_taken", nil) || x.status("P1") != c.status || x.state().Current != "" {
				t.Fatalf("the human's %s: P1 %+v, the lead on %q\n%s", c.answer, x.worker("P1"), x.state().Current, x.events())
			}
		})
	}
}
