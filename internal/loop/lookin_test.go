package loop

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// failing records that job id of phase's sub-run printed its first failure, at now.
func (x *crew) failing(phase, id string) {
	x.sub(phase).Update(func(st *state.State) error {
		st.Job(id).Failing = &state.Failing{At: x.now, Line: "--- FAIL: TestLogin (0.20s)"}
		return nil
	})
}

// A phase agent's gate that prints a failure as it runs is told to the agent at once, once: an idle agent
// is sent a message, and given the look-in as it ends that turn, after its `baton done` too; one at work
// takes it at its next tool call, and is sent nothing.
func TestAnAgentIsToldItsGateIsFailing(t *testing.T) {
	for _, c := range []struct {
		name       string
		done, busy bool
	}{
		{"idle", false, false},
		{"idle, after done", true, false},
		{"at work", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			x.atWork("P1")
			id := x.waiting("P1")
			if c.done {
				x.done("P1")
			}
			if c.busy {
				x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = true; return nil })
			}
			x.tick(5 * time.Second)
			x.failing("P1", id)
			x.tick(5 * time.Second)
			a := x.agent("P1")
			head := "[baton] Job " + id + " (P1) is failing as it runs"
			if c.busy {
				if len(x.sent) != 0 {
					t.Fatalf("a message to an agent at work: %+v", x.sent)
				}
				_, out := x.hook("PostToolUse", map[string]any{"agent_id": a.id, "agent_type": state.PhaseAgent, "cwd": a.tree, "tool_name": "Bash",
					"tool_use_id": "toolu_1", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
				if !strings.Contains(out, "is failing as it runs") || !x.subLogged("P1", "look_in_told", map[string]any{"type": state.LookFailing, "via": "tool"}) {
					t.Errorf("the tool call gave %s\n%s", out, x.subEvents("P1"))
				}
				return
			}
			if len(x.sent) != 1 || x.sent[0].agent != a.id || !strings.Contains(x.sent[0].text, "[baton] A gate of yours is failing as it runs") ||
				!strings.Contains(x.sent[0].text, "End this turn, and baton gives it to you then") {
				t.Fatalf("sent %+v", x.sent)
			}
			if !x.subLogged("P1", "look_in_typed", map[string]any{"via": "mod"}) {
				t.Errorf("not logged:\n%s", x.subEvents("P1"))
			}
			if got := x.endTurn("P1"); !strings.Contains(got, head) {
				t.Fatalf("its stop gave it %q", got)
			}
			if st, _ := x.sub("P1").Load(); st.Job(id).FailTold.IsZero() || !x.subLogged("P1", "look_in_told", map[string]any{"via": "stop"}) {
				t.Errorf("not told: %+v\n%s", st.Job(id), x.subEvents("P1"))
			}
			x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen, st.Run.LastStop = false, x.now; return nil })
			for range 4 {
				x.tick(30 * time.Second)
			}
			if len(x.sent) != 1 {
				t.Errorf("sent again: %+v", x.sent)
			}
		})
	}
}

// The lead's gate that prints a failure as it runs is told to the lead at once, once: idle, baton's mod
// submits a line for it, and the prompt's hook gives the look-in, after `baton done` moved the run on too;
// at work, its next tool call gives it, and nothing is submitted.
func TestTheLeadIsToldItsGateIsFailing(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle, after done", true: "at work"}[busy], func(t *testing.T) {
			x := newCrew(t, 0)
			x.leadIdle = true
			x.lead.Update(func(st *state.State) error {
				q, _, _ := state.QueueJob(st, state.Job{Phase: "P0", Kind: state.JobVerify, Command: "make e2e", Commit: "c1"}, x.now)
				j := st.Job(q.ID)
				j.Status, j.Started, j.PID = state.JobRunning, x.now, 4242
				if !busy {
					pl, _ := x.lead.LoadPlan()
					if _, err := state.Done(st, pl, "P0", x.now, state.DoneOpts{}); err != nil {
						return err
					}
					st.BoundaryOwed = false
					state.Start(st, x.now, state.Origin{}) // the compaction after it took P1 up
				}
				st.Run.TurnOpen, st.Run.LastStop, st.Run.LastActivity = busy, x.now, x.now
				return nil
			})
			if st := x.state(); !busy && (st.Current == "P0" || st.Phases["P0"].Status != state.PhaseVerifying) {
				t.Fatalf("not moved on: in hand %q, P0 %+v", st.Current, st.Phases["P0"])
			}
			x.tick(time.Second)
			x.lead.Update(func(st *state.State) error {
				st.Jobs[0].Failing = &state.Failing{At: x.now, Line: "--- FAIL: TestLogin (0.20s)"}
				return nil
			})
			x.tick(5 * time.Second)
			if busy {
				if x.logged("look_in_typed", nil) {
					t.Fatalf("submitted to a lead at work:\n%s", x.events())
				}
				_, out := x.hook("PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_1", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
				if !strings.Contains(out, "is failing as it runs") || !x.logged("look_in_told", map[string]any{"via": "tool"}) {
					t.Errorf("the tool call gave %s\n%s", out, x.events())
				}
				return
			}
			if x.count("look_in_typed", map[string]any{"via": "mod"}) != 1 {
				t.Fatalf("not submitted:\n%s", x.events())
			}
			_, out := x.hook("UserPromptSubmit", map[string]any{"prompt": "[baton] A gate of yours is failing as it runs: baton's look-in, attached, says what it printed and what you can do."})
			if !strings.Contains(out, "is failing as it runs: `make e2e` printed this") || !x.logged("look_in_told", map[string]any{"via": "prompt"}) {
				t.Fatalf("the prompt gave %s\n%s", out, x.events())
			}
			for range 4 {
				x.tick(30 * time.Second)
			}
			if n := x.count("look_in_typed", nil); n != 1 {
				t.Errorf("submitted %d times", n)
			}
		})
	}
}

// lookIn lets the coordinator look for look-ins at at (once a minute at most).
func (x *still) lookIn(at time.Time) {
	x.now = at
	st, err := x.lead.Load()
	if err != nil {
		x.t.Fatal(err)
	}
	x.c.lookIns(host.View{Now: at, Store: x.lead, Owner: true}, x.lead, st)
}

// passed puts on record, in store s, a gate of command that passed after took, ending before t0.
func (x *still) passed(s *state.Store, command string, took time.Duration) {
	s.Update(func(st *state.State) error {
		q, _, _ := state.QueueJob(st, state.Job{Phase: "P1", Kind: state.JobVerify, Command: command}, x.t0.Add(-5*time.Hour))
		j := st.Job(q.ID)
		j.Status, j.Started, j.Ended = state.JobPassed, x.t0.Add(-4*time.Hour), x.t0.Add(-4*time.Hour+took)
		return nil
	})
}

// gate starts, in store s, a gate of command for phase, its command begun at t0, its log at log.
func (x *still) gate(s *state.Store, phase, command string, edit func(*state.Job)) string {
	var id string
	s.Update(func(st *state.State) error {
		q, _, _ := state.QueueJob(st, state.Job{Phase: phase, Kind: state.JobVerify, Command: command}, x.t0)
		j := st.Job(q.ID)
		j.Status, j.Started, j.Ran, j.Heartbeat, j.Log = state.JobRunning, x.t0, x.t0, x.t0, x.log
		if edit != nil {
			edit(j)
		}
		id = j.ID
		return nil
	})
	return id
}

// leftIn lists the look-ins left in store s.
func leftIn(t *testing.T, s *state.Store) []state.LookIn {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st.LookIns
}

// A gate past 1.5× its command's slowest earlier pass, and SlowAfter more, is a look-in for its owner, at
// that time and once; so is one at 80% of verify_timeout. A gate with no history, an await, and a gate
// waiting for a lane are not held to it, and an agent's look-in is told to the human in the lead's session.
func TestAGatePastItsHistoryIsALookIn(t *testing.T) {
	x := newStill(t)
	x.c.Lead.Timing.JobTimeout = 3 * time.Hour
	x.passed(x.sub, "make test", 10*time.Minute)
	x.passed(x.lead, "make test", 20*time.Minute) // the lead's runs of it count too
	id := x.gate(x.sub, "P1", "make test", nil)
	x.gate(x.lead, "P0", "make lint", nil) // no history
	os.WriteFile(x.log, []byte("ok\n"), 0o644)
	os.Chtimes(x.log, x.t0.Add(30*time.Minute), x.t0.Add(30*time.Minute))
	x.lookIn(x.t0.Add(34*time.Minute + 30*time.Second)) // 1.5× 20m is 30m, but 20m + 15m is 35m
	if got := leftIn(t, x.sub); len(got) != 0 {
		t.Fatalf("early: %+v", got)
	}
	x.lookIn(x.t0.Add(35*time.Minute + 10*time.Second)) // within a minute of the last look: no look
	if got := leftIn(t, x.sub); len(got) != 0 {
		t.Fatalf("looked within the minute: %+v", got)
	}
	x.lookIn(x.t0.Add(36 * time.Minute))
	got := leftIn(t, x.sub)
	if len(got) != 1 || got[0].Kind != state.LookSlow || got[0].Job != id {
		t.Fatalf("left %+v", got)
	}
	for _, want := range []string{"has run 36m, well past its slowest earlier pass: earlier passing runs of `make test` took 10m and 20m.",
		"has not grown for 6m", "At 3h00m (verify_timeout)"} {
		if !strings.Contains(got[0].Text, want) {
			t.Errorf("lacks %q:\n%s", want, got[0].Text)
		}
	}
	if !strings.Contains(strings.Join(x.leadState().Run.Announce, "\n"), "past its slowest earlier pass (20m) → telling P1's agent") {
		t.Errorf("the human is not told: %q", x.leadState().Run.Announce)
	}
	if !lookLogged(x.sub, "look_in", map[string]any{"type": state.LookSlow, "job": id, "slowest_seconds": 1200, "runs": 2}) {
		t.Errorf("not logged:\n%s", lookEvents(x.sub))
	}
	if got := leftIn(t, x.lead); len(got) != 0 {
		t.Errorf("a gate with no history: %+v", got)
	}
	x.lookIn(x.t0.Add(60 * time.Minute))
	if got := leftIn(t, x.sub); len(got) != 1 {
		t.Errorf("left again: %+v", got)
	}
	x.lookIn(x.t0.Add(2*time.Hour + 24*time.Minute)) // 80% of 3h
	got = leftIn(t, x.sub)
	if len(got) != 2 || got[1].Kind != state.LookTimeout || !strings.Contains(got[1].Text, "has run 2h24m of the 3h00m a job may run") {
		t.Fatalf("near the timeout: %+v", got)
	}
	if got := leftIn(t, x.lead); len(got) != 1 || got[0].Kind != state.LookTimeout {
		t.Errorf("the lead's gate near the timeout: %+v", got)
	}
	if strings.Contains(strings.Join(x.leadState().Run.Announce, "\n"), "make lint") {
		t.Errorf("the lead's own look-in announced as it was left: %q", x.leadState().Run.Announce)
	}

	for _, c := range []struct {
		name string
		edit func(j *state.Job)
	}{
		{"an await", func(j *state.Job) { j.Kind = state.JobAwait }},
		{"waiting for a lane", func(j *state.Job) { j.LaneWait = x.t0 }},
	} {
		y := newStill(t)
		y.passed(y.sub, "make test", 10*time.Minute)
		y.gate(y.sub, "P1", "make test", c.edit)
		y.lookIn(y.t0.Add(2 * time.Hour))
		if got := leftIn(t, y.sub); len(got) != 0 {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// Landings that waited HeldAfter behind the lead's gate in its checkout are a look-in for the lead, once,
// naming them and since when.
func TestLandingsHeldBehindAGateHereAreALookIn(t *testing.T) {
	x := newStill(t)
	id := x.gate(x.lead, "P0", "make e2e", func(j *state.Job) { j.Here = true })
	x.setLead(func(st *state.State) {
		st.Workers[0].Status = state.WorkerLanding
		st.Workers[0].Merge = &state.Merge{Queued: x.t0.Add(time.Minute), Deferred: heldByGate(id), DeferredAt: x.t0.Add(5 * time.Minute)}
	})
	x.lookIn(x.t0.Add(24 * time.Minute))
	if got := leftIn(t, x.lead); len(got) != 0 {
		t.Fatalf("early: %+v", got)
	}
	x.lookIn(x.t0.Add(25 * time.Minute))
	got := leftIn(t, x.lead)
	if len(got) != 1 || got[0].Kind != state.LookHeld || got[0].Job != id {
		t.Fatalf("left %+v", got)
	}
	if want := "job " + id + " (`make e2e`, 25m in), holds landings: P1's work has waited to land since 09:05 (20m)"; !strings.Contains(got[0].Text, want) {
		t.Errorf("lacks %q:\n%s", want, got[0].Text)
	}
	if !lookLogged(x.lead, "look_in", map[string]any{"type": state.LookHeld, "job": id}) {
		t.Errorf("not logged:\n%s", lookEvents(x.lead))
	}
	x.lookIn(x.t0.Add(40 * time.Minute))
	if got := leftIn(t, x.lead); len(got) != 1 {
		t.Errorf("left again: %+v", got)
	}
}

// A file in a second landing conflict of the run is a look-in for the lead, once a file.
func TestAFileInASecondLandingConflictIsALookIn(t *testing.T) {
	x := newStill(t)
	e := x.leadState().Workers[0]
	x.c.returned(x.t0, x.lead, e, "its branch conflicts with the run's in notes.md", []string{"notes.md", "go.sum"})
	if got := leftIn(t, x.lead); len(got) != 0 {
		t.Fatalf("after one conflict: %+v", got)
	}
	x.c.returned(x.t0.Add(time.Hour), x.lead, e, "its branch conflicts with the run's in notes.md", []string{"notes.md"})
	got := leftIn(t, x.lead)
	if len(got) != 1 || got[0].Kind != state.LookConflict || !strings.Contains(got[0].Text, "notes.md (P1, then P1)") || strings.Contains(got[0].Text, "go.sum") {
		t.Fatalf("left %+v", got)
	}
	if !lookLogged(x.lead, "look_in", map[string]any{"type": state.LookConflict}) {
		t.Errorf("not logged:\n%s", lookEvents(x.lead))
	}
	x.c.returned(x.t0.Add(2*time.Hour), x.lead, e, "again", []string{"notes.md"})
	if got := leftIn(t, x.lead); len(got) != 1 {
		t.Errorf("told again: %+v", got)
	}
}

// A look-in the host left for a phase agent reaches it as a first failure does: an idle agent is sent a
// message, and given it as it ends that turn.
func TestAnAgentIsGivenALookInTheHostLeft(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	id := x.waiting("P1")
	x.done("P1")
	x.tick(5 * time.Second)
	x.sub("P1").Update(func(st *state.State) error {
		state.AddLookIn(st, state.LookIn{Kind: state.LookSlow, Job: id, Text: "[baton] Job " + id + " runs long.", Say: "baton: it runs long", At: x.now})
		return nil
	})
	x.tick(5 * time.Second)
	if len(x.sent) != 1 || !strings.Contains(x.sent[0].text, "a look-in for you on a gate of yours that runs") {
		t.Fatalf("sent %+v", x.sent)
	}
	if got := x.endTurn("P1"); !strings.Contains(got, "runs long.") {
		t.Fatalf("its stop gave it %q", got)
	}
}

func (x *still) leadState() state.State {
	st, _ := x.lead.Load()
	return st
}

// lookEvents is store s's event log.
func lookEvents(s *state.Store) string {
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	return string(b)
}

// lookLogged reports an event of kind in store s's log with every one of fields, each as JSON writes it.
func lookLogged(s *state.Store, kind string, fields map[string]any) bool {
	for _, line := range strings.Split(lookEvents(s), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil || e["kind"] != kind {
			continue
		}
		if !slices.ContainsFunc(slices.Collect(maps.Keys(fields)), func(k string) bool { return !sameJSON(e[k], fields[k]) }) {
			return true
		}
	}
	return false
}

// sameJSON reports whether a and b read the same as JSON.
func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
