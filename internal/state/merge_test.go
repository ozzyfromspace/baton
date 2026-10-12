package state

import (
	"reflect"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// finished is a worker's sub-run on P3 whose `baton done` completed it, after its verification j1
// passed on commit c1, and whose worker was told so at its stop.
func finished(t *testing.T) (State, plan.Plan) {
	t.Helper()
	p := graph("P3:")
	st := AttachWorker(New(), "P3")
	Start(&st, t0, Origin{Head: "base"})
	j, _, _ := QueueJob(&st, Job{Phase: "P3", Kind: JobVerify, Command: "make test", Commit: "c1", Notes: "all of it"}, t0)
	x := st.Job(j.ID)
	x.Status, x.Ended, x.Delivered = JobPassed, t0, t0
	if _, err := Done(&st, p, "P3", t0, DoneOpts{}); err != nil {
		t.Fatal(err)
	}
	if st.Mode != ModeComplete {
		t.Fatalf("the sub-run is %s, not complete", st.Mode)
	}
	st.Run.CompleteNotified = true
	return st, p
}

// A finished worker's phase is verified again on its rebased branch: green completes the sub-run again
// without a word to the worker, and red takes the phase up again, as any red verification does.
func TestReverify(t *testing.T) {
	for _, c := range []struct {
		name   string
		status string
	}{{"green", JobPassed}, {"red", JobFailed}} {
		t.Run(c.name, func(t *testing.T) {
			st, p := finished(t)
			st.Run.CompleteNotified = false // its worker's stop did not come: it is told nothing all the same
			now := t0.Add(time.Hour)
			j, err := Reverify(&st, Job{Phase: "P3", Command: "make test", Commit: "c2", Notes: "rebased"}, now)
			if err != nil {
				t.Fatal(err)
			}
			if j.ID != "P3.j2" || j.Kind != JobVerify || j.Commit != "c2" || j.Status != JobQueued || !j.Requeue || !st.Job(j.ID).Requeue {
				t.Errorf("queued %+v", j)
			}
			if st.Mode != ModeRunning || st.Current != "" || st.Phases["P3"].Status != PhaseVerifying || Complete(st, p) {
				t.Fatalf("verifying again: mode %s, current %q, %+v", st.Mode, st.Current, st.Phases["P3"])
			}
			if _, err := Reverify(&st, Job{Phase: "P3"}, now); err == nil {
				t.Error("a phase verifying already was verified again")
			}

			x := st.Job(j.ID)
			x.Status, x.Ended = c.status, now.Add(time.Minute)
			Settle(&st, p, now.Add(time.Minute))
			if c.status == JobPassed {
				if st.Mode != ModeComplete || st.Phases["P3"].Status != PhaseDone || len(st.Due()) > 0 {
					t.Errorf("after it passed: mode %s, %+v, due %v", st.Mode, st.Phases["P3"], st.Due())
				}
				return
			}
			ps := st.Phases["P3"]
			if st.Mode != ModeRunning || st.Current != "P3" || !st.BoundaryOwed || ps.Status != PhaseParked || ps.Park.Reason != ParkRed {
				t.Fatalf("after it failed: mode %s, current %q, owed %v, %+v", st.Mode, st.Current, st.BoundaryOwed, ps)
			}
			if st.Run.CompleteNotified {
				t.Error("the phase reopened red, yet its next completion would not be reported")
			}
		})
	}
}

// A finished worker's phase whose work cannot land goes back to the worker, in hand, with why.
func TestReopen(t *testing.T) {
	st, _ := finished(t)
	now := t0.Add(time.Hour)
	m := Message{Kind: MessageLand, Text: "rebase it yourself"}
	if err := Reopen(&st, "P3", m, now); err != nil {
		t.Fatal(err)
	}
	ps := st.Phases["P3"]
	if st.Mode != ModeRunning || st.Current != "P3" || st.BoundaryOwed || ps.Status != PhaseActive || !ps.DoneAt.IsZero() || ps.StartHead != "base" {
		t.Fatalf("reopened: mode %s, current %q, owed %v, %+v", st.Mode, st.Current, st.BoundaryOwed, ps)
	}
	if st.Run.CompleteNotified {
		t.Error("its next completion would not be reported")
	}
	if err := Reopen(&st, "P3", Message{Kind: MessageLand, Text: "again"}, now); err != nil {
		t.Fatalf("reopening it again: %v", err)
	}
	if got := TakeMail(&st); len(got) != 2 || got[0].Text != "rebase it yourself" || !got[0].At.Equal(now) || got[1].Text != "again" {
		t.Errorf("mail = %+v", got)
	}
	if err := Reopen(&st, "P9", m, now); err == nil {
		t.Error("an unknown phase was reopened")
	}
	running := AttachWorker(New(), "P3")
	if err := Reopen(&running, "P3", m, now); err == nil {
		t.Error("a phase not finished was reopened")
	}
}

// Messages and landings wait while a compaction is owed or underway, and are given once.
func TestMailAndLandingsWaitForACompaction(t *testing.T) {
	st := New()
	st.Mode = ModeRunning
	Tell(&st, Message{Kind: MessageLand, Text: "hello"}, t0)
	st.Landed = []Landing{{Phase: "P1", Commits: []string{"c1"}}, {Phase: "P2", Commits: []string{"c2", "c3"}, Told: true}}
	st.BoundaryOwed = true
	if len(TakeMail(&st)) != 0 || len(TakeLandings(&st)) != 0 {
		t.Fatal("given with a compaction owed")
	}
	st.BoundaryOwed = false
	st.Run.Compaction.Queue("boundary", t0)
	if len(TakeMail(&st)) != 0 || len(TakeLandings(&st)) != 0 {
		t.Fatal("given with a compaction queued")
	}
	st.Run.Compaction = Compaction{}
	if got := TakeMail(&st); len(got) != 1 || len(TakeMail(&st)) != 0 {
		t.Errorf("mail = %+v, then given again", got)
	}
	if got := TakeLandings(&st); len(got) != 1 || got[0].Phase != "P1" || len(TakeLandings(&st)) != 0 {
		t.Errorf("landings = %+v, then told again", got)
	}
	if got := st.LandedCommits(); !reflect.DeepEqual(got, []string{"c1", "c2", "c3"}) {
		t.Errorf("landed commits = %q", got)
	}
}

func TestToolsInFlight(t *testing.T) {
	var r Runtime
	r.ToolStarted(ToolCall{ID: "t1", Tool: "Edit", Since: t0})
	r.ToolStarted(ToolCall{ID: "t1", Tool: "Edit", Since: t0})
	r.ToolStarted(ToolCall{ID: "t2", Agent: "a1", Tool: "Bash", Since: t0})
	r.ToolStarted(ToolCall{Tool: "Read", Since: t0.Add(time.Second)})
	r.ToolStarted(ToolCall{ID: "t3", Tool: "Bash", Since: t0.Add(-ToolStale)})
	ids := func() []string {
		var out []string
		for _, c := range r.InFlight(t0.Add(time.Minute)) {
			out = append(out, c.ID+"/"+c.Tool)
		}
		return out
	}
	if got := ids(); !reflect.DeepEqual(got, []string{"t1/Edit", "t2/Bash", "/Read"}) {
		t.Errorf("in flight = %q (a call twice, or a stale one, counted)", got)
	}
	r.ToolEnded("t1", "", "Edit")
	r.ToolEnded("", "", "Read")
	if got := ids(); !reflect.DeepEqual(got, []string{"t2/Bash"}) {
		t.Errorf("after two ended: %q", got)
	}
	r.ToolStarted(ToolCall{ID: "t4", Tool: "Write", Since: t0})
	r.ToolsEnded("")
	if got := ids(); !reflect.DeepEqual(got, []string{"t2/Bash"}) {
		t.Errorf("after the main agent's turn ended: %q (the subagent's call is still running)", got)
	}
	r.ToolsEnded("a1")
	if len(r.Tools) != 0 {
		t.Errorf("after the subagent ended: %+v", r.Tools)
	}
	for i := range maxTools + 5 {
		r.ToolStarted(ToolCall{ID: string(rune('a' + i)), Tool: "Bash", Since: t0})
	}
	if len(r.Tools) != maxTools || r.Tools[0].ID != string(rune('a'+5)) {
		t.Errorf("%d calls on record, the oldest %q: the oldest go first", len(r.Tools), r.Tools[0].ID)
	}
}

// A landing holds the lock; a tool call waits for it, and gives up after a while.
func TestLandingLock(t *testing.T) {
	dir := t.TempDir()
	release, ok, err := HoldLanding(dir)
	if !ok || err != nil {
		t.Fatalf("HoldLanding: %v, %v", ok, err)
	}
	if _, again, _ := HoldLanding(dir); again {
		t.Fatal("two landings held the lock at once")
	}
	if waited, err := WaitLanding(dir, 50*time.Millisecond); err == nil || waited < 50*time.Millisecond {
		t.Errorf("waiting on a landing that went on: waited %s, %v", waited, err)
	}
	go func(release func()) {
		time.Sleep(100 * time.Millisecond)
		release()
	}(release)
	if waited, err := WaitLanding(dir, 5*time.Second); err != nil || waited < 50*time.Millisecond {
		t.Errorf("waiting on a landing that ended: waited %s, %v", waited, err)
	}
	if waited, err := WaitLanding(dir, time.Second); err != nil || waited > 500*time.Millisecond {
		t.Errorf("with no landing: waited %s, %v", waited, err)
	}
	release, ok, _ = HoldLanding(dir)
	if !ok {
		t.Fatal("the lock was not let go")
	}
	release()
}
