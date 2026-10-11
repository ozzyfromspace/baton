package loop

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// fake is a worker's session that ends when the test says so.
type fake struct {
	mu          sync.Mutex
	done        chan struct{}
	code        int
	ended, quit int
	tail        string
}

func newFake() *fake { return &fake{done: make(chan struct{}), code: -1} }

func (f *fake) Done() <-chan struct{} { return f.done }
func (f *fake) ExitCode() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.code
}
func (f *fake) End() error   { f.mu.Lock(); f.ended++; f.mu.Unlock(); return nil }
func (f *fake) Quit() error  { f.mu.Lock(); f.quit++; f.mu.Unlock(); return nil }
func (f *fake) Tail() []byte { f.mu.Lock(); defer f.mu.Unlock(); return []byte(f.tail) }
func (f *fake) exit(code int) {
	f.mu.Lock()
	f.code = code
	f.mu.Unlock()
	close(f.done)
}

// crew is a host running workers: the run sess-1, on the branch main of a git project, whose plan has P0
// (the lead's), P1 and P2, which can start at once, and P3, after P1. Its sessions are fakes, and its
// work on the workers runs in place.
type crew struct {
	t        *testing.T
	nowMu    sync.Mutex // the clock is read off the lead's tick too
	now      time.Time
	repo     string
	home     string
	proj     *state.Project
	pl       plan.Plan
	lead     *state.Store
	c        *Coordinator
	specs    []host.Spec
	sessions []*fake
	spawnErr error
	leadIdle bool              // the lead's hooks do not run
	typedTo  map[string]*typed // what was typed into each worker's session, by its phase
	machine  machine.Reading   // what the machine reads (roomy, unless the test says otherwise)
}

// roomy is a machine with room for workers: 14 CPUs, 48 GiB, little load, and memory pressure normal.
var roomy = machine.Reading{CPUs: 14, MemTotal: 48 << 30, MemAvailablePct: 45, Pressure: machine.PressureNormal, Load1: 3.1, Known: true}

func newCrew(t *testing.T, max int) *crew {
	return newCrewOn(t, max, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}},
		{ID: "P2", Title: "two", After: []string{}}, {ID: "P3", Title: "three", After: []string{"P1"}}}})
}

// newCrewOn is a crew whose run has the plan pl.
func newCrewOn(t *testing.T, max int, pl plan.Plan) *crew {
	gittest.Isolate(t)
	x := &crew{t: t, now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), repo: t.TempDir(), home: t.TempDir(), machine: roomy, pl: pl}
	gittest.Git(t, x.repo, "init", "-q", "-b", "main")
	gittest.Write(t, x.repo, "a.txt", "a\n")
	gittest.Commit(t, x.repo, "first")
	var err error
	if x.proj, err = state.OpenProject(filepath.Join(x.repo, ".baton"), x.clock); err != nil {
		t.Fatal(err)
	}
	state.ExcludeFromGit(x.repo)
	x.c = x.coordinator("inst", max)
	return x
}

// coordinator makes the host instance's coordinator, with its lead bound to the run.
func (x *crew) coordinator(instance string, max int) *Coordinator {
	t := x.t
	lead, err := x.proj.Bind(state.Binding{Session: "sess-1", Instance: instance})
	if err != nil {
		t.Fatal(err)
	}
	if x.lead == nil {
		lead.WritePlan(x.pl)
		lead.Update(func(st *state.State) error {
			*st = state.Reattach(*st, x.pl, x.now, state.Origin{})
			st.RunBranch = "main"
			return nil
		})
	}
	x.lead = lead
	c := &Coordinator{Lead: &Loop{Notify: &bomb{}, Project: "demo", Timing: DefaultTiming}, Project: x.proj, Home: x.home}
	t.Cleanup(c.snapshotsTaken) // before the test's directories are removed (cleanups run last first)
	c.Fleet = &Fleet{Max: max, UsageMax: 80, Go: func(fn func()) { fn() }, Machine: func() machine.Reading { return x.machine },
		Launch: Launch{Args: []string{"--model", "haiku"}, Autocompact: "810k", Settings: `{"hooks":{}}`, Bin: "/opt/baton", Version: "v0.5.0",
			Env: []string{"HOME=/home/x", "PWD=/checkout", "BATON_DIR=/stale"}, BatonEnv: []string{"BATON_ESCALATION_TIMEOUT=5m0s", "BATON_TTY=/dev/ttys001"}},
		Spawn: func(spec host.Spec) (Hidden, error) {
			if x.spawnErr != nil {
				return nil, x.spawnErr
			}
			// Before claude starts, its sub-run is on the roster and linked to its session and instance, so
			// that its first hook finds it.
			st, _ := lead.Load()
			sub, ok := x.proj.Lookup("", spec.Instance)
			if !ok || len(st.Workers) == 0 || x.proj.RunOf(st.Workers[len(st.Workers)-1].Session) != sub.Ref || st.Workers[len(st.Workers)-1].Instance != spec.Instance {
				t.Errorf("%s launched before its roster entry and links were written: %+v", spec.Name, st.Workers)
			}
			f := newFake()
			x.specs, x.sessions = append(x.specs, spec), append(x.sessions, f)
			return f, nil
		}}
	return c
}

// snapshotsTaken waits for the snapshots the lead's and the workers' Loops take off the tick, when a
// session halts, so that none writes into a test's directories once the test is over.
func (c *Coordinator) snapshotsTaken() {
	c.mu.Lock()
	loops := []*Loop{c.Lead}
	for _, w := range c.workers {
		loops = append(loops, w.loop)
	}
	c.mu.Unlock()
	for _, l := range loops {
		l.saving.Wait()
	}
}

// tick advances the clock by d, then ticks the lead and every worker still running, as their sessions
// would. The lead's session is at work (its hooks run), unless the test says otherwise.
func (x *crew) tick(d time.Duration) {
	x.advance(d)
	if !x.leadIdle {
		x.lead.Update(func(st *state.State) error { st.Run.LastActivity = x.now; return nil })
	}
	view := func(s *state.Store) host.View {
		return host.View{Now: x.now, Store: s, Owner: s != nil, LastOutput: x.now.Add(-time.Hour), LastHumanKey: x.now.Add(-time.Hour)}
	}
	x.c.Tick(view(x.lead), &typed{})
	for i, spec := range x.specs {
		select {
		case <-x.sessions[i].done:
			continue
		default:
		}
		sub, _ := x.proj.Lookup("", spec.Instance)
		if x.typedTo == nil {
			x.typedTo = map[string]*typed{}
		}
		if x.typedTo[spec.Name] == nil {
			x.typedTo[spec.Name] = &typed{}
		}
		spec.Controller.Tick(view(sub), x.typedTo[spec.Name])
	}
}

func (x *crew) clock() time.Time {
	x.nowMu.Lock()
	defer x.nowMu.Unlock()
	return x.now
}

func (x *crew) advance(d time.Duration) {
	x.nowMu.Lock()
	defer x.nowMu.Unlock()
	x.now = x.now.Add(d)
}

func (x *crew) state() state.State {
	st, err := x.lead.Load()
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

func (x *crew) sub(phase string) *state.Store {
	s, err := x.proj.ExistingRun(state.WorkerRef("sess-1", phase), "")
	if err != nil {
		x.t.Fatal(err)
	}
	return s
}

// started makes worker phase's session send its first prompt.
func (x *crew) started(phase string) {
	x.sub(phase).Update(func(st *state.State) error { st.Run.TurnStarted = x.now; return nil })
}

// last is the latest session started, and its spec.
func (x *crew) last() (host.Spec, *fake) {
	if len(x.specs) == 0 {
		x.t.Fatal("no worker's session was started")
	}
	return x.specs[len(x.specs)-1], x.sessions[len(x.sessions)-1]
}

func (x *crew) events() string {
	b, _ := os.ReadFile(filepath.Join(x.lead.Dir, "events.jsonl"))
	return string(b)
}

func (x *crew) worker(phase string) state.Worker {
	for _, w := range slices.Backward(x.state().Workers) {
		if w.Phase == phase {
			return w
		}
	}
	x.t.Fatalf("no worker on %s: %+v", phase, x.state().Workers)
	return state.Worker{}
}

// A worker starts on a phase that can go ahead beside the lead's, while a slot is free: in a worktree
// of its own outside the checkout, on a branch made from the run's, with its roster entry and its
// sub-run written first, and the lead's own flags.
func TestAWorkerTakesUpAPhaseThatCanStart(t *testing.T) {
	x := newCrew(t, 1)
	x.tick(time.Second)
	spec, _ := x.last()
	w := x.worker("P1")
	st := x.state()
	if len(x.specs) != 1 || w.Status != state.WorkerStarting || st.Phases["P1"].Status != state.PhaseWorking || st.Phases["P1"].Worker != "sess-1/P1" {
		t.Fatalf("spawned %d; roster %+v; P1 %+v", len(x.specs), st.Workers, st.Phases["P1"])
	}
	real, _ := filepath.EvalSymlinks(x.home)
	if want := filepath.Join(real, "worktrees", lane.Key(x.repo), "sess-1", "P1"); spec.Dir != w.Worktree || w.Worktree != want {
		t.Errorf("worktree %q (spec %q), want %q", w.Worktree, spec.Dir, want)
	}
	if strings.HasPrefix(w.Worktree, x.repo) || gitx.Branch(w.Worktree) != "baton/sess-1/P1" || w.Branch != "baton/sess-1/P1" ||
		w.Base != gitx.Head(x.repo) || gitx.Head(w.Worktree) != w.Base {
		t.Errorf("worktree %s on %q at %s, base %s", w.Worktree, gitx.Branch(w.Worktree), gitx.Head(w.Worktree), w.Base)
	}
	if spec.Name != "P1" || spec.Instance != "inst-P1" || w.Instance != "inst-P1" {
		t.Errorf("spec %+v", spec)
	}
	want := []string{"--autocompact", "810k", "--settings", `{"hooks":{}}`, "--model", "haiku", "--session-id", w.Session, "--", decide.WorkerBegin("P1")}
	if !slices.Equal(spec.Args, want) {
		t.Errorf("args %q, want %q", spec.Args, want)
	}
	if x.proj.RunOf(w.Session) != "sess-1/P1" || x.proj.HostRun("inst-P1") != "sess-1/P1" {
		t.Errorf("the worker's session and instance are not linked to its sub-run")
	}
	sub := x.sub("P1")
	sst, _ := sub.Load()
	spl, _ := sub.LoadPlan()
	if sst.Current != "P1" || sst.IDPrefix != "P1." || sst.Mode != state.ModeRunning || len(spl.Phases) != 1 || spl.Phases[0].ID != "P1" || sub.Root != w.Worktree {
		t.Errorf("the sub-run: %+v, plan %+v, root %s", sst, spl.Phases, sub.Root)
	}
	if ev := x.events(); !strings.Contains(ev, `"kind":"worker_spawned"`) || !strings.Contains(ev, `"branch":"baton/sess-1/P1"`) {
		t.Errorf("events:\n%s", ev)
	}
	if ann := strings.Join(st.Run.Announce, "\n"); !strings.Contains(ann, "a worker took up P1 (one)") || !strings.Contains(ann, "baton view P1") {
		t.Errorf("announced %q", ann)
	}

	// One slot: the next phase waits for it. The session starting is noticed.
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	if w := x.worker("P1"); len(x.specs) != 1 || w.Status != state.WorkerWorking || w.Started.IsZero() {
		t.Errorf("spawned %d; P1's worker %+v", len(x.specs), w)
	}
	if ev := x.events(); !strings.Contains(ev, `"kind":"worker_started"`) || !strings.Contains(strings.Join(x.state().Run.Announce, "\n"), "the worker on P1 is at work") {
		t.Errorf("not announced: %s", ev)
	}
}

// With BATON_SCREEN_DUMP=1, a worker's session keeps the latest of its screen in its sub-run as it ends,
// for debugging; otherwise nowhere.
func TestAWorkersScreenIsKeptOnlyWhenAsked(t *testing.T) {
	for _, keep := range []bool{false, true} {
		x := newCrew(t, 1)
		x.c.Fleet.Launch.ScreenDump = keep
		x.tick(time.Second)
		spec, _ := x.last()
		want := ""
		if keep {
			want = filepath.Join(x.sub("P1").Dir, "screen.log")
		}
		if spec.Dump != want {
			t.Errorf("ScreenDump %v: the screen goes to %q, want %q", keep, spec.Dump, want)
		}
	}
}

func TestWhenAWorkerStarts(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	pl := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0", After: []string{}}, {ID: "P1", After: []string{}}, {ID: "P2", After: []string{}}, {ID: "P3", After: []string{"P1"}}}}
	chain := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0"}, {ID: "P1"}}}
	on := func(st *state.State, phase, status string) {
		st.Phases[phase] = &state.PhaseState{Status: state.PhaseWorking, Worker: "r/" + phase}
		st.Workers = append(st.Workers, state.Worker{Phase: phase, Ref: "r/" + phase, Session: phase, Status: status, Generation: st.Generation})
	}
	const busyWhy, laneWhy = "the machine is busy (load 15.0 on 14 CPUs)", "a gate waits for a lane"
	for _, c := range []struct {
		name  string
		f     Fleet
		room  room
		pl    plan.Plan
		st    func(*state.State)
		phase string // the phase that waits for a worker, or gets one
		why   string // why it gets none
		held  string // what holds it back
	}{
		{"a free slot, and a phase that can start beside the lead's", Fleet{Max: 1}, room{}, pl, nil, "P1", "", ""},
		{"never the lead's phase", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Phases["P0"].Status, st.Current = state.PhaseDone, "P1"
			st.Phases["P1"].Status = state.PhaseActive
		}, "P2", "", ""},
		{"nor the phase the idle lead takes up next", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Phases["P0"].Status, st.Current = state.PhaseDone, ""
		}, "P2", "", ""},
		{"every slot taken", Fleet{Max: 1}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "", "1 of 1 workers run already", ""},
		{"a suspended worker keeps its slot", Fleet{Max: 1}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerSuspended) }, "", "1 of 1 workers run already", ""},
		{"a worker of a replaced plan keeps none", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Generation++
		}, "P2", "", ""},
		{"never two workers on one phase", Fleet{Max: 2}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "P2", "", ""},
		{"a failed worker frees its slot", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Workers = append(st.Workers, state.Worker{Phase: "P1", Ref: "r/P1", Status: state.WorkerFailed})
		}, "P1", "", ""},
		{"a phase waits for one a worker has not landed", Fleet{Max: 3}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			on(st, "P2", state.WorkerLanding)
		}, "", "no other phase can start", ""},
		{"a chain", Fleet{Max: 1}, room{}, chain, nil, "", "no other phase can start", ""},
		{"paused", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.Mode = state.ModePaused }, "", "the run is paused", ""},
		{"blocked by the lead", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.Blocked = &state.Block{Reason: "x"} }, "", "the run is blocked", ""},
		{"a review due", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.ReviewDue = "P0" }, "", "the run waits on a review of its decisions", ""},
		{"attached on a detached HEAD", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.RunBranch = "" }, "", "the plan was attached with no branch checked out", ""},
		{"usage over the max", Fleet{Max: 1, UsageMax: 80}, room{}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85, Resets: now.Add(time.Hour)}
		}, "P1", "85% of the five-hour usage is used (worker_usage_max 80%)", state.HoldUsage},
		{"usage over the max, past its reset", Fleet{Max: 1, UsageMax: 80}, room{}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85, Resets: now.Add(-time.Minute)}
		}, "P1", "", ""},
		{"usage under the max", Fleet{Max: 1, UsageMax: 80}, room{}, pl, func(st *state.State) { st.Usage = &state.Usage{UsedPct: 79} }, "P1", "", ""},
		{"no usage check, the default", Fleet{Max: 1}, room{}, pl, func(st *state.State) { st.Usage = &state.Usage{UsedPct: 99} }, "P1", "", ""},
		{"a session waits on a usage limit", Fleet{Max: 1}, room{}, pl, func(st *state.State) {
			st.Usage = &state.Usage{Limited: now.Add(time.Minute)}
		}, "", "a session waits on a usage limit", ""},

		// workers: auto, and the machine.
		{"auto, under what the machine allows", Fleet{Max: 6, Auto: true}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "P2", "", ""},
		{"auto, at what the machine allows", Fleet{Max: 1, Auto: true}, room{}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "", "1 of 1 workers run already", ""},
		{"no room on the machine", Fleet{Max: 6, Auto: true}, room{busy: busyWhy}, pl, nil, "P1", busyWhy, state.HoldBusy},
		{"no room on the machine, with a number", Fleet{Max: 3}, room{busy: "memory is low (9% free)"}, pl, nil, "P1", "memory is low (9% free)", state.HoldBusy},
		{"the machine before the usage", Fleet{Max: 1, UsageMax: 80}, room{busy: busyWhy}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85}
		}, "P1", busyWhy, state.HoldBusy},
		{"no room, and no phase waits", Fleet{Max: 1}, room{busy: busyWhy}, chain, nil, "", "no other phase can start", ""},
		{"no room, and every slot taken", Fleet{Max: 1}, room{busy: busyWhy}, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "", "1 of 1 workers run already", ""},
		{"auto: the ramp after a worker started", Fleet{Max: 6, Auto: true}, room{started: now.Add(-20 * time.Second)}, pl, nil,
			"P1", "a worker started 20s ago, and with workers: auto the next starts 1m after it", ""},
		{"auto: the ramp is over", Fleet{Max: 6, Auto: true}, room{started: now.Add(-time.Minute)}, pl, nil, "P1", "", ""},
		{"auto: a ramp of its own", Fleet{Max: 6, Auto: true, Ramp: 5 * time.Second}, room{started: now.Add(-5 * time.Second)}, pl, nil, "P1", "", ""},
		{"no ramp with a number", Fleet{Max: 6}, room{started: now.Add(-time.Second)}, pl, nil, "P1", "", ""},

		// The lanes, and the landings.
		{"a gate waits for a lane", Fleet{Max: 6, Auto: true}, room{lanes: laneWhy}, pl, nil, "P1", laneWhy, state.HoldLanes},
		{"the machine before the lanes", Fleet{Max: 6}, room{busy: busyWhy, lanes: laneWhy}, pl, nil, "P1", busyWhy, state.HoldBusy},
		{"the lanes before the usage", Fleet{Max: 6, UsageMax: 80}, room{lanes: laneWhy}, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85}
		}, "P1", laneWhy, state.HoldLanes},
		{"two landings in a row conflicted", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 2
		}, "P2", "2 landings in a row conflicted", state.HoldConflicts},
		{"three", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 3
		}, "P2", "3 landings in a row conflicted", state.HoldConflicts},
		{"one conflicted", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 1
		}, "P2", "", ""},
		{"conflicts, and no worker left to land", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			st.Workers = append(st.Workers, state.Worker{Phase: "P1", Ref: "r/P1", Status: state.WorkerFailed})
			st.Conflicts = 2
		}, "P1", "", ""},
		{"conflicts, and only a replaced plan's worker left", Fleet{Max: 6}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Generation++
			st.Conflicts = 2
		}, "P2", "", ""},
		{"the lanes before the conflicts", Fleet{Max: 6}, room{lanes: laneWhy}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts = 2
		}, "P2", laneWhy, state.HoldLanes},
		{"the conflicts before the usage", Fleet{Max: 6, UsageMax: 80}, room{}, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			st.Conflicts, st.Usage = 2, &state.Usage{UsedPct: 85}
		}, "P2", "2 landings in a row conflicted", state.HoldConflicts},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := state.Attach(c.pl, now, state.Origin{})
			st.RunBranch = "main"
			if c.st != nil {
				c.st(&st)
			}
			phase, why, held := c.f.next(st, c.pl, now, c.room)
			if phase != c.phase || why != c.why || held != c.held {
				t.Errorf("next = %q, %q, %q; want %q, %q, %q", phase, why, held, c.phase, c.why, c.held)
			}
		})
	}
}

// The machine has room for another session while its load is under its CPUs and its memory is not short:
// on macOS by the kernel's pressure and how fast swap grows, never by its free share, which macOS holds
// near 36% by swapping; on Linux by what is available. With no live readings, it has room.
func TestRoomOnTheMachine(t *testing.T) {
	mac := roomy
	linux := machine.Reading{CPUs: 8, MemTotal: 16 << 30, MemAvailablePct: 40, Load1: 2, Known: true}
	with := func(m machine.Reading, fn func(*machine.Reading)) machine.Reading { fn(&m); return m }
	for _, c := range []struct {
		name string
		m    machine.Reading
		grew uint64
		want string
	}{
		{"a roomy Mac", mac, 0, ""},
		{"a roomy Linux machine", linux, 0, ""},
		{"no live readings", machine.Reading{CPUs: 8, Load1: 99}, 0, ""},
		{"a load just under the CPUs", with(linux, func(m *machine.Reading) { m.Load1 = 7.9 }), 0, ""},
		{"a load at the CPUs", with(linux, func(m *machine.Reading) { m.Load1 = 8 }), 0, "the machine is busy (load 8.0 on 8 CPUs)"},
		{"a busy Mac", with(mac, func(m *machine.Reading) { m.Load1 = 15.04 }), 0, "the machine is busy (load 15.0 on 14 CPUs)"},
		{"macOS's free share is no guide", with(mac, func(m *machine.Reading) { m.MemAvailablePct = 5 }), 0, ""},
		{"memory pressure critical", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureCritical }), 0, "memory pressure is critical"},
		{"pressure at warn, swap steady", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureWarn }), 256 << 20, ""},
		{"pressure at warn, swapping", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureWarn }), 1288 << 20,
			"the machine is swapping (1.3 GiB in the last minute)"},
		{"pressure at warn, swapping a little", with(mac, func(m *machine.Reading) { m.Pressure = machine.PressureWarn }), 300 << 20,
			"the machine is swapping (300 MiB in the last minute)"},
		{"swapping at normal pressure", mac, 2 << 30, ""},
		{"Linux at the floor", with(linux, func(m *machine.Reading) { m.MemAvailablePct = 15 }), 0, ""},
		{"Linux under it", with(linux, func(m *machine.Reading) { m.MemAvailablePct = 14 }), 0, "memory is low (14% free)"},
	} {
		if got := busy(c.m, c.grew); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// No worker starts before the lead's session has: until then, it may be showing Claude Code's trust
// dialog, and so would the worker.
func TestNoWorkerStartsBeforeTheLead(t *testing.T) {
	x := newCrew(t, 1)
	x.leadIdle = true
	x.lead.Update(func(st *state.State) error { st.Run.LastActivity = x.now.Add(-time.Hour); return nil })
	x.tick(time.Second)
	x.tick(time.Minute)
	if len(x.specs) != 0 {
		t.Fatal("a worker started before the lead's session")
	}
	x.leadIdle = false
	x.tick(time.Second)
	if len(x.specs) != 1 {
		t.Errorf("%d workers once the lead started", len(x.specs))
	}
}

// A worker's session that never sends its first prompt is ended without a key typed into it, and its
// phase goes back to the scheduler. A trust screen stops workers in this host for good: baton never
// answers it.
func TestAWorkerThatNeverStartsIsEnded(t *testing.T) {
	for _, c := range []struct {
		name, tail, why string
		off             bool
	}{
		{"a slow start", "\x1b[2J\x1b[Hloading…", "its session did not start within 30s", false},
		{"a trust screen", "\x1b[2J\x1b[1;1HDo\x1b[1Cyou trust the files in this\r\nfolder?\x1b[0m", "Claude Code asked whether to trust its worktree", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			x.tick(time.Second)
			spec, f := x.last()
			f.tail = c.tail
			x.tick(29 * time.Second)
			if f.quit != 0 {
				t.Fatal("ended before its 30 seconds")
			}
			x.tick(2 * time.Second)
			if f.quit != 1 || f.ended != 0 {
				t.Fatalf("quit %d, ended %d: want it quit, nothing typed", f.quit, f.ended)
			}
			f.exit(143)
			x.tick(time.Second)
			st := x.state()
			w := st.Workers[0]
			if w.Status != state.WorkerFailed || w.Why != c.why || st.Phases["P1"].Status != state.PhasePending || isDir(spec.Dir) ||
				gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") != "" {
				t.Errorf("worker %+v; P1 %+v; worktree there %v", w, st.Phases["P1"], isDir(spec.Dir))
			}
			ev := x.events()
			if !strings.Contains(ev, `"kind":"worker_ended"`) || strings.Contains(ev, `"kind":"workers_off"`) != c.off || strings.Contains(ev, `"notice":"workers"`) != c.off {
				t.Errorf("events:\n%s", ev)
			}
			if c.off {
				x.tick(time.Hour)
				if len(x.specs) != 1 {
					t.Error("a worker started after a trust screen")
				}
				return
			}
			// The phase went back, and a new worker takes it up a while later, under a session of its own.
			retry := x.now.Add(launchRetry).Local().Format("15:04")
			x.tick(time.Minute)
			if len(x.specs) != 1 {
				t.Fatal("a new worker started at once")
			}
			if ann := strings.Join(x.state().Run.Announce, "\n"); !strings.Contains(ann, "baton: no worker on P1 for now ("+c.why+") → baton tries again at "+retry) {
				t.Errorf("announced %q", ann)
			}
			x.tick(launchRetry)
			if len(x.specs) != 2 || x.worker("P1").Status != state.WorkerStarting || x.worker("P1").Session == w.Session {
				t.Errorf("%d sessions; roster %+v", len(x.specs), x.state().Workers)
			}
			// A second in a row that never starts stops workers in this host.
			_, f = x.last()
			x.tick(31 * time.Second)
			f.exit(143)
			x.tick(time.Second)
			x.tick(launchRetry)
			if ev := x.events(); len(x.specs) != 2 || !strings.Contains(ev, `"kind":"workers_off"`) {
				t.Errorf("%d sessions; events:\n%s", len(x.specs), ev)
			}
		})
	}
}

// A worker whose session ends by itself is resumed in its worktree, the same conversation, twice at
// most. The third time, its phase goes back to the scheduler, and its branch keeps its commits.
func TestAnUnexpectedExitIsResumed(t *testing.T) {
	x := newCrew(t, 1)
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	first := x.worker("P1")
	gittest.Write(t, first.Worktree, "w.txt", "the worker's\n")
	gittest.Commit(t, first.Worktree, "the worker's commit")
	for n := 1; n <= MaxResumes; n++ {
		_, f := x.last()
		f.exit(1)
		x.tick(time.Second)
		spec, _ := x.last()
		w := x.worker("P1")
		want := []string{"--resume", first.Session, "--", decide.WorkerResume("P1", "it ended unexpectedly (exit code 1)")}
		if len(x.specs) != n+1 || spec.Dir != first.Worktree || !slices.Equal(spec.Args[len(spec.Args)-4:], want) {
			t.Fatalf("resume %d: %d sessions, dir %s, args %q", n, len(x.specs), spec.Dir, spec.Args)
		}
		if w.Resumes != n || w.Session != first.Session || w.Status != state.WorkerStarting {
			t.Fatalf("resume %d: %+v", n, w)
		}
		x.started("P1")
		x.tick(time.Second)
	}
	if n := strings.Count(x.events(), `"kind":"worker_resumed"`); n != MaxResumes {
		t.Errorf("%d resumes logged", n)
	}
	_, f := x.last()
	f.exit(1)
	x.tick(time.Second)
	st := x.state()
	w := st.Workers[0]
	if w.Status != state.WorkerFailed || w.Unlanded != 1 || !strings.Contains(w.Why, "3 times") || st.Phases["P1"].Status != state.PhasePending {
		t.Errorf("worker %+v, P1 %+v", w, st.Phases["P1"])
	}
	if isDir(first.Worktree) || gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1") == "" {
		t.Error("the worktree is still there, or the branch with the worker's commit is gone")
	}
	if ann := strings.Join(st.Run.Announce, "\n"); !strings.Contains(ann, "its branch baton/sess-1/P1 keeps 1 commit that never landed") {
		t.Errorf("announced %q", ann)
	}
	if len(x.specs) != MaxResumes+1 {
		t.Errorf("a new worker started at once: %d sessions", len(x.specs))
	}
}

// A dropped run's workers are ended. Each one's uncommitted work is saved and its worktree removed, and
// a branch that holds commits that never landed is kept.
func TestDroppingTheRunEndsItsWorkers(t *testing.T) {
	x := newCrew(t, 2)
	x.tick(time.Second)
	x.tick(time.Second)
	if len(x.specs) != 2 {
		t.Fatalf("%d workers", len(x.specs))
	}
	x.started("P1")
	x.started("P2")
	x.tick(time.Second)
	p1, p2 := x.worker("P1"), x.worker("P2")
	gittest.Write(t, p1.Worktree, "one.txt", "committed\n")
	gittest.Commit(t, p1.Worktree, "P1's work")
	gittest.Write(t, p2.Worktree, "two.txt", "uncommitted\n")
	x.lead.Update(func(st *state.State) error { return state.Detach(st) })
	x.tick(time.Second)
	for i, f := range x.sessions {
		if f.ended != 1 || f.quit != 0 {
			t.Errorf("session %d: ended %d, quit %d", i, f.ended, f.quit)
		}
		f.exit(0)
	}
	x.tick(time.Second)
	st := x.state()
	for _, w := range st.Workers {
		if w.Status != state.WorkerFailed || w.Why != "the run was dropped" || isDir(w.Worktree) {
			t.Errorf("worker %+v; worktree there %v", w, isDir(w.Worktree))
		}
	}
	// The run was dropped: P1's branch, with a commit that never landed, is kept under kept/, so that no
	// worker of a later plan carries on from it.
	kept := x.worker("P1").Branch
	if x.worker("P1").Unlanded != 1 || kept != "baton/sess-1/kept/P1-20261011-1200" || gitx.RevParse(x.repo, "refs/heads/"+kept) == "" ||
		gitx.RevParse(x.repo, "refs/heads/"+p1.Branch) != "" {
		t.Errorf("P1's branch, with a commit that never landed: %q", kept)
	}
	if ann := strings.Join(x.state().Run.Announce, "\n"); !strings.Contains(ann, "its branch "+kept+" keeps 1 commit that never landed") {
		t.Errorf("announced %q", ann)
	}
	if x.worker("P2").Unlanded != 0 || gitx.RevParse(x.repo, "refs/heads/"+p2.Branch) != "" {
		t.Error("P2's branch, with nothing on it, is still there")
	}
	b, _ := os.ReadFile(filepath.Join(x.sub("P2").Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"snapshot"`) || !strings.Contains(string(b), `"files":1`) {
		t.Errorf("P2's uncommitted work was not saved:\n%s", b)
	}
	if x.proj.HostRun(p1.Instance) != "" || x.proj.HostRun(p2.Instance) != "" {
		t.Error("the workers' instances still drive their sub-runs")
	}
	if n := strings.Count(x.events(), `"kind":"worker_ended"`); n != 2 {
		t.Errorf("%d workers ended:\n%s", n, x.events())
	}
	x.tick(time.Minute)
	if len(x.specs) != 2 {
		t.Error("a worker started on a dropped run")
	}
}

// A phase the plan no longer has takes its worker with it.
func TestARemovedPhaseCancelsItsWorker(t *testing.T) {
	x := newCrew(t, 1)
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	revised := x.pl
	revised.Phases = []plan.Phase{x.pl.Phases[0], x.pl.Phases[2]}
	x.lead.WritePlan(revised)
	x.lead.Update(func(st *state.State) error { state.Revise(st, revised, x.now); return nil })
	x.tick(time.Second)
	_, f := x.last()
	if f.ended != 1 {
		t.Fatalf("ended %d", f.ended)
	}
	f.exit(0)
	x.tick(time.Second)
	if w := x.state().Workers[0]; w.Status != state.WorkerFailed || w.Why != "its phase is no longer in the plan" {
		t.Errorf("worker %+v", w)
	}
}

// A worker whose session ended with its host is suspended, and the next host for the run resumes it in
// its worktree: the same conversation, under the new host's instance. One that never started starts
// anew.
func TestTheNextHostResumesSuspendedWorkers(t *testing.T) {
	for _, c := range []struct {
		name    string
		started bool
	}{{"a worker at work", true}, {"a worker that never started", false}} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			x.tick(time.Second)
			if c.started {
				x.started("P1")
				x.tick(time.Second)
			}
			before := x.worker("P1")
			_, f := x.last()
			f.exit(0) // the host ended it
			suspended := x.c.Ended("the baton session ended")
			if len(suspended) != 1 || suspended[0].Instance != "inst-P1" {
				t.Fatalf("Ended returned %+v", suspended)
			}
			if w := x.worker("P1"); w.Status != state.WorkerSuspended || w.Why != "the baton session ended" || x.state().Phases["P1"].Status != state.PhaseWorking {
				t.Fatalf("worker %+v", w)
			}
			x.proj.Unbind("inst-P1")
			x.proj.Unbind("inst")

			x.c = x.coordinator("next", 1)
			x.tick(time.Second)
			spec, _ := x.last()
			w := x.worker("P1")
			if len(x.specs) != 2 || spec.Dir != before.Worktree || spec.Instance != "next-P1" || w.Instance != "next-P1" || w.Status != state.WorkerStarting || w.Resumes != 0 {
				t.Fatalf("%d sessions; spec %+v; worker %+v", len(x.specs), spec, w)
			}
			want := []string{"--resume", before.Session, "--", decide.WorkerResume("P1", "the baton session ended")}
			if !c.started {
				want = []string{"--session-id", w.Session, "--", decide.WorkerBegin("P1")}
				if w.Session == before.Session {
					t.Error("a session that never started was started again under its id")
				}
			}
			if !slices.Equal(spec.Args[len(spec.Args)-4:], want) {
				t.Errorf("args %q, want them to end %q", spec.Args, want)
			}
			if x.proj.HostRun("next-P1") != "sess-1/P1" || x.proj.RunOf(w.Session) != "sess-1/P1" {
				t.Error("the resumed worker is not linked to its sub-run")
			}
			if !strings.Contains(x.events(), `"kind":"worker_resumed"`) {
				t.Errorf("events:\n%s", x.events())
			}
		})
	}
}

// A worker launched as the host ends never starts: it is suspended for the next host, and no other
// starts in this one.
func TestAWorkerLaunchedAsTheHostEndsIsSuspended(t *testing.T) {
	x := newCrew(t, 2)
	x.spawnErr = host.ErrClosed
	x.tick(time.Second)
	x.tick(time.Second)
	st := x.state()
	if len(st.Workers) != 1 || st.Workers[0].Status != state.WorkerSuspended || st.Phases["P1"].Status != state.PhaseWorking {
		t.Errorf("roster %+v", st.Workers)
	}
	if !isDir(st.Workers[0].Worktree) {
		t.Error("its worktree is gone")
	}
	x.spawnErr = errors.New("no pty")
	x.tick(time.Second)
	if len(x.state().Workers) != 1 {
		t.Errorf("another worker was launched: %+v", x.state().Workers)
	}
}

// A worker's session is started with the lead's flags, its own session id, and its prompt after "--";
// its environment is the lead's, without anything naming the lead's session, terminal or directory.
func TestAWorkersCommandAndEnvironment(t *testing.T) {
	l := Launch{Args: []string{"--model", "opus", "--permission-mode", "acceptEdits"}, Settings: "{}", Bin: "/b/baton", Version: "v1",
		Env:      []string{"HOME=/h", "PWD=/checkout", "OLDPWD=/x", "BATON_INSTANCE=lead", "PATH=/bin", "CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS=120000"},
		BatonEnv: []string{"BATON_WARN_PCT=90", "BATON_TTY=/dev/ttys004", "BATON_PENDING=run", "BATON_HOME=/bh", "BATON_INSTANCE=lead"}}
	if got, want := l.args("u-1", false, "go"), []string{"--settings", "{}", "--model", "opus", "--permission-mode", "acceptEdits", "--session-id", "u-1", "--", "go"}; !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
	l.Autocompact = "500k"
	if got, want := l.args("u-1", true, "on"), []string{"--autocompact", "500k", "--settings", "{}", "--model", "opus", "--permission-mode", "acceptEdits", "--resume", "u-1", "--", "on"}; !slices.Equal(got, want) {
		t.Errorf("resume args %q, want %q", got, want)
	}
	env := l.env(state.Worker{Phase: "P3", Instance: "lead-P3", Worktree: "/wt/P3"}, "run", "/proj/.baton", "/proj")
	want := []string{"HOME=/h", "PATH=/bin", "CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS=120000", "BATON_WARN_PCT=90", "BATON_HOME=/bh",
		"BATON_HOST=1", "BATON_INSTANCE=lead-P3", "BATON_BIN=/b/baton", "BATON_VERSION=v1", "BATON_DIR=/proj/.baton",
		"BATON_ROLE=worker", "BATON_PHASE=P3", "BATON_RUN=run", "BATON_WORKTREE=/wt/P3", "BATON_CHECKOUT=/proj"}
	if !slices.Equal(env, want) {
		t.Errorf("env:\n%q\nwant:\n%q", env, want)
	}
}

func TestTrustScreen(t *testing.T) {
	for tail, want := range map[string]bool{
		"Do you trust the files in this folder?":                                 true,
		"\x1b[1mDo\x1b[22m\x1b[1Cyou\x1b[1Ctrust\r\nthe files":                   true,
		"Quick safety check: Is this a project you created or one you trust?":    false,
		"\x1b]0;claude\x07 Yes, I trust this\x1b[2C folder":                      true,
		"> Begin P1 — the brief above has your instructions.\r\n⏺ Reading files": false,
	} {
		if got := trustScreen([]byte(tail)); got != want {
			t.Errorf("trustScreen(%q) = %v, want %v", tail, got, want)
		}
	}
}

// The work on the workers runs off the lead's tick, one step at a time, and the host waits for the step
// under way before it suspends them.
func TestTheWorkersAreLookedAfterOffTheLeadsTick(t *testing.T) {
	x := newCrew(t, 2)
	var mu sync.Mutex
	var started []string
	spawn := x.c.Fleet.Spawn
	x.c.Fleet.Go = nil
	x.c.Fleet.Spawn = func(spec host.Spec) (Hidden, error) {
		mu.Lock()
		defer mu.Unlock()
		started = append(started, spec.Name)
		return spawn(spec)
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(started) }
	for i := 0; i < 50 && count() < 2; i++ {
		x.advance(time.Second)
		x.lead.Update(func(st *state.State) error { st.Run.LastActivity = x.now; return nil })
		x.c.Tick(host.View{Now: x.now, Store: x.lead, Owner: true, LastOutput: x.now.Add(-time.Hour), LastHumanKey: x.now.Add(-time.Hour)}, &typed{})
		time.Sleep(20 * time.Millisecond)
	}
	if !eventually(func() bool { return count() == 2 }) {
		t.Fatalf("started %v", started)
	}
	mu.Lock()
	for _, f := range x.sessions {
		f.exit(0)
	}
	mu.Unlock()
	if got := x.c.Ended("the baton session ended"); len(got) != 2 {
		t.Errorf("suspended %+v", got)
	}
	for _, w := range x.state().Workers {
		if w.Status != state.WorkerSuspended {
			t.Errorf("worker %+v", w)
		}
	}
}

// A suspended worker has no session to end: when its run is dropped, the next host saves its uncommitted
// work itself, then removes its worktree.
func TestASuspendedWorkerOfADroppedRunIsCleanedUp(t *testing.T) {
	x := newCrew(t, 1)
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	w := x.worker("P1")
	gittest.Write(t, w.Worktree, "draft.txt", "not committed\n")
	_, f := x.last()
	f.exit(0)
	x.c.Ended("the baton session ended")
	x.proj.Unbind(w.Instance)
	x.proj.Unbind("inst")
	x.lead.Update(func(st *state.State) error { return state.Detach(st) })

	x.c = x.coordinator("next", 1)
	x.tick(time.Second)
	if len(x.specs) != 1 {
		t.Fatalf("a worker of a dropped run was resumed")
	}
	if got := x.worker("P1"); got.Status != state.WorkerFailed || got.Why != "the run was dropped" || isDir(w.Worktree) {
		t.Errorf("worker %+v; worktree there %v", got, isDir(w.Worktree))
	}
	b, _ := os.ReadFile(filepath.Join(x.sub("P1").Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"snapshot"`) || !strings.Contains(string(b), `"why":"the run was dropped"`) {
		t.Errorf("its uncommitted work was not saved:\n%s", b)
	}
}
