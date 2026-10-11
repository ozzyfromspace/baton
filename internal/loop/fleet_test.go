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
}

func newCrew(t *testing.T, max int) *crew {
	gittest.Isolate(t)
	x := &crew{t: t, now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), repo: t.TempDir(), home: t.TempDir()}
	gittest.Git(t, x.repo, "init", "-q", "-b", "main")
	gittest.Write(t, x.repo, "a.txt", "a\n")
	gittest.Commit(t, x.repo, "first")
	var err error
	if x.proj, err = state.OpenProject(filepath.Join(x.repo, ".baton"), x.clock); err != nil {
		t.Fatal(err)
	}
	state.ExcludeFromGit(x.repo)
	x.pl = plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}},
		{ID: "P2", Title: "two", After: []string{}}, {ID: "P3", Title: "three", After: []string{"P1"}}}}
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
	c := &Coordinator{Lead: &Loop{Notify: &bomb{}, Project: "demo", Timing: DefaultTiming}}
	c.Fleet = &Fleet{Max: max, UsageMax: 80, Home: x.home, Project: x.proj, Go: func(fn func()) { fn() },
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

func TestWhenAWorkerStarts(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	pl := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0", After: []string{}}, {ID: "P1", After: []string{}}, {ID: "P2", After: []string{}}, {ID: "P3", After: []string{"P1"}}}}
	chain := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0"}, {ID: "P1"}}}
	on := func(st *state.State, phase, status string) {
		st.Phases[phase] = &state.PhaseState{Status: state.PhaseWorking, Worker: "r/" + phase}
		st.Workers = append(st.Workers, state.Worker{Phase: phase, Ref: "r/" + phase, Session: phase, Status: status})
	}
	for _, c := range []struct {
		name  string
		max   int
		usage float64
		pl    plan.Plan
		st    func(*state.State)
		want  string // the phase, or why none
	}{
		{"a free slot, and a phase that can start beside the lead's", 1, 80, pl, nil, "P1"},
		{"never the lead's phase", 1, 80, pl, func(st *state.State) {
			st.Phases["P0"].Status, st.Current = state.PhaseDone, "P1"
			st.Phases["P1"].Status = state.PhaseActive
		}, "P2"},
		{"nor the phase the idle lead takes up next", 1, 80, pl, func(st *state.State) {
			st.Phases["P0"].Status, st.Current = state.PhaseDone, ""
		}, "P2"},
		{"every slot taken", 1, 80, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "1 of 1 workers run already"},
		{"a suspended worker keeps its slot", 1, 80, pl, func(st *state.State) { on(st, "P1", state.WorkerSuspended) }, "1 of 1 workers run already"},
		{"never two workers on one phase", 2, 80, pl, func(st *state.State) { on(st, "P1", state.WorkerWorking) }, "P2"},
		{"a failed worker frees its slot", 1, 80, pl, func(st *state.State) {
			st.Workers = append(st.Workers, state.Worker{Phase: "P1", Ref: "r/P1", Status: state.WorkerFailed})
		}, "P1"},
		{"a phase waits for one a worker has not landed", 3, 80, pl, func(st *state.State) {
			on(st, "P1", state.WorkerWorking)
			on(st, "P2", state.WorkerLanding)
		}, "no other phase can start"},
		{"a chain", 1, 80, chain, nil, "no other phase can start"},
		{"paused", 1, 80, pl, func(st *state.State) { st.Mode = state.ModePaused }, "the run is paused"},
		{"blocked by the lead", 1, 80, pl, func(st *state.State) { st.Blocked = &state.Block{Reason: "x"} }, "the run is blocked"},
		{"a review due", 1, 80, pl, func(st *state.State) { st.ReviewDue = "P0" }, "the run waits on a review of its decisions"},
		{"attached on a detached HEAD", 1, 80, pl, func(st *state.State) { st.RunBranch = "" }, "the plan was attached with no branch checked out"},
		{"usage over the max", 1, 80, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85, Resets: now.Add(time.Hour)}
		}, "85% of the five-hour usage is used (worker_usage_max 80%)"},
		{"usage over the max, past its reset", 1, 80, pl, func(st *state.State) {
			st.Usage = &state.Usage{UsedPct: 85, Resets: now.Add(-time.Minute)}
		}, "P1"},
		{"usage under the max", 1, 80, pl, func(st *state.State) { st.Usage = &state.Usage{UsedPct: 79} }, "P1"},
		{"no usage check", 1, 0, pl, func(st *state.State) { st.Usage = &state.Usage{UsedPct: 99} }, "P1"},
		{"a session waits on a usage limit", 1, 0, pl, func(st *state.State) {
			st.Usage = &state.Usage{Limited: now.Add(time.Minute)}
		}, "a session waits on a usage limit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := state.Attach(c.pl, now, state.Origin{})
			st.RunBranch = "main"
			if c.st != nil {
				c.st(&st)
			}
			phase, why := (&Fleet{Max: c.max, UsageMax: c.usage}).next(st, c.pl, now)
			if got := phase + why; got != c.want || phase != "" && why != "" {
				t.Errorf("next = %q, %q; want %q", phase, why, c.want)
			}
		})
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
	if x.worker("P1").Unlanded != 1 || gitx.RevParse(x.repo, "refs/heads/"+p1.Branch) == "" {
		t.Error("P1's branch, with a commit that never landed, is gone")
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
