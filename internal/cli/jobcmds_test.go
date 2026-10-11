package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/state"
)

// gitSession is a hosted session in a git repository, with the test plan attached and owned, P0 under
// way, and a baton home of its own.
func gitSession(t *testing.T) (*session, string) {
	s, planFile, root := newRepoSession(t)
	s.env["BATON_HOME"] = t.TempDir()
	s.attach(planFile)
	s.set(func(x *state.State) { state.Claim(x, "inst", 1, s.now) })
	return s, root
}

func TestVerifyAndAwaitRefusals(t *testing.T) {
	const notes = "--notes=the parser is done; this checks it"
	for _, c := range []struct {
		name  string
		setup func(s *session, root string)
		args  []string
		want  string
		why   string // the refused event's, if one is logged
	}{
		{"no command", nil, []string{"verify", "--notes", "x"}, `usage: baton verify "<command>" --notes`, ""},
		{"not hosted", func(s *session, _ string) { delete(s.env, "BATON_HOST") }, []string{"verify", "make test", notes}, "not queued — the baton host runs jobs, and this session is not hosted", ""},
		{"no notes", nil, []string{"verify", "make test"}, `not queued — say where P0 stands with --notes "<what is done, what is left, what this job checks>"`, "no notes"},
		{"blank notes", nil, []string{"await", "gh run watch", "--notes", " "}, "not queued — say where P0 stands", "no notes"},
		{"fresh work", func(s *session, root string) { gittest.Write(t, root, "src/new.go", "package src\n") },
			[]string{"verify", "make test", notes}, "and 1 file changed in P0 is not committed, so the gate would not see it:\n    src/new.go\nCommit them (by path), then verify again", "uncommitted work"},
		{"blocked", func(s *session, _ string) { s.set(func(x *state.State) { state.SetBlocked(x, "need a key", s.now) }) },
			[]string{"verify", "make test", notes}, "not queued — the run is waiting on the human (blocked: need a key", "held for the human"},
		{"a proposal not asked", func(s *session, _ string) {
			s.must("", "propose", "drop the cache", "--because", "it is stale", "--undo", "rebuild it")
		}, []string{"await", "gh run watch", notes}, "not queued — put your proposal d1 to the human first", "proposal not asked"},
		{"a phase not started", func(s *session, _ string) { s.set(func(x *state.State) { x.BoundaryOwed = true }) },
			[]string{"verify", "make test", notes}, "not queued — P0 has not started yet: end your turn now", "phase not started"},
		{"no phase in hand", func(s *session, _ string) { s.set(func(x *state.State) { x.Current = "" }) },
			[]string{"verify", "make test", notes}, "not queued — no phase is in hand: every phase left waits for a job to finish", "no phase in hand"},
		{"a plan complete", func(s *session, _ string) { s.set(func(x *state.State) { x.Mode = state.ModeComplete }) },
			[]string{"await", "gh run watch", notes}, "not queued — no plan is running here (mode: complete)", "no run"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := gitSession(t)
			if c.setup != nil {
				c.setup(s, root)
			}
			if why := s.fails(c.args...); !strings.Contains(why, c.want) {
				t.Errorf("refusal %q, want it to mention %q", why, c.want)
			}
			if c.why != "" && !s.logged("refused", map[string]any{"command": c.args[0], "why": c.why}) {
				t.Errorf("no refused event for %q: %s", c.why, s.events())
			}
			if st := s.state(); len(st.Jobs) != 0 {
				t.Errorf("a refused %s queued %+v", c.args[0], st.Jobs)
			}
		})
	}
}

func TestVerifyQueuesAGateOnHead(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	head := gittest.Git(t, root, "rev-parse", "HEAD")
	orphan := filepath.Join(s.store().Dir, "jobs", "j9.log") // a log no job on record has
	os.MkdirAll(filepath.Dir(orphan), 0o755)
	os.WriteFile(orphan, []byte("old\n"), 0o644)

	out := s.must("", "verify", "make", "test", "--notes", "the parser is done")
	for _, want := range []string{"baton: queued j1 — `make test` (P0), on " + head[:12] + ", in a lane of its own. baton starts it within seconds; its output goes to " + s.store().JobLog("j1"),
		"Keep working on what does not depend on its result", "If it runs past 3h0m0s, baton stops it", "What you commit from now on is not in this check."} {
		if !strings.Contains(out, want) {
			t.Errorf("verify says:\n%s\nwant %q", out, want)
		}
	}
	j := s.state().Job("j1")
	if j == nil || j.Phase != "P0" || j.Kind != state.JobVerify || j.Command != "make test" || j.Commit != head || j.Notes != "the parser is done" || j.Status != state.JobQueued || !j.Queued.Equal(s.now) {
		t.Fatalf("job: %+v", j)
	}
	if !s.logged("job_queued", map[string]any{"job": "j1", "phase": "P0", "type": "verify", "command": "make test", "commit": head}) {
		t.Errorf("events: %s", s.events())
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("the log of a job no longer on record is still there")
	}

	// A phase has one job at a time: another replaces it.
	out = s.must("", "await", "gh run watch 7 --exit-status", "--notes", "pushed; CI checks it")
	if !strings.Contains(out, "baton: queued j2 — `gh run watch 7 --exit-status` (P0), run from the working tree.") || !strings.Contains(out, "it replaces j1 (`make test`), which baton stops") {
		t.Errorf("await says:\n%s", out)
	}
	if strings.Contains(out, "not in this check") {
		t.Errorf("await speaks of commits:\n%s", out)
	}
	st := s.state()
	if j1, j2 := st.Job("j1"), st.Job("j2"); j1.Status != state.JobInterrupted || j1.Reason != "replaced by j2" || j2.Kind != state.JobAwait || j2.Commit != "" {
		t.Errorf("jobs: %+v, %+v", *j1, *j2)
	}
	if !s.logged("job_queued", map[string]any{"job": "j2", "type": "await", "replaced": "j1"}) {
		t.Errorf("events: %s", s.events())
	}
}

// Work already uncommitted when the phase started is not the phase's, so it does not hold a gate up; but
// the gate does not see it either, and verify says so.
func TestVerifyNotesWorkFromBeforeThePhase(t *testing.T) {
	s, planFile, root := newRepoSession(t)
	s.env["BATON_HOME"] = t.TempDir()
	gittest.Write(t, root, "scratch.txt", "the human's\n")
	s.attach(planFile)
	if out := s.must("", "verify", "make test", "--notes", "x"); !strings.Contains(out, "1 uncommitted file was already there when P0 started; the gate checks HEAD, without it.") {
		t.Errorf("verify says:\n%s", out)
	}
}

func TestAwaitWorksWithoutGit(t *testing.T) {
	s := attached(t)
	s.env["BATON_HOME"] = t.TempDir()
	if why := s.fails("verify", "make test", "--notes", "x"); !strings.Contains(why, "this project does not use git. Run the gate in place instead") {
		t.Errorf("verify without git: %s", why)
	}
	s.must("", "await", "make test", "--notes", "x")
	if j := s.state().Job("j1"); j == nil || j.Kind != state.JobAwait || j.Commit != "" {
		t.Fatalf("job: %+v", j)
	}
}

// proc is a process standing in for a job's runner.
type proc struct {
	pid  int
	done chan error
}

func sleeper(t *testing.T) proc {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stands in for a runner with a POSIX process")
	}
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := proc{pid: cmd.Process.Pid, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	return p
}

// stopped fails unless the process was told to stop (SIGTERM).
func (p proc) stopped(t *testing.T, by string) {
	t.Helper()
	select {
	case err := <-p.done:
		if err == nil || !strings.Contains(err.Error(), "terminated") {
			t.Errorf("the runner ended with %v, not SIGTERM", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not stop the job's runner", by)
	}
}

func (p proc) running() bool {
	select {
	case err := <-p.done:
		p.done <- err
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

func TestDropStopsTheJobs(t *testing.T) {
	s := attached(t)
	s.env["BATON_HOME"] = t.TempDir()
	runner := sleeper(t)
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobPassed},
			{ID: "j2", Phase: "P0", Command: "make e2e", Status: state.JobRunning, PID: runner.pid, Heartbeat: s.now}}
		x.JobSeq = 2
	})
	logs := filepath.Join(s.store().Dir, "jobs")
	os.MkdirAll(logs, 0o755)
	os.WriteFile(filepath.Join(logs, "j1.log"), []byte("passed\n"), 0o644)

	out := s.must("", "drop")
	if !strings.Contains(out, "baton: it stopped job j2 (`make e2e`, P0), and removed the logs of the run's jobs.") {
		t.Errorf("drop says:\n%s", out)
	}
	runner.stopped(t, "drop")
	if _, err := os.Stat(logs); err == nil {
		t.Error("the jobs' logs are still there")
	}
	if st := s.state(); len(st.Jobs) != 0 || st.JobSeq != 2 {
		t.Errorf("after drop: jobs %+v, seq %d", st.Jobs, st.JobSeq)
	}
	if ev := s.events(); !strings.Contains(ev, `"kind":"dropped"`) || !strings.Contains(ev, `"stopped":["j2"]`) {
		t.Errorf("events: %s", ev)
	}
}

// A runner not heard from for a while may be gone, and its pid another process's: drop does not signal it.
func TestStopRunnersSparesARunnerNotHeardFrom(t *testing.T) {
	runner := sleeper(t)
	now := time.Now()
	stopRunners([]state.Job{{ID: "j1", PID: runner.pid, Heartbeat: now.Add(-time.Hour)}}, now)
	if !runner.running() {
		t.Fatal("a runner not heard from for an hour was signalled")
	}
	stopRunners([]state.Job{{ID: "j1", PID: runner.pid, Spawned: now}}, now)
	runner.stopped(t, "stopping a runner just started")
}

func TestTheJobsStopWhenTheSessionEnds(t *testing.T) {
	s := attached(t)
	runner := sleeper(t)
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning, PID: runner.pid, Heartbeat: s.now}}
	})
	io, out, _ := testIO("", s.env)
	io.Now = func() time.Time { return s.now }
	endJobs(io, s.store(), "another-host")
	if j := s.state().Job("j1"); j.Status != state.JobRunning {
		t.Fatalf("a host that does not drive the run stopped its job: %+v", *j)
	}
	endJobs(io, s.store(), "inst")
	runner.stopped(t, "the session's end")
	if j := s.state().Job("j1"); j.Status != state.JobInterrupted || j.Reason != "the baton session ended" {
		t.Errorf("job: %+v", *j)
	}
	if !strings.Contains(out.String(), "baton: stopped job j1 (`make test`, P0), as the session ended.") || !s.logged("jobs_stopped", map[string]any{"why": "the session ended"}) {
		t.Errorf("said %q; events %s", out, s.events())
	}
}

func TestStatusListsTheJobs(t *testing.T) {
	s, root := gitSession(t)
	head := gittest.Git(t, root, "rev-parse", "HEAD")
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{
			{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Commit: head, Status: state.JobFailed, Exit: 2, Lane: 1, Started: s.now.Add(-time.Hour), Ended: s.now.Add(-48 * time.Minute)},
			{ID: "j2", Phase: "P0", Kind: state.JobAwait, Command: "gh run watch", Status: state.JobInterrupted, Exit: -1, Started: s.now.Add(-5 * time.Minute), Ended: s.now.Add(-5*time.Minute + 30*time.Second), Reason: "replaced by j3"},
			{ID: "j3", Phase: "P0", Kind: state.JobVerify, Command: "make test", Commit: head, Status: state.JobRunning, Lane: 1, Log: "/elsewhere/j3.log", Started: s.now.Add(-2*time.Hour - 5*time.Minute)},
			{ID: "j4", Phase: "P0", Kind: state.JobAwait, Command: "sleep 1", Status: state.JobQueued},
		}
	})
	out := s.must("", "status")
	for _, want := range []string{
		"jobs (newest first):\n  j4   P0       await  queued  `sleep 1`\n       log: " + s.store().JobLog("j4") + "\n" +
			"  j3   P0       verify running 2h05m, lane 1  `make test` on " + head[:12] + "\n       log: /elsewhere/j3.log\n" +
			"  j2   P0       await  interrupted after 30s: replaced by j3  `gh run watch`\n",
		"  j1   P0       verify failed (exit 2) after 12m, lane 1  `make test` on " + head[:12] + "\n",
		"lanes: this project has no .baton-lane, so a gate runs with sh -c in a bare checkout of its commit, one at a time",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status:\n%s\nwant %q", out, want)
		}
	}
}

func TestTheHostRefusesAnInvalidVerifyTimeout(t *testing.T) {
	t.Chdir(t.TempDir())
	io, _, errb := testIO("", map[string]string{"BATON_HOME": t.TempDir(), "BATON_VERIFY_TIMEOUT": "forever", "BATON_CLAUDE": "/nonexistent/claude"})
	if code := runHostWith(nil, io, nil); code != 1 || !strings.Contains(errb.String(), `BATON_VERIFY_TIMEOUT "forever" is not valid: use a duration from 1s to 24h`) {
		t.Errorf("exit %d: %s", code, errb)
	}
}

// The test binary runs baton itself when CLI_TEST_MAIN is set: that is how the host's runners run here.
func TestMain(m *testing.M) {
	if os.Getenv("CLI_TEST_MAIN") == "1" {
		os.Exit(Main(os.Args[1:], StdIO()))
	}
	os.Exit(m.Run())
}

type keys struct{}

func (keys) Type(string, bool) error { return nil }
func (keys) ClearInput() error       { return nil }
func (keys) Quit() error             { return nil }

// The whole way through: the host's loop starts a detached runner (baton lane-run, here the test binary)
// for a queued job, which runs the gate in a lane and records how it ended.
func TestTheHostRunsAJobToItsEnd(t *testing.T) {
	s, env := laneProject(t, "sleep 1; echo gate output; exit 1")
	t.Setenv("CLI_TEST_MAIN", "1")
	p, err := state.OpenProject(env["BATON_DIR"], time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err = p.Bind(state.Binding{Session: "sess-1", Instance: "inst"})
	if err != nil {
		t.Fatal(err)
	}
	io, _, _ := testIO("", env)
	io.Now = time.Now
	exe, _ := os.Executable()
	l := &loop.Loop{Store: s, Timing: loop.DefaultTiming, Runners: newHostRunners(io, exe, p, "inst")}

	deadline := time.Now().Add(30 * time.Second)
	for {
		now := time.Now()
		s.Update(func(st *state.State) error { st.Owner.Heartbeat = now; return nil })
		l.Tick(host.View{Now: now, Store: s, Owner: true, LastOutput: now.Add(-time.Hour), LastHumanKey: now.Add(-time.Hour)}, keys{})
		if st, _ := s.Load(); st.Job("j1").Finished() {
			break
		}
		if now.After(deadline) {
			b, _ := os.ReadFile(filepath.Join(env["BATON_DIR"], "baton.log"))
			t.Fatalf("the job never finished; baton.log:\n%s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, _ := s.Load()
	j := st.Job("j1")
	if j.Status != state.JobFailed || j.Exit != 1 || j.Lane != 1 || j.Spawned.IsZero() || j.PID == os.Getpid() {
		t.Errorf("job: %+v", *j)
	}
	if log, _ := os.ReadFile(j.Log); !strings.Contains(string(log), "gate output") {
		t.Errorf("job log:\n%s", log)
	}
	ev := events(t, s)
	if ev["job_started"] == nil || ev["job_finished"] == nil || ev["job_finished"]["status"] != "failed" {
		t.Errorf("events: %v", ev)
	}
}
