package loop

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// machineLeft is a machine as a finder reads it: its processes and containers. Stop records what it stops,
// which then ends, and cannot stop those in stubborn; reads counts the looks.
type machineLeft struct {
	table      []procs.Proc
	containers []leftover.Container
	stubborn   map[int]bool
	stopped    []int
	reads      int
}

func (m *machineLeft) finder() leftover.Finder {
	return leftover.Finder{
		Procs: func() ([]procs.Proc, error) {
			m.reads++
			return slices.DeleteFunc(slices.Clone(m.table), func(p procs.Proc) bool { return slices.Contains(m.stopped, p.PID) }), nil
		},
		Containers: func(context.Context) ([]leftover.Container, error) { return m.containers, nil },
		Stop: func(pids []int) []int {
			var left []int
			for _, pid := range pids {
				if m.stubborn[pid] {
					left = append(left, pid)
				} else {
					m.stopped = append(m.stopped, pid)
				}
			}
			return left
		},
	}
}

// underBaton is the environment of a process a hosted session or a phase agent started.
var underBaton = map[string]string{"BATON_INSTANCE": "inst"}

// Retiring a worker stops what it left running in its worktree, before the worktree goes: each process
// started under baton, but for its jobs' own, which their runners stop. The run's own host names to the
// lead, in a look-in, what it did not stop (a container, a process it could not stop, a hidden one in the
// group of one it stopped that still runs), and baton removes no container; the human's own shell there,
// and what runs elsewhere, are left alone. Any other retirer leaves no look-in, and says it in its own way.
func TestRetiringStopsWhatTheWorkerLeftRunning(t *testing.T) {
	for _, host := range []bool{true, false} {
		t.Run(map[bool]string{true: "its run's host", false: "baton clean"}[host], func(t *testing.T) {
			x := newCrew(t, 1)
			w := x.suspended()
			x.sub("P1").Update(func(st *state.State) error {
				st.Jobs = append(st.Jobs, state.Job{ID: "P1.j1", Phase: "P1", Kind: state.JobVerify, Command: "make e2e", Here: true, Status: state.JobRunning, PID: 706, PGID: 707})
				return nil
			})
			m := &machineLeft{table: []procs.Proc{
				{PID: 701, PPID: 1, PGID: 701, Comm: "node", Cwd: filepath.Join(w.Worktree, "web"), Env: underBaton},
				{PID: 702, PPID: 701, PGID: 701, Comm: "sh", Cwd: w.Worktree},
				{PID: 703, PPID: 1, PGID: 703, Comm: "go", Cwd: w.Worktree, Env: underBaton},
				{PID: 704, PPID: 1, PGID: 704, Comm: "zsh", Cwd: w.Worktree},
				{PID: 705, PPID: 1, PGID: 705, Comm: "node", Cwd: x.repo, Env: underBaton},
				{PID: 708, PPID: 706, PGID: 707, Comm: "go", Cwd: w.Worktree, Env: underBaton},
			}, containers: []leftover.Container{
				{Name: "p1-db-1", Status: "Up 4 minutes", Project: "p1", Dir: w.Worktree},
				{Name: "main-db-1", Status: "Up 2 hours", Project: "main", Dir: x.repo},
			}, stubborn: map[int]bool{703: true}}
			r := x.retirement(w)
			if host {
				r.Host, r.By = true, "inst"
			}
			r.Leftovers, r.isRunner = m.finder(), func(int) bool { return false }
			got := RetireWorker(r)
			if got.Skipped != "" || !slices.Equal(m.stopped, []int{701}) || !slices.Equal(leftover.PIDs(got.Stopped), []int{701}) {
				t.Fatalf("retired %+v; stopped %v", got, m.stopped)
			}
			if !slices.Equal(leftover.PIDs(got.Left.Procs), []int{703}) || !slices.Equal(leftover.PIDs(got.Left.Hidden), []int{702}) ||
				len(got.Left.Containers) != 1 || got.Left.Containers[0].Name != "p1-db-1" {
				t.Errorf("left %+v", got.Left)
			}
			if isDir(w.Worktree) {
				t.Errorf("its worktree %s is still there", w.Worktree)
			}
			if !x.logged("leftover_stopped", map[string]any{"phase": "P1", "processes": []int{701}, "names": []string{"node"}}) ||
				!x.logged("leftover_told", map[string]any{"phase": "P1", "containers": []string{"p1-db-1"}, "processes": []int{703, 702}, "look_in": host}) {
				t.Errorf("events:\n%s", x.events())
			}
			lis := x.state().LookIns
			if !host {
				if len(lis) > 0 {
					t.Errorf("baton clean left a look-in: %+v", lis)
				}
				return
			}
			if len(lis) != 1 || lis[0].Kind != state.LookLeftover || lis[0].Job != "" {
				t.Fatalf("look-ins %+v", lis)
			}
			for _, want := range []string{"[baton] P1's agent has ended", ": kill 703\n", ", in process group 701: kill 702\n", "docker rm -f p1-db-1"} {
				if !strings.Contains(lis[0].Text, want) {
					t.Errorf("the look-in lacks %q:\n%s", want, lis[0].Text)
				}
			}
			for _, not := range []string{"process 704", "process 705", "process 708", "kill 704", "kill 705", "kill 708", "main-db-1"} {
				if strings.Contains(lis[0].Text, not) {
					t.Errorf("the look-in names %s:\n%s", not, lis[0].Text)
				}
			}
			if lis[0].Say != "baton: P1 left a container and 2 processes running after its agent ended (p1-db-1, go (pid 703) and sh (pid 702))" {
				t.Errorf("says %q", lis[0].Say)
			}
		})
	}
}

// The run's host that retires a landed worker says, with the line that it retired it, what it stopped.
func TestTheHostSaysWhatItStoppedAsItRetires(t *testing.T) {
	x := newCrew(t, 1)
	e := x.suspended()
	gittest.Write(t, e.Worktree, "parser.go", "package parser\n")
	gittest.Commit(t, e.Worktree, "the parser")
	gittest.Git(t, x.repo, "merge", "-q", "--ff-only", e.Branch)
	if _, err := x.lead.Update(func(st *state.State) error {
		head := gitx.Head(x.repo)
		_, err := state.Landed(st, x.pl, state.Landing{Phase: "P1", Commits: []string{head}, Head: head, Told: true, By: state.LandedByLead}, x.now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	x.c = x.coordinator("next", 1)
	m := &machineLeft{table: []procs.Proc{{PID: 901, PPID: 1, PGID: 901, Comm: "vite", Cwd: e.Worktree, Env: underBaton}}}
	x.c.Leftovers = m.finder()
	for range 3 {
		x.tick(time.Second)
	}
	if !slices.Equal(m.stopped, []int{901}) {
		t.Fatalf("stopped %v\n%s", m.stopped, x.events())
	}
	if said := x.state().Run.Announce; !slices.Contains(said, "baton: the phase agent on P1 ended: its work landed") ||
		!slices.Contains(said, "baton: stopped what P1's agent left running in its worktree: vite (pid 901)") {
		t.Errorf("announced %q", said)
	}
	if lis := x.state().LookIns; len(lis) > 0 {
		t.Errorf("a look-in with nothing left: %+v", lis)
	}
}

// While the machine has no room and a gate runs past its history, its owner is told, once, what finished
// phases left running in their worktrees (running processes and containers), with the command to stop
// each; what a live worker's worktree runs is not a finished phase's. The machine is read only with such a
// gate and a finished phase to look at, and, after a look that found nothing, not again for leftoverEvery.
func TestASlowGateOnABusyMachineIsToldWhatFinishedPhasesLeft(t *testing.T) {
	x := newStill(t)
	x.c.Lead.Timing.JobTimeout = 3 * time.Hour
	x.passed(x.sub, "make test", 10*time.Minute) // past its history at 25m: 1.5× 10m, and 15m more
	id := x.gate(x.sub, "P1", "make test", nil)
	p2, p3, again := filepath.Join(x.t.TempDir(), "P2"), filepath.Join(x.t.TempDir(), "P3"), filepath.Join(x.t.TempDir(), "P4")
	m := &machineLeft{table: []procs.Proc{
		{PID: 801, PPID: 1, PGID: 801, Comm: "node", Cwd: p2, Env: underBaton},
		{PID: 802, PPID: 1, PGID: 802, Comm: "node", Cwd: again, Env: underBaton},
	}, containers: []leftover.Container{
		{Name: "p2-db-1", Status: "Up 40 minutes", Dir: p2},
		{Name: "p2-old-1", Status: "Exited (0) 1 hour ago", Dir: p2},
		{Name: "p3-db-1", Status: "Up 1 hour", Dir: filepath.Join(p3, "deploy")},
		{Name: "p4-db-1", Status: "Up 1 hour", Dir: again},
	}}
	x.c.Leftovers = m.finder()
	look := func(at time.Duration, busy string) {
		x.c.noRoom = busy
		x.lookIn(x.t0.Add(at))
	}
	kinds := func() (out []string) {
		for _, li := range leftIn(t, x.sub) {
			out = append(out, li.Kind)
		}
		return out
	}
	const busy = "the machine is busy (load 21.0 on 8 CPUs)"
	look(20*time.Minute, busy) // not past its history yet
	look(26*time.Minute, busy) // no finished phase
	if m.reads != 0 || slices.Contains(kinds(), state.LookLeftover) {
		t.Fatalf("read %d times, left %v", m.reads, kinds())
	}
	x.lead.Update(func(st *state.State) error {
		st.Workers = append(st.Workers,
			state.Worker{Phase: "P2", Ref: state.WorkerRef(x.lead.Ref, "P2"), Worktree: p2, Status: state.WorkerLanded},
			state.Worker{Phase: "P3", Ref: state.WorkerRef(x.lead.Ref, "P3"), Worktree: p3, Status: state.WorkerLanding},
			state.Worker{Phase: "P4", Ref: state.WorkerRef(x.lead.Ref, "P4"), Worktree: again, Status: state.WorkerLanded, Generation: -1},
			state.Worker{Phase: "P4", Ref: state.WorkerRef(x.lead.Ref, "P4"), Worktree: again, Status: state.WorkerWorking})
		return nil
	})
	look(27*time.Minute, "") // the machine has room
	if m.reads != 0 || slices.Contains(kinds(), state.LookLeftover) {
		t.Fatalf("with room: read %d times, left %v", m.reads, kinds())
	}
	look(28*time.Minute, busy)
	var li state.LookIn
	for _, l := range leftIn(t, x.sub) {
		if l.Kind == state.LookLeftover {
			li = l
		}
	}
	if m.reads != 1 || li.Job != id {
		t.Fatalf("read %d times, left %+v", m.reads, leftIn(t, x.sub))
	}
	for _, want := range []string{
		"[baton] Job " + id + " (P1, `make test`) has run 28m, past its slowest earlier pass, while " + busy + "; and phases that finished left this running:\n  P2:\n",
		"    - process 801 (node) in " + p2 + ": kill 801\n", "docker rm -f p2-db-1", "  P3:\n", "docker rm -f p3-db-1",
	} {
		if !strings.Contains(li.Text, want) {
			t.Errorf("the look-in lacks %q:\n%s", want, li.Text)
		}
	}
	for _, not := range []string{"p2-old-1", "802", "p4-db-1", "P4"} {
		if strings.Contains(li.Text, not) {
			t.Errorf("the look-in names %s:\n%s", not, li.Text)
		}
	}
	if !strings.Contains(strings.Join(x.leadState().Run.Announce, "\n"), "beside what P2 and P3 left running → telling P1's agent") {
		t.Errorf("the human is not told: %q", x.leadState().Run.Announce)
	}
	if !lookLogged(x.sub, "look_in", map[string]any{"type": state.LookLeftover, "job": id, "busy": busy, "phases": []string{"P2", "P3"},
		"processes": []int{801}, "containers": []string{"p2-db-1", "p3-db-1"}}) {
		t.Errorf("not logged:\n%s", lookEvents(x.sub))
	}
	look(40*time.Minute, busy) // told once
	if n := strings.Count(strings.Join(kinds(), " "), state.LookLeftover); n != 1 || m.reads != 1 {
		t.Errorf("left %d, read %d times", n, m.reads)
	}

	// A look that found nothing waits leftoverEvery before the next.
	y := newStill(t)
	y.c.Lead.Timing.JobTimeout = 3 * time.Hour
	y.passed(y.sub, "make test", 10*time.Minute)
	y.gate(y.sub, "P1", "make test", nil)
	y.lead.Update(func(st *state.State) error {
		st.Workers = append(st.Workers, state.Worker{Phase: "P2", Ref: state.WorkerRef(y.lead.Ref, "P2"), Worktree: p2, Status: state.WorkerLanded})
		return nil
	})
	none := &machineLeft{}
	y.c.Leftovers, y.c.noRoom = none.finder(), busy
	for _, at := range []time.Duration{26 * time.Minute, 28 * time.Minute, 30 * time.Minute, 31 * time.Minute} {
		y.lookIn(y.t0.Add(at))
	}
	if none.reads != 2 || len(leftIn(t, y.sub)) != 1 { // its past_history look-in alone
		t.Errorf("read %d times, left %+v", none.reads, leftIn(t, y.sub))
	}
}
