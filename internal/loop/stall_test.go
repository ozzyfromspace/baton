package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// still is a run whose stall alarm the test drives: the lead on P0, a phase agent on P1 (its sub-run),
// and P2 after P1, everything last moving at t0, both loops idle.
type still struct {
	t       *testing.T
	t0, now time.Time
	lead    *state.Store
	sub     *state.Store
	c       *Coordinator
	log     string // the log of the job a case runs, if any
}

func newStill(t *testing.T) *still {
	x := &still{t: t, t0: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
	x.now = x.t0
	clock := func() time.Time { return x.now }
	p, err := state.OpenProject(filepath.Join(t.TempDir(), ".baton"), clock)
	if err != nil {
		t.Fatal(err)
	}
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "zero", After: []string{}},
		{ID: "P1", Title: "one", After: []string{}}, {ID: "P2", Title: "two", After: []string{"P1"}}}}
	if x.lead, err = p.Bind(state.Binding{Session: "sess-1", Instance: "inst"}); err != nil {
		t.Fatal(err)
	}
	x.lead.WritePlan(pl)
	ref := state.WorkerRef(x.lead.Ref, "P1")
	x.lead.Update(func(st *state.State) error {
		*st = state.Attach(pl, x.t0, state.Origin{})
		st.RunBranch = "main"
		if err := state.Assign(st, pl, state.Worker{Phase: "P1", Ref: ref, AgentID: "a1"}, x.t0); err != nil {
			return err
		}
		st.Workers[0].Status = state.WorkerWorking
		st.Run.LastActivity, st.Run.LastStop = x.t0, x.t0
		return state.Claim(st, "inst", 1, x.t0)
	})
	if x.sub, err = p.NewWorker(x.lead.Ref, "P1", "inst", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	only, _ := pl.Only("P1")
	x.sub.WritePlan(only)
	x.sub.Update(func(st *state.State) error {
		*st = state.AttachWorker(*st, "P1")
		state.Start(st, x.t0, state.Origin{})
		st.Run.LastActivity, st.Run.LastStop = x.t0, x.t0
		return nil
	})
	x.log = filepath.Join(t.TempDir(), "job.log")
	x.c = &Coordinator{Lead: &Loop{Store: x.lead, Notify: &bomb{}, Project: "demo", Timing: DefaultTiming}, Project: p}
	return x
}

// look lets the alarm look at the run at at.
func (x *still) look(at time.Time) {
	x.now = at
	st, err := x.lead.Load()
	if err != nil {
		x.t.Fatal(err)
	}
	x.c.stalls(host.View{Now: at, Store: x.lead, Owner: true}, x.lead, st)
}

// told lists the notices the alarm left for the human.
func (x *still) told() []string {
	st, _ := x.lead.Load()
	var out []string
	for _, n := range st.Run.Notices {
		if n.Kind == "run_stalled" {
			out = append(out, n.Text)
		}
	}
	return out
}

// running gives the sub-run a gate that runs, its runner beating at at, its log of size bytes.
func (x *still) running(at time.Time, size int) {
	x.sub.Update(func(st *state.State) error {
		if len(st.Jobs) == 0 {
			q, _, _ := state.QueueJob(st, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make test"}, x.t0)
			j := st.Job(q.ID)
			j.Status, j.Started, j.Log = state.JobRunning, x.t0, x.log
			st.Phases["P1"].Status = state.PhaseVerifying
			st.Current = ""
		}
		st.Jobs[0].Heartbeat = at
		return nil
	})
	os.WriteFile(x.log, []byte(strings.Repeat("x", size)), 0o644)
}

func (x *still) setLead(fn func(st *state.State)) {
	x.lead.Update(func(st *state.State) error { fn(st); return nil })
}

func (x *still) setSub(fn func(st *state.State)) {
	x.sub.Update(func(st *state.State) error { fn(st); return nil })
}

// The stall alarm's table: a run that has not moved for StallAfter, with nothing busy, is told to the
// human once, naming what each phase left waits on; anything that moved, anything busy, and anything that
// says no alarm can be due keeps it quiet.
func TestARunThatStopsMovingSaysSo(t *testing.T) {
	after := DefaultTiming.StallAfter
	recent := after - 10*time.Minute // movement this long after t0, at a look at t0 + after + 1m
	quiet := "the run has not moved for " + decide.Span(after+time.Minute)
	for _, c := range []struct {
		name string
		set  func(x *still)
		told []string // what the notice names; nil: no notice
	}{
		{"nothing moved, nothing busy", func(*still) {},
			[]string{quiet + " — P0: the lead works on it", "P1: agent between turns", "P2: waits on P1", "lead: its turn over, with P0 in hand"}},
		// Movement.
		{"a hook of the lead's", func(x *still) { x.setLead(func(st *state.State) { st.Run.LastActivity = x.t0.Add(recent) }) }, nil},
		{"the lead's turn began", func(x *still) { x.setLead(func(st *state.State) { st.Run.TurnStarted = x.t0.Add(recent) }) }, nil},
		{"the lead's turn ended", func(x *still) { x.setLead(func(st *state.State) { st.Run.LastStop = x.t0.Add(recent) }) }, nil},
		{"the human took part", func(x *still) { x.setLead(func(st *state.State) { st.Run.HumanAt = x.t0.Add(recent) }) }, nil},
		{"a compaction ended", func(x *still) { x.setLead(func(st *state.State) { st.Run.Compaction.Finished = x.t0.Add(recent) }) }, nil},
		{"a landing", func(x *still) {
			x.setLead(func(st *state.State) { st.Landed = []state.Landing{{Phase: "P9", At: x.t0.Add(recent)}} })
		}, nil},
		{"a worker changed how it stands", func(x *still) { x.setLead(func(st *state.State) { st.Workers[0].Since = x.t0.Add(recent) }) }, nil},
		{"a phase done", func(x *still) {
			x.setLead(func(st *state.State) {
				st.Phases["P0"].Status, st.Phases["P0"].DoneAt = state.PhaseDone, x.t0.Add(recent)
			})
		}, nil},
		{"a job ended", func(x *still) {
			x.setLead(func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Status: state.JobPassed, Ended: x.t0.Add(recent), Delivered: x.t0.Add(recent)}}
			})
		}, nil},
		{"a hook of the agent's", func(x *still) { x.setSub(func(st *state.State) { st.Run.LastActivity = x.t0.Add(recent) }) }, nil},
		{"the agent's turn ended", func(x *still) { x.setSub(func(st *state.State) { st.Run.LastStop = x.t0.Add(recent) }) }, nil},
		{"the agent's job ended", func(x *still) {
			x.setSub(func(st *state.State) {
				st.Jobs = []state.Job{{ID: "P1.j1", Phase: "P1", Status: state.JobFailed, Ended: x.t0.Add(recent)}}
			})
		}, nil},
		{"the machine just woke", func(x *still) { x.c.Lead.wokeAt = x.t0.Add(recent) }, nil},
		// Busy.
		{"a tool call in flight", func(x *still) {
			x.setSub(func(st *state.State) {
				st.Run.ToolStarted(state.ToolCall{ID: "t1", Tool: "Bash", Since: x.t0.Add(after)})
			})
		}, nil},
		// Background work younger than BackgroundIdle. (With the defaults, StallAfter and BackgroundIdle are
		// both 20 minutes: work that runs as long as the run has not moved is past it.)
		{"the model's own background work", func(x *still) {
			x.c.Lead.Timing.BackgroundIdle = after + 10*time.Minute
			x.setLead(func(st *state.State) {
				st.Run.Background = []state.Task{{ID: "b1", Type: "shell", Status: "running", Description: "npm test", Since: x.t0}}
			})
		}, nil},
		{"background work past BackgroundIdle", func(x *still) {
			x.c.Lead.Timing.BackgroundIdle = after
			x.setLead(func(st *state.State) {
				st.Run.Background = []state.Task{{ID: "b1", Type: "shell", Status: "running", Description: "npm test", Since: x.t0}}
			})
		}, []string{quiet}},
		// Never.
		{"the usage limit", func(x *still) {
			x.setLead(func(st *state.State) { st.Usage = &state.Usage{Limited: x.t0.Add(2 * after), Hit: x.t0, By: "lead"} })
		}, nil},
		{"the usage limit lifted a moment ago", func(x *still) {
			x.setLead(func(st *state.State) { st.Usage = &state.Usage{Limited: x.t0.Add(recent), Hit: x.t0, By: "lead"} })
		}, nil},
		{"a dialog open", func(x *still) {
			x.setLead(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "a1", Since: x.t0}) })
		}, nil},
		{"a paused run", func(x *still) { x.setLead(func(st *state.State) { st.Mode = state.ModePaused }) }, nil},
		// What it names.
		{"a phase held for the human", func(x *still) {
			x.setLead(func(st *state.State) { st.Workers[0].Status, st.Workers[0].Since = state.WorkerHeld, x.t0 })
		}, []string{"P1: needs you: its agent was stopped; /baton run starts a fresh one"}},
		{"an escalation pushed already", func(x *still) {
			x.setLead(func(st *state.State) {
				st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "no activity on P0 for 20m, even after a reminder", Since: x.t0, Pushed: true}
			})
		}, []string{"lead: needs you: no activity on P0 for 20m, even after a reminder"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newStill(t)
			c.set(x)
			x.look(x.t0.Add(after + time.Minute))
			told := x.told()
			if c.told == nil {
				if len(told) != 0 {
					t.Errorf("told %q", told)
				}
				return
			}
			if len(told) != 1 {
				t.Fatalf("told %q", told)
			}
			for _, w := range c.told {
				if !strings.Contains(told[0], w) {
					t.Errorf("the notice lacks %q:\n%s", w, told[0])
				}
			}
			ev, _ := os.ReadFile(filepath.Join(x.lead.Dir, "events.jsonl"))
			if !hasEvent(string(ev), "run_stalled", map[string]any{"quiet": (after + time.Minute).String()}) {
				t.Errorf("events:\n%s", ev)
			}
		})
	}
}

// A long gate whose log still grows keeps the run from counting as stalled; one whose log stopped growing
// while its runner beats on is a hung gate, and named so.
func TestAHungGateIsTold(t *testing.T) {
	after := DefaultTiming.StallAfter
	for _, grows := range []bool{true, false} {
		x := newStill(t)
		x.running(x.t0.Add(time.Minute), 100)
		x.look(x.t0.Add(time.Minute))
		size := 100
		if grows {
			size = 5000
		}
		at := x.t0.Add(after + 2*time.Minute)
		x.running(at, size)
		x.look(at)
		switch told := x.told(); {
		case grows && len(told) != 0:
			t.Errorf("a gate whose log grows: told %q", told)
		case !grows && (len(told) != 1 || !strings.Contains(told[0], "P1: verifying "+decide.Span(after+2*time.Minute)+" (job P1.j1), its log has not grown for "+decide.Span(after+time.Minute))):
			t.Errorf("a hung gate: told %q", told)
		}
	}
}

// The alarm is the coordinator's, on the lead's tick: its notice is pushed as the lead's are, and its
// clock starts with the host's first tick.
func TestTheStallAlarmIsPushedFromTheLeadsTick(t *testing.T) {
	x := newStill(t)
	p := &pushes{}
	x.c.Lead.Notify = p
	start := x.t0.Add(time.Hour) // the host starts an hour after anything last moved
	for m := time.Duration(0); m <= DefaultTiming.StallAfter+2*time.Minute; m += time.Minute {
		x.now = start.Add(m)
		x.c.Tick(host.View{Now: x.now, Store: x.lead, Owner: true, LastOutput: x.now.Add(-time.Hour), LastHumanKey: x.now.Add(-time.Hour)}, &typed{})
		if m < DefaultTiming.StallAfter && strings.Contains(strings.Join(p.kinds, " "), "run_stalled") {
			t.Fatalf("pushed after %s: %q", m, p.kinds)
		}
	}
	n := 0
	for i, k := range p.kinds {
		if k == "run_stalled" {
			n++
			if !strings.Contains(p.details[i], "the run has not moved for "+decide.Span(DefaultTiming.StallAfter)+" — P0:") {
				t.Errorf("pushed %q", p.details[i])
			}
		}
	}
	if n != 1 {
		t.Errorf("pushed %q", p.kinds)
	}
}

// The human is told once for a run that stays still; movement arms the alarm again, and the next stretch
// with nothing moving is told again.
func TestTheStallAlarmIsToldOnceAndReArmed(t *testing.T) {
	after := DefaultTiming.StallAfter
	x := newStill(t)
	x.look(x.t0.Add(after + time.Minute))
	for i := 2; i < 10; i++ {
		x.look(x.t0.Add(after + time.Duration(i)*time.Minute))
	}
	if told := x.told(); len(told) != 1 {
		t.Fatalf("told %d times: %q", len(told), told)
	}
	moved := x.t0.Add(after + 15*time.Minute)
	x.setSub(func(st *state.State) { st.Run.LastActivity = moved })
	x.look(moved.Add(time.Minute))
	if st, _ := x.lead.Load(); st.Run.Stalled != nil || len(x.told()) != 1 {
		t.Fatalf("not armed again after moving: %+v, told %q", st.Run.Stalled, x.told())
	}
	x.look(moved.Add(after + time.Minute))
	if told := x.told(); len(told) != 2 {
		t.Errorf("the next stretch: told %q", told)
	}
}
