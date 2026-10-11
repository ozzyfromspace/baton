package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
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
		{env, nil, 2, "usage: baton lane-run <run> <job>"},
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
