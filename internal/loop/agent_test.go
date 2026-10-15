package loop

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's loop keeps its sub-run claimed by its worker's instance, as often as a session's host
// refreshes its own claim: every other baton process sees the worker at work, and leaves it alone.
func TestAnAgentsLoopKeepsItsSubRunClaimed(t *testing.T) {
	x := newFleet(t)
	beat := func() *state.Owner {
		st, err := x.work.Load()
		if err != nil {
			t.Fatal(err)
		}
		return st.Owner
	}
	x.tick(time.Second)
	first := x.now
	if o := beat(); o == nil || o.Instance != state.WorkerInstance("inst", "P1") || !o.Heartbeat.Equal(first) || o.PID != os.Getpid() {
		t.Fatalf("owner %+v", o)
	}
	x.tick(beatEvery - 2*time.Second)
	if o := beat(); !o.Heartbeat.Equal(first) {
		t.Errorf("refreshed before its time: %+v", o)
	}
	x.tick(2 * time.Second)
	if o := beat(); !o.Heartbeat.Equal(x.now) {
		t.Errorf("not refreshed: %+v", o)
	}
}

// A phase agent's loop settles its sub-run's jobs: a phase verifying on a gate that passed is done, and
// with it the worker's sub-run, whose work the merge queue then lands. One whose gate failed is its agent's
// again at once, with the result due to it: nothing compacts a phase agent, so its sub-run owes none.
func TestAnAgentsLoopSettlesItsJobs(t *testing.T) {
	for _, status := range []string{state.JobPassed, state.JobFailed} {
		t.Run(status, func(t *testing.T) {
			x := newFleet(t)
			pl, err := x.work.LoadPlan()
			if err != nil {
				t.Fatal(err)
			}
			var id string
			x.set(x.work, func(st *state.State) {
				state.Start(st, x.now, state.Origin{})
				q, _, _ := state.QueueJob(st, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make test", Commit: "c1"}, x.now)
				id = q.ID
				j := st.Job(id)
				j.Status, j.Started, j.PID = state.JobRunning, x.now, 4242
				if _, err := state.Done(st, pl, "P1", x.now, state.DoneOpts{}); err != nil {
					t.Fatal(err)
				}
			})
			if st, _ := x.work.Load(); st.Phases["P1"].Status != state.PhaseVerifying {
				t.Fatalf("P1 %+v", st.Phases["P1"])
			}
			x.set(x.work, func(st *state.State) {
				j := st.Job(id)
				j.Status, j.Ended = status, x.now
				if status == state.JobFailed {
					j.Exit = 1
				}
			})
			x.tick(time.Second)
			st, _ := x.work.Load()
			if status == state.JobPassed {
				if st.Phases["P1"].Status != state.PhaseDone || st.Mode != state.ModeComplete {
					t.Errorf("P1 %+v; the sub-run %s", st.Phases["P1"], st.Mode)
				}
				if !strings.Contains(x.events(x.work), `"by":"verification"`) || strings.Contains(x.events(x.lead), "phase_done") {
					t.Errorf("the sub-run's events:\n%s\nthe lead's:\n%s", x.events(x.work), x.events(x.lead))
				}
				return
			}
			ps := st.Phases["P1"]
			if st.Mode != state.ModeRunning || st.Current != "P1" || st.BoundaryOwed || ps.Status != state.PhaseActive || ps.Park == nil || ps.Park.Reason != state.ParkRed || st.Run.Compaction.InFlight() {
				t.Fatalf("after red: %s on %q, owed %v, P1 %+v, compaction %+v", st.Mode, st.Current, st.BoundaryOwed, ps, st.Run.Compaction)
			}
			if due := st.Due(); len(due) != 1 || due[0].ID != id {
				t.Errorf("due %+v", due)
			}
			if events := x.events(x.work); !hasEvent(events, "phase_resumed", map[string]any{"phase": "P1", "reason": "red", "job": id, "by": "settle"}) ||
				strings.Contains(events, "compact_queued") {
				t.Errorf("the sub-run's events:\n%s", events)
			}
			lead, _ := x.lead.Load()
			if want := "baton: P1's verification failed (exit 1) after 0s (job " + id + ") → its agent takes P1 up again"; !slices.Contains(lead.Run.Announce, want) {
				t.Errorf("told the lead %q, want %q", lead.Run.Announce, want)
			}
		})
	}
}

// A phase's sub-run that a baton before v0.8.2 left owing a boundary before its reopened phase, with the
// compaction queued that nothing would ever type into its agent, is healed by its loop's tick: the phase
// is its agent's again, the compaction cancelled, and the result due.
func TestAStuckSubRunIsHealedByTheTick(t *testing.T) {
	x := newFleet(t)
	id := "P1.j1"
	x.set(x.work, func(st *state.State) {
		st.Current, st.BoundaryOwed = "P1", true
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseParked, StartedAt: x.now, Park: &state.Park{Reason: state.ParkRed, Since: x.now, Job: id}}
		st.Jobs = []state.Job{{ID: id, Phase: "P1", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1, Started: x.now, Ended: x.now}}
		st.Run.Compaction.Queue("boundary", x.now)
	})
	x.tick(time.Second)
	st, _ := x.work.Load()
	if ps := st.Phases["P1"]; st.BoundaryOwed || ps.Status != state.PhaseActive || st.Run.Compaction.Status != state.CompactCancelled {
		t.Fatalf("owed %v, P1 %+v, compaction %+v", st.BoundaryOwed, ps, st.Run.Compaction)
	}
	if due := st.Due(); len(due) != 1 || due[0].ID != id || !hasEvent(x.events(x.work), "phase_resumed", map[string]any{"reason": "red", "job": id, "by": "tick"}) {
		t.Errorf("due %+v; events:\n%s", due, x.events(x.work))
	}
	x.tick(time.Second)
	if n := strings.Count(x.events(x.work), `"kind":"phase_resumed"`); n != 1 {
		t.Errorf("taken up %d times", n)
	}
}

// hasEvent reports an event of kind in log (an events.jsonl) with every one of fields.
func hasEvent(log, kind string, fields map[string]any) bool {
	for _, line := range strings.Split(log, "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil || e["kind"] != kind {
			continue
		}
		if !slices.ContainsFunc(slices.Collect(maps.Keys(fields)), func(k string) bool { return fmt.Sprint(e[k]) != fmt.Sprint(fields[k]) }) {
			return true
		}
	}
	return false
}
