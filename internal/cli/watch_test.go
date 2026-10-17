package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

var watchPlan = plan.Plan{Version: 1, Title: "Gates that report", File: "/plans/gates.md", Phases: []plan.Phase{
	{ID: "P1", Title: "The protocol", After: []string{}},
	{ID: "P2", Title: "The runner", After: []string{}},
	{ID: "P3", Title: "Look-ins", After: []string{}},
}}

// watchFixture is a project with a live run, r1, of watchPlan: the lead on P1 with a gate running and one
// queued, a phase agent on P2 whose gate waits for a lane, P3's work waiting to land, and events. Beside it
// is a run no host drives, r0. It returns the project and the run.
func watchFixture(t *testing.T, now time.Time) (*state.Project, *state.Store) {
	t.Helper()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	p, err := state.OpenProject(filepath.Join(t.TempDir(), ".baton"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	old, _ := p.Run("r0", "gone")
	old.SavePlan(plan.Plan{Version: 1, Title: "An old plan", Phases: []plan.Phase{{ID: "P0", Title: "Old", After: []string{}}}})
	old.Update(func(st *state.State) error {
		*st = state.Attach(plan.Plan{Phases: []plan.Phase{{ID: "P0"}}}, ago(3*time.Hour), state.Origin{})
		st.Owner = &state.Owner{Instance: "gone", Heartbeat: ago(time.Hour)}
		return nil
	})

	s, _ := p.Run("r1", "inst")
	if err := s.SavePlan(watchPlan); err != nil {
		t.Fatal(err)
	}
	gate := "go test ./..."
	if _, err := s.Update(func(st *state.State) error {
		*st = state.Attach(watchPlan, ago(time.Hour), state.Origin{})
		st.Owner = &state.Owner{Instance: "inst", Heartbeat: now}
		st.Current = "P1"
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: ago(30 * time.Minute)}
		st.Run.TurnOpen, st.Run.TurnStarted = true, ago(time.Minute)
		st.Phases["P2"] = &state.PhaseState{Status: state.PhaseWorking, Worker: "r1/P2"}
		st.Phases["P3"] = &state.PhaseState{Status: state.PhaseWorking, Worker: "r1/P3"}
		st.Workers = []state.Worker{
			{Phase: "P2", Ref: "r1/P2", AgentID: "agent2aaaaaa", Status: state.WorkerWorking, Assigned: ago(20 * time.Minute), Since: ago(20 * time.Minute),
				Generation: st.Generation},
			{Phase: "P3", Ref: "r1/P3", AgentID: "agent3aaaaaa", Status: state.WorkerLanding, Since: ago(6 * time.Minute), Generation: st.Generation,
				Merge: &state.Merge{Queued: ago(6 * time.Minute), Deferred: "the lead's changes to the same files", DeferredAt: ago(5 * time.Minute)}},
		}
		st.Jobs = []state.Job{
			{ID: "j1", Phase: "P1", Kind: state.JobVerify, Command: gate, Status: state.JobPassed,
				Queued: ago(time.Hour), Started: ago(time.Hour), Ran: ago(time.Hour), Ended: ago(52 * time.Minute)},
			{ID: "j2", Phase: "P1", Kind: state.JobVerify, Command: gate, Commit: "72b7740aaaa", Status: state.JobRunning, Lane: 1,
				Queued: ago(12 * time.Minute), Started: ago(12 * time.Minute), Ran: ago(10 * time.Minute), Log: s.JobLog("j2")},
			{ID: "j3", Phase: "P1", Kind: state.JobAwait, Command: "gh run watch 42", Status: state.JobQueued, Queued: ago(time.Minute)},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log := "building\n::baton stage lint start\nok\n::baton stage lint passed exit=0 seconds=12\n" +
		"::baton stage unit start\n=== RUN TestA\n--- FAIL: TestA (0.01s)\n--- FAIL: TestB (0.01s)\nstill going"
	writeFile(t, s.JobLog("j2"), log)
	os.Chtimes(s.JobLog("j2"), ago(4*time.Minute), ago(4*time.Minute))
	lane.AppendStage(s.JobStages("j2"), lane.Stage{Name: "lint", Status: lane.StagePassed, Started: ago(10 * time.Minute), Ended: ago(10*time.Minute - 12*time.Second)})

	sub, err := p.NewWorker("r1", "P2", "inst", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub.Update(func(st *state.State) error {
		w := state.AttachWorker(watchPlanState(now), "P2")
		w.Owner = st.Owner
		w.Phases["P2"].Status = state.PhaseActive
		w.Run.TurnOpen = true
		w.Jobs = []state.Job{
			{ID: "P2.j1", Phase: "P2", Kind: state.JobVerify, Command: gate, Status: state.JobPassed,
				Queued: ago(2 * time.Hour), Started: ago(2 * time.Hour), Ran: ago(2*time.Hour - time.Minute), Ended: ago(2*time.Hour - 10*time.Minute)},
			{ID: "P2.j2", Phase: "P2", Kind: state.JobVerify, Command: "make e2e", Status: state.JobRunning,
				Queued: ago(3 * time.Minute), Started: ago(3 * time.Minute), LaneWait: ago(3 * time.Minute)},
		}
		*st = w
		return nil
	})

	s.Event("phase_started", map[string]any{"phase": "P1"})
	s.Event("lock_slow", map[string]any{"caller": "somewhere"})
	s.Event("job_finished", map[string]any{"job": "j1", "phase": "P1", "status": "passed", "exit": 0, "seconds": 480})
	sub.Event("phase_started", map[string]any{"phase": "P2"})
	return p, s
}

// watchPlanState is the run's state as a phase agent's sub-run starts from it.
func watchPlanState(now time.Time) state.State {
	return state.Attach(watchPlan, now.Add(-time.Hour), state.Origin{})
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// watchNow is a moment close to the files' own times, which say whether a run's record changed lately.
func watchNow() time.Time { return time.Now().Truncate(time.Second) }

func hhmm(t time.Time) string { return t.Local().Format("15:04") }

func TestWatchShowsEachLiveRunFromItsRecords(t *testing.T) {
	now := watchNow()
	p, s := watchFixture(t, now)
	w := newWatcher(p, func() string { return "" })
	view := strings.Join(w.frame().lines(0, 0), "\n")
	for _, want := range []string{
		decide.WatchHead(now, 1),
		`"Gates that report"`,
		"P1  The protocol — the lead works on it 30m",
		"P2  The runner — agent at work 20m · agent agent2aa",
		"P3  Look-ins — waits to land: the lead's changes to the same files",
		"jobs:",
		"  j2  P1  `go test ./...` on 72b7740",
		"      running 10m in lane 1 · past the 9m its longest earlier run took · got its lane after 2m · log still 4m",
		"      stages: lint ✓ 12s · unit running",
		"      first failure, in unit: --- FAIL: TestA (0.01s)",
		"  P2.j2  P2  `make e2e`\n      waiting 3m for a lane",
		"  j3  P1  `gh run watch 42`\n      queued 1m",
		"landing (first finished, first landed):\n  1. P3 finished " + hhmm(now.Add(-6*time.Minute)) +
			" · waits to land: the lead's changes to the same files (since " + hhmm(now.Add(-5*time.Minute)) + ", 5m)",
		"lately:",
		"P1 started",
		"j1 passed in 8m",
		"P2 started",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the view lacks %q:\n%s", want, view)
		}
	}
	for _, not := range []string{"An old plan", "somewhere", "lock", "@P2", "at the lead's prompt"} {
		if strings.Contains(view, not) {
			t.Errorf("the view shows %q:\n%s", not, view)
		}
	}
	// The jobs come in the order they were queued: the lead's gate, the agent's, the wait.
	if a, b, c := strings.Index(view, "  j2 "), strings.Index(view, "  P2.j2 "), strings.Index(view, "  j3 "); !(a < b && b < c) {
		t.Errorf("jobs out of order (%d, %d, %d):\n%s", a, b, c, view)
	}

	// The next frame reads only what the log gained: the stage ended.
	f, _ := os.OpenFile(s.JobLog("j2"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n::baton stage unit failed exit=1 seconds=60\n")
	f.Close()
	view = strings.Join(w.frame().lines(0, 0), "\n")
	if strings.Contains(view, "unit running") || !strings.Contains(view, "first failure, in unit: --- FAIL: TestA") {
		t.Errorf("after the stage ended:\n%s", view)
	}
	if scan := w.logs[s.JobLog("j2")]; scan == nil || scan.stage != "" {
		t.Errorf("the log's scan: %+v", scan)
	}
}

func TestWatchTakesTheFirstFailureFromTheRunner(t *testing.T) {
	now := watchNow()
	p, s := watchFixture(t, now)
	s.Update(func(st *state.State) error {
		st.Job("j2").Failing = &state.Failing{At: now, Line: "FAIL\texample.com/pkg", Stage: "unit"}
		return nil
	})
	view := strings.Join(newWatcher(p, func() string { return "" }).frame().lines(0, 0), "\n")
	if !strings.Contains(view, "first failure, in unit: FAIL\texample.com/pkg") {
		t.Errorf("the runner's first failure is not shown:\n%s", view)
	}
}

func TestWatchToleratesARecordCaughtHalfWritten(t *testing.T) {
	now := watchNow()
	p, s := watchFixture(t, now)
	w := newWatcher(p, func() string { return "" })
	w.frame()

	stateFile := filepath.Join(s.Dir, "state.json")
	whole, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, stateFile, string(whole[:len(whole)/2]))
	planFile := s.PlanPath()
	pl, _ := os.ReadFile(planFile)
	writeFile(t, planFile, string(pl[:len(pl)/2]))

	view := strings.Join(w.frame().lines(0, 0), "\n")
	for _, want := range []string{`"Gates that report"`, "  j2  P1", decide.WatchUnread(now)} {
		if !strings.Contains(view, want) {
			t.Errorf("a half-written record: the view lacks %q:\n%s", want, view)
		}
	}

	// A view with no reading of it yet says it could not read it, and goes on.
	view = strings.Join(newWatcher(p, func() string { return "" }).frame().lines(0, 0), "\n")
	if !strings.Contains(view, "run r1: its record could not be read just now (state.json: ") || strings.Contains(view, decide.WatchNoRun) {
		t.Errorf("a half-written record and no reading:\n%s", view)
	}
}

func TestWatchFitsTheTerminal(t *testing.T) {
	now := watchNow()
	p, _ := watchFixture(t, now)
	w := newWatcher(p, func() string { return "" })
	all := w.frame().lines(0, 0)
	for _, width := range []int{20, 40, 72} {
		for _, l := range w.frame().lines(width, 0) {
			if n := len([]rune(l)); n > width {
				t.Errorf("width %d: a line of %d: %q", width, n, l)
			}
		}
	}
	// A job's lines and a landing's are broken between words rather than cut.
	narrow := strings.Join(w.frame().lines(50, 0), "\n")
	for _, want := range []string{"after 2m · log still 4m", "\n    changes to the same files (since "} {
		if !strings.Contains(narrow, want) {
			t.Errorf("width 50: the view lacks %q:\n%s", want, narrow)
		}
	}

	// Short of height, the oldest events go first, then the section's head; the rest stays whole.
	events := 0
	for _, l := range all {
		if strings.HasPrefix(l, "  ") && strings.Contains(l, " started") || strings.Contains(l, "passed in 8m") {
			events++
		}
	}
	if events != 3 {
		t.Fatalf("expected 3 events in the view:\n%s", strings.Join(all, "\n"))
	}
	for _, c := range []struct {
		height int
		want   int // the events shown
	}{{len(all) + 1, 3}, {len(all), 2}, {len(all) - 1, 1}, {len(all) - 2, 0}, {len(all) - 3, 0}} {
		got := w.frame().lines(0, c.height)
		if len(got) > c.height-1 {
			t.Errorf("height %d: %d lines", c.height, len(got))
		}
		shown := 0
		for _, l := range got {
			if strings.HasPrefix(l, "  ") && (strings.HasSuffix(l, " started") || strings.HasSuffix(l, "passed in 8m")) {
				shown++
			}
		}
		if shown != c.want {
			t.Errorf("height %d: %d events, want %d:\n%s", c.height, shown, c.want, strings.Join(got, "\n"))
		}
		if c.want > 0 && !strings.HasSuffix(got[len(got)-1], "P2 started") && !strings.HasSuffix(got[len(got)-1], "passed in 8m") {
			t.Errorf("height %d: the newest events are not kept:\n%s", c.height, strings.Join(got, "\n"))
		}
		if c.want == 0 && c.height == len(all)-2 && strings.Contains(strings.Join(got, "\n"), decide.WatchEvents) {
			t.Errorf("height %d: the events' head with no event:\n%s", c.height, strings.Join(got, "\n"))
		}
	}
}

func TestWatchOnce(t *testing.T) {
	io, out, _ := testIO("", map[string]string{"BATON_DIR": filepath.Join(t.TempDir(), ".baton")})
	if code := Main([]string{"watch", "--once"}, io); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if got := out.String(); !strings.Contains(got, decide.WatchNoRun) {
		t.Errorf("an empty project: %q", got)
	}

	now := watchNow()
	p, _ := watchFixture(t, now)
	io, out, _ = testIO("", map[string]string{"BATON_DIR": p.Dir})
	io.Now = func() time.Time { return now }
	if code := Main([]string{"watch"}, io); code != 0 { // output that is not a terminal: once
		t.Fatalf("exit %d: %s", code, out)
	}
	if got := out.String(); !strings.Contains(got, "  j2  P1  `go test ./...`") || strings.Contains(got, "\x1b[") {
		t.Errorf("watch to a file:\n%s", got)
	}
	if code := Main([]string{"watch", "now"}, io); code == 0 {
		t.Errorf("watch with an argument passed")
	}
}
