package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// laneProject is a repository with one commit and a run (sess-1) driven by host inst, holding job j1 for
// command, queued. It returns the run and the environment the host gives lane-run.
func laneProject(t *testing.T, command string) (*state.Store, map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("lanes run POSIX shell commands")
	}
	gittest.Isolate(t)
	root := t.TempDir()
	gittest.Init(t, root)
	gittest.Write(t, root, "a.txt", "a\n")
	gittest.Commit(t, root, "first")
	head := gittest.Git(t, root, "rev-parse", "HEAD")
	dir := filepath.Join(root, ".baton")
	p, err := state.OpenProject(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Bind(state.Binding{Session: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	s.Update(func(st *state.State) error {
		now := time.Now()
		st.Owner = &state.Owner{Instance: "inst", PID: os.Getpid(), Started: now, Heartbeat: now}
		st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Kind: state.JobVerify, Command: command, Commit: head, Status: state.JobQueued, Queued: now}}
		return nil
	})
	return s, map[string]string{"BATON_DIR": dir, "BATON_HOME": t.TempDir(), "BATON_INSTANCE": "inst"}
}

func laneRun(env map[string]string, args ...string) (int, string) {
	io, _, errb := testIO("", env)
	io.Now = time.Now
	code := Main(append([]string{"lane-run"}, args...), io)
	return code, errb.String()
}

func events(t *testing.T, s *state.Store) map[string]map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(s.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]map[string]any{}
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var e map[string]any
		json.Unmarshal(sc.Bytes(), &e)
		out[e["kind"].(string)] = e
	}
	return out
}

func TestLaneRunRunsAJob(t *testing.T) {
	s, env := laneProject(t, "sleep 1; echo gate output; exit 1")
	if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
		t.Fatalf("lane-run: exit %d: %s", code, errs)
	}
	st, _ := s.Load()
	j := st.Job("j1")
	if j.Status != state.JobFailed || j.Exit != 1 || j.Lane != 1 || j.PID != os.Getpid() || j.PGID == 0 || j.Log != s.JobLog("j1") {
		t.Errorf("job: %+v", *j)
	}
	if j.Started.IsZero() || j.Heartbeat.IsZero() || j.Ended.Sub(j.Started) < time.Second {
		t.Errorf("times: started %s, heartbeat %s, ended %s", j.Started, j.Heartbeat, j.Ended)
	}
	if log, _ := os.ReadFile(j.Log); !strings.Contains(string(log), "gate output") || !strings.Contains(string(log), "[baton] failed (exit 1)") {
		t.Errorf("job log:\n%s", log)
	}
	ev := events(t, s)
	if e := ev["job_started"]; e == nil || e["job"] != "j1" || e["phase"] != "P1" || e["type"] != "verify" {
		t.Errorf("job_started: %v", e)
	}
	if e := ev["job_finished"]; e == nil || e["status"] != "failed" || e["exit"] != 1.0 || e["lane"] != 1.0 || e["seconds"].(float64) < 1 {
		t.Errorf("job_finished: %v", e)
	}
	if got := st.Run.Announce; len(got) != 1 || got[0] != "baton: job j1 started — `sleep 1; echo gate output; exit 1` (P1)" {
		t.Errorf("announced %q", got)
	}
}

// A gate run --here runs in the checkout itself, on what is checked out there, with no lane: nothing is
// checked out for it, and no lane is made.
func TestLaneRunRunsAGateHere(t *testing.T) {
	s, env := laneProject(t, "pwd -P > where.txt; test -f a.txt")
	s.Update(func(st *state.State) error {
		st.Jobs[0].Here = true
		return nil
	})
	if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
		t.Fatalf("lane-run: exit %d: %s", code, errs)
	}
	st, _ := s.Load()
	j := st.Job("j1")
	if j.Status != state.JobPassed || j.Lane != 0 {
		t.Errorf("job: %+v", *j)
	}
	checkout, _ := filepath.EvalSymlinks(s.Checkout)
	if b, _ := os.ReadFile(filepath.Join(s.Checkout, "where.txt")); strings.TrimSpace(string(b)) != checkout {
		t.Errorf("the gate ran in %q, not the checkout %s", b, checkout)
	}
	if lanes := (lane.Pool{Home: env["BATON_HOME"], Root: s.Checkout}).Lanes(); len(lanes) != 0 {
		t.Errorf("a gate run --here made lanes: %+v", lanes)
	}
	if log, _ := os.ReadFile(j.Log); !strings.Contains(string(log), "with no lane") {
		t.Errorf("job log:\n%s", log)
	}
	if e := events(t, s)["job_started"]; e["here"] != true {
		t.Errorf("job_started: %v", e)
	}
}

// The job of a phase set aside while it ran: the human is told it ended, since nothing else says so until
// the phase comes back.
func TestLaneRunTellsOfAParkedPhasesJob(t *testing.T) {
	s, env := laneProject(t, "exit 0")
	s.Update(func(st *state.State) error {
		st.Mode, st.Current = state.ModeRunning, "P2"
		st.Phases = map[string]*state.PhaseState{"P1": {Status: state.PhaseParked, Park: &state.Park{Reason: state.ParkJob, Job: "j1"}}, "P2": {Status: state.PhaseActive}}
		return nil
	})
	if code, errs := laneRun(env, "sess-1", "j1"); code != 0 {
		t.Fatalf("lane-run: exit %d: %s", code, errs)
	}
	st, _ := s.Load()
	if got := st.Run.Announce; len(got) != 2 || !strings.HasPrefix(got[1], "baton: job j1 (`exit 0`, P1) passed after ") || !strings.HasSuffix(got[1], " → P1 comes back once P2 is done or set aside") {
		t.Errorf("announced %q", got)
	}
}

func TestLaneRunStopsACancelledJob(t *testing.T) {
	defer func(d time.Duration) { laneTick = d }(laneTick)
	laneTick = 20 * time.Millisecond
	s, env := laneProject(t, "sleep 30")
	done := make(chan int)
	go func() {
		code, _ := laneRun(env, "sess-1", "j1")
		done <- code
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, _ := s.Load(); st.Job("j1").PGID != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Update(func(st *state.State) error { st.Job("j1").Status = state.JobInterrupted; return nil })
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("lane-run: exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lane-run kept running a cancelled job")
	}
	st, _ := s.Load()
	if j := st.Job("j1"); j.Status != state.JobInterrupted || j.Ended.IsZero() {
		t.Errorf("job: %+v", *j)
	}
	if e := events(t, s)["job_finished"]; e == nil || e["status"] != "interrupted" || !strings.Contains(e["reason"].(string), "cancelled") {
		t.Errorf("job_finished: %v", e)
	}
}

// A job that waits for a lane, every lane being in use, records since when, for the host, which holds new
// workers back while gates queue for lanes; the record goes once the job holds a lane. The wait is
// announced, with how many wait ahead of it, and logged.
func TestLaneRunRecordsItsWaitForALane(t *testing.T) {
	defer func(d time.Duration) { laneTick = d }(laneTick)
	laneTick = 20 * time.Millisecond
	s, env := laneProject(t, "exit 0")
	held, err := lane.Pool{Home: env["BATON_HOME"], Root: s.Checkout}.TryAcquire(1)
	if err != nil || held == nil {
		t.Fatalf("holding the only lane: %+v, %v", held, err)
	}
	done := make(chan int)
	go func() {
		code, _ := laneRun(env, "sess-1", "j1")
		done <- code
	}()
	before := time.Now()
	deadline := before.Add(10 * time.Second)
	for {
		st, _ := s.Load()
		if j := st.Job("j1"); !j.LaneWait.IsZero() {
			if j.Status != state.JobRunning || j.LaneWait.Before(j.Started) || j.LaneWait.After(time.Now()) {
				t.Errorf("job: %+v", *j)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never said it waits for a lane")
		}
		time.Sleep(10 * time.Millisecond)
	}
	held.Release()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("lane-run: exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lane-run never took the lane that came free")
	}
	st, _ := s.Load()
	j := st.Job("j1")
	if j.Status != state.JobPassed || j.Lane != 1 || !j.LaneWait.IsZero() {
		t.Errorf("job: %+v", *j)
	}
	if want := decide.LaneWaits(*j, 1, 0); !slices.Contains(st.Run.Announce, want) {
		t.Errorf("announced %q, want %q", st.Run.Announce, want)
	}
	if e := events(t, s)["lane_wait"]; e == nil || e["job"] != "j1" || e["phase"] != "P1" || e["slots"] != 1.0 || e["ahead"] != 0.0 || e["here"] != false {
		t.Errorf("lane_wait: %v", e)
	}
}

// A runner whose job was taken off the record stops, and leaves alone a later job that took its id.
func TestLaneRunLeavesAJobThatIsNotItsOwn(t *testing.T) {
	defer func(d time.Duration) { laneTick = d }(laneTick)
	laneTick = 20 * time.Millisecond
	s, env := laneProject(t, "sleep 30")
	done := make(chan int)
	go func() {
		code, _ := laneRun(env, "sess-1", "j1")
		done <- code
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, _ := s.Load(); st.Job("j1").PGID != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	later := state.Job{ID: "j1", Phase: "P2", Command: "true", Status: state.JobQueued, Queued: time.Now()}
	s.Update(func(st *state.State) error { st.Jobs = []state.Job{later}; return nil })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("lane-run kept running a job taken off the record")
	}
	if st, _ := s.Load(); len(st.Jobs) != 1 || st.Jobs[0].Status != state.JobQueued || st.Jobs[0].Phase != "P2" || st.Jobs[0].PID != 0 || !st.Jobs[0].Ended.IsZero() {
		t.Errorf("the later job: %+v", st.Jobs)
	}
	if e := events(t, s)["job_finished"]; e == nil || e["status"] != "interrupted" || e["phase"] != "P1" {
		t.Errorf("job_finished: %v", e)
	}
}

func TestLaneRunRefuses(t *testing.T) {
	s, env := laneProject(t, "true")
	s.Update(func(st *state.State) error {
		st.Jobs = append(st.Jobs, state.Job{ID: "j2", Phase: "P1", Status: state.JobRunning})
		return nil
	})
	noDir := map[string]string{"BATON_HOME": env["BATON_HOME"]}
	for _, c := range []struct {
		env  map[string]string
		args []string
		code int
		want string
	}{
		{env, nil, 2, "usage: baton lane-run <ref> <job>"},
		{env, []string{"sess-1"}, 2, "usage"},
		{noDir, []string{"sess-1", "j1"}, 1, "BATON_DIR"},
		{env, []string{"other", "j1"}, 1, `no run "other" here`},
		{env, []string{"sess-1", "j9"}, 1, "run sess-1 has no job j9"},
		{env, []string{"sess-1", "j2"}, 1, "job j2 is running, not queued"},
	} {
		if code, errs := laneRun(c.env, c.args...); code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("lane-run %v: exit %d %q, want %d mentioning %q", c.args, code, errs, c.code, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(env["BATON_DIR"], "runs", "other")); err == nil {
		t.Error("lane-run made a run that was not there")
	}
	if st, _ := s.Load(); st.Job("j1").Status != state.JobQueued {
		t.Errorf("a refused lane-run touched j1: %+v", *st.Job("j1"))
	}
}

func TestLaneRunIsNotInHelp(t *testing.T) {
	io, out, _ := testIO("", nil)
	Main([]string{"help"}, io)
	if strings.Contains(out.String(), "lane-run") {
		t.Errorf("help lists lane-run:\n%s", out)
	}
}

// A worker's jobs are found by its ref. An await runs in the worker's own worktree, and a verification
// in one of the project's lanes, which every session of the project shares.
func TestLaneRunRunsAWorkersJobs(t *testing.T) {
	lead, env := laneProject(t, "true")
	tree := filepath.Join(t.TempDir(), "P1")
	gittest.Git(t, lead.Checkout, "worktree", "add", "-q", "-b", "baton/sess-1/P1", tree)
	tree, _ = filepath.EvalSymlinks(tree)
	p, err := state.OpenProject(env["BATON_DIR"], time.Now)
	if err != nil {
		t.Fatal(err)
	}
	w, err := p.NewWorker(lead.Ref, "P1", "inst", tree)
	if err != nil {
		t.Fatal(err)
	}
	head := gittest.Git(t, tree, "rev-parse", "HEAD")
	w.Update(func(st *state.State) error {
		now := time.Now()
		st.Jobs = []state.Job{
			{ID: "j1", Phase: "P1", Kind: state.JobAwait, Command: "pwd -P > where.txt", Status: state.JobQueued, Queued: now},
			{ID: "j2", Phase: "P1", Kind: state.JobVerify, Command: "true", Commit: head, Status: state.JobQueued, Queued: now},
			{ID: "j3", Phase: "P1", Kind: state.JobVerify, Command: `pwd -P > here.txt; echo "$BATON_LANE" > lane.txt`, Commit: head, Here: true, Status: state.JobQueued, Queued: now},
		}
		return nil
	})
	for _, id := range []string{"j1", "j2", "j3"} {
		if code, errs := laneRun(env, w.Ref, id); code != 0 {
			t.Fatalf("lane-run %s %s: exit %d: %s", w.Ref, id, code, errs)
		}
	}
	st, _ := w.Load()
	if j := st.Job("j1"); j.Status != state.JobPassed || j.Lane != 0 || j.Log != w.JobLog("j1") {
		t.Errorf("await: %+v", *j)
	}
	if b, _ := os.ReadFile(filepath.Join(tree, "where.txt")); strings.TrimSpace(string(b)) != tree {
		t.Errorf("the await ran in %q, not the worker's worktree %s", b, tree)
	}
	if j := st.Job("j2"); j.Status != state.JobPassed || j.Lane != 1 {
		t.Errorf("verify: %+v", *j)
	}
	// A gate run --here runs in the worker's own worktree too, holding one of the project's lanes as it
	// runs, with BATON_LANE set to it.
	if j := st.Job("j3"); j.Status != state.JobPassed || j.Lane != 1 {
		t.Errorf("verify --here: %+v", *j)
	}
	if b, _ := os.ReadFile(filepath.Join(tree, "here.txt")); strings.TrimSpace(string(b)) != tree {
		t.Errorf("the gate run --here ran in %q, not the worker's worktree %s", b, tree)
	}
	if b, _ := os.ReadFile(filepath.Join(tree, "lane.txt")); strings.TrimSpace(string(b)) != "1" {
		t.Errorf("the gate run --here saw BATON_LANE %q", b)
	}
	io, _, _ := testIO("", env)
	if pool := lanePool(io, w); pool != lanePool(io, lead) || !strings.HasPrefix(pool.LaneDir(1), env["BATON_HOME"]) {
		t.Errorf("the worker's lanes %+v are not the project's %+v", pool, lanePool(io, lead))
	} else if _, err := os.Stat(pool.LaneDir(1)); err != nil {
		t.Errorf("no lane 1 in the project's pool: %v", err)
	}
	if e := events(t, w)["job_finished"]; e["worker"] != "P1" || e["phase"] != "P1" {
		t.Errorf("job_finished: %v", e)
	}
	if _, err := os.Stat(filepath.Join(lead.Dir, "jobs")); err == nil {
		t.Error("a worker's job wrote into the lead's run")
	}
}
