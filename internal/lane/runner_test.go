package lane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
)

// steppingAfter is an injected clock that stands still until moving says so, and from then on moves on
// by step each time it is read.
func steppingAfter(moving func() bool, step time.Duration) func() time.Time {
	t := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		if moving() {
			t = t.Add(step)
		}
		return t
	}
}

func TestCheck(t *testing.T) {
	type beat struct {
		n      int           // how many beats like this
		after  time.Duration // time since the beat before
		host   bool          // the host beat in between
		wanted bool
	}
	for _, c := range []struct {
		name  string
		beats []beat
		watch bool // a host to watch (Beat is set)
		want  string
		ran   time.Duration
	}{
		{"a job inside its timeout", []beat{{239, 15 * time.Second, true, true}}, true, "", 59*time.Minute + 45*time.Second},
		{"a job past its timeout", []beat{{240, 15 * time.Second, true, true}}, true, Timeout, time.Hour},
		{"the machine slept", []beat{{1, 10 * time.Hour, true, true}, {4, 15 * time.Second, true, true}}, true, "", time.Minute},
		{"a long pause just under the gap counts", []beat{{60, time.Minute, true, true}}, true, Timeout, time.Hour},
		{"the host went quiet", []beat{{40, 15 * time.Second, false, true}}, true, Interrupted, 10 * time.Minute},
		{"the host went quiet briefly", []beat{{39, 15 * time.Second, false, true}, {1, 15 * time.Second, true, true}, {39, 15 * time.Second, false, true}}, true, "", 19*time.Minute + 45*time.Second},
		{"the host was quiet while the machine slept", []beat{{1, 8 * time.Hour, false, true}, {39, 15 * time.Second, false, true}}, true, "", 9*time.Minute + 45*time.Second},
		{"the job was cancelled", []beat{{3, 15 * time.Second, true, true}, {1, 15 * time.Second, true, false}}, true, Interrupted, time.Minute},
		{"no host to watch", []beat{{100, 15 * time.Second, false, true}}, false, "", 25 * time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			host, wanted := now, true
			r := &Runner{Timeout: time.Hour, Tick: 15 * time.Second, HostStale: 10 * time.Minute, Gap: time.Minute, Now: func() time.Time { return now }}
			if c.watch {
				r.Beat = func(time.Time) (time.Time, bool) { return host, wanted }
			}
			r.begin()
			r.got(0)
			var stop *Result
			for _, b := range c.beats {
				for i := 0; i < b.n && stop == nil; i++ {
					now = now.Add(b.after)
					if b.host {
						host = now
					}
					wanted = b.wanted
					stop = r.check()
				}
			}
			got := ""
			if stop != nil {
				got = stop.Status
			}
			if got != c.want || r.ran != c.ran {
				t.Errorf("stopped: %q after %s, want %q after %s (%+v)", got, r.ran, c.want, c.ran, stop)
			}
		})
	}
}

// The timeout counts from when the job got its lane: a wait for one, however long, is not running time.
func TestTheTimeoutCountsFromTheLane(t *testing.T) {
	for _, c := range []struct {
		name          string
		before, after int  // beats of a minute before the job got its lane, and after
		got           bool // it got one
		want          string
		waited        time.Duration
	}{
		{"waiting past the timeout", 120, 0, false, "", 0},
		{"inside the timeout after a wait", 30, 59, true, "", 30 * time.Minute},
		{"past the timeout after a wait", 30, 60, true, Timeout, 30 * time.Minute},
		{"past the timeout with no wait", 0, 60, true, Timeout, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
			var told []time.Duration
			r := &Runner{Timeout: time.Hour, Tick: time.Minute, Gap: time.Hour, Now: func() time.Time { return now },
				Acquired: func(at time.Time, lane int, waited time.Duration) {
					if !at.Equal(now) || lane != 1 {
						t.Errorf("told of lane %d at %s, want lane 1 at %s", lane, at, now)
					}
					told = append(told, waited)
				}}
			r.begin()
			var stop *Result
			for range c.before {
				now = now.Add(time.Minute)
				if stop = r.check(); stop != nil {
					t.Fatalf("stopped while it waited for a lane: %+v", stop)
				}
			}
			if c.got {
				r.got(1)
			}
			for i := 0; i < c.after && stop == nil; i++ {
				now = now.Add(time.Minute)
				stop = r.check()
			}
			got := ""
			if stop != nil {
				got = stop.Status
			}
			if got != c.want || r.waited != c.waited {
				t.Errorf("stopped %q having waited %s, want %q having waited %s", got, r.waited, c.want, c.waited)
			}
			if c.got && (len(told) != 1 || told[0] != c.waited) {
				t.Errorf("told of the lane %v, want once, having waited %s", told, c.waited)
			}
		})
	}
}

// alive reports whether pid is still running, waiting a little for it to go: a process that was just
// killed takes a moment to be reaped.
func alive(pid int) bool {
	for range 50 {
		if !running(pid) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

func pids(t *testing.T, path string) []int {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []int
	for _, f := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("pid file: %q", b)
		}
		out = append(out, n)
	}
	return out
}

// gate starts a server in the background, records both pids in $PIDS, and then hangs.
const gate = `sleep 300 & echo $! >> "$PIDS"; echo $$ >> "$PIDS"; echo started; sleep 300`

// gateStarted reports whether gate has started, from its pid file.
func gateStarted(pidfile string) func() bool {
	return func() bool {
		b, _ := os.ReadFile(pidfile)
		return len(strings.Fields(string(b))) >= 2
	}
}

func TestATimeoutKillsTheWholeGroup(t *testing.T) {
	root, head := project(t, false)
	pidfile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("PIDS", pidfile)
	r := testRunner(t, root)
	// Time stands still until the gate has started, then a minute passes with every beat.
	r.Now, r.Timeout, r.Gap = steppingAfter(gateStarted(pidfile), time.Minute), 3*time.Minute, time.Hour
	start := time.Now()
	got := r.Run(context.Background(), Job{ID: "j1", Command: gate, Commit: head})
	if got.Status != Timeout || got.Took < 3*time.Minute || !strings.Contains(got.Reason, "3m0s") {
		t.Fatalf("%+v\n%s", got, logOf(t, r))
	}
	if real := time.Since(start); real > 5*time.Second {
		t.Errorf("the timeout took %s of real time", real)
	}
	for _, pid := range pids(t, pidfile) {
		if alive(pid) {
			t.Errorf("process %d outlived its job's timeout", pid)
		}
	}
}

func TestAStoppedJobBringsItsLaneDown(t *testing.T) {
	for _, c := range []struct {
		name    string
		reason  string
		arrange func(r *Runner, started func() bool, cancel func())
	}{
		{"SIGTERM", "stopped", func(_ *Runner, started func() bool, cancel func()) {
			go func() {
				for !started() {
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
			}()
		}},
		{"cancelled", "cancelled", func(r *Runner, started func() bool, _ func()) {
			r.Beat = func(time.Time) (time.Time, bool) { return time.Now(), !started() }
		}},
		{"the host is gone", "not been heard from", func(r *Runner, started func() bool, _ func()) {
			host := time.Now()
			r.Beat = func(time.Time) (time.Time, bool) { return host, true }
			r.Now, r.Gap, r.HostStale = steppingAfter(started, time.Minute), time.Hour, 5*time.Minute
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, head := project(t, true)
			path := calls(t)
			pidfile := filepath.Join(t.TempDir(), "pids")
			t.Setenv("PIDS", pidfile)
			r := testRunner(t, root)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.arrange(r, gateStarted(pidfile), cancel)
			got := r.Run(ctx, Job{ID: "j1", Command: gate, Commit: head})
			if got.Status != Interrupted || !strings.Contains(got.Reason, c.reason) || got.Lane != 1 {
				t.Fatalf("%+v, want interrupted (%s)\n%s", got, c.reason, logOf(t, r))
			}
			if want := []string{"slots", "up", "run", "down"}; !slices.Equal(readCalls(path), want) {
				t.Errorf("calls = %v, want %v", readCalls(path), want)
			}
			if up := r.Pool.upBy(1); up != "" {
				t.Errorf("the lane is still up by %q", up)
			}
			for _, pid := range pids(t, pidfile) {
				if alive(pid) {
					t.Errorf("process %d outlived its stopped job", pid)
				}
			}
		})
	}
}

// lanes makes lanes 1 to n in p at commit, each brought up by the host in up (none for "").
func lanes(t *testing.T, p Pool, commit string, up ...string) {
	t.Helper()
	if err := p.prepare(); err != nil {
		t.Fatal(err)
	}
	for i, host := range up {
		s, err := p.tryLane(i + 1)
		if err != nil || s == nil || s.N != i+1 {
			t.Fatalf("lane %d: %+v, %v", i+1, s, err)
		}
		if err := p.checkout(context.Background(), s, commit); err != nil {
			t.Fatal(err)
		}
		if host != "" {
			os.WriteFile(p.upPath(s.N), []byte(host+"\n"), 0o644)
		}
		s.Release()
	}
}

// listed reports whether git lists lane n among the repository's worktrees.
func listed(t *testing.T, p Pool, n int) bool {
	t.Helper()
	list := gittest.Git(t, p.Root, "worktree", "list", "--porcelain") + "\n"
	real, _ := filepath.EvalSymlinks(p.Dir())
	return strings.Contains(list, "worktree "+p.LaneDir(n)+"\n") || strings.Contains(list, "worktree "+filepath.Join(real, strconv.Itoa(n))+"\n")
}

func TestSweep(t *testing.T) {
	root, head := project(t, true)
	path := calls(t)
	p := Pool{Home: t.TempDir(), Root: root}
	// Lane 1 is up by a live host, 2 by a dead one, 3 was never up, and a job holds lane 4.
	lanes(t, p, head, "alive", "dead", "", "dead")
	busy, _ := p.tryLane(4)
	defer busy.Release()

	if got := p.Sweepable(func(h string) bool { return h == "alive" }); !slices.Equal(got, []int{2, 3}) || len(readCalls(path)) > 0 {
		t.Errorf("Sweepable = %v, having run %v; want [2 3], having run nothing", got, readCalls(path))
	}
	var out strings.Builder
	got := p.Sweep(context.Background(), func(h string) bool { return h == "alive" }, &out)
	if !slices.Equal(got, []int{2, 3}) {
		t.Errorf("swept %v, want [2 3]", got)
	}
	if want := []string{"down"}; !slices.Equal(readCalls(path), want) {
		t.Errorf("calls = %v, want %v (lane 2's down)", readCalls(path), want)
	}
	if !strings.Contains(out.String(), "down in "+p.LaneDir(2)) {
		t.Errorf("the sweep's output: %q", out.String())
	}
	for n, want := range map[int]bool{1: true, 2: false, 3: false, 4: true} {
		_, err := os.Stat(p.LaneDir(n))
		if (err == nil) != want || listed(t, p, n) != want {
			t.Errorf("lane %d: there %v, listed by git %v; want %v", n, err == nil, listed(t, p, n), want)
		}
	}
	if p.upBy(1) != "alive" || p.upBy(2) != "" || p.upBy(4) != "dead" {
		t.Errorf("up markers: 1 %q, 2 %q, 4 %q", p.upBy(1), p.upBy(2), p.upBy(4))
	}

	// A lane whose directory went missing is forgotten by git.
	os.RemoveAll(p.LaneDir(1))
	p.Sweep(context.Background(), nil, &out)
	if listed(t, p, 1) {
		t.Error("git still lists a lane whose directory is gone")
	}
}

func TestSweepLeavesAProjectWithoutLanesAlone(t *testing.T) {
	root, _ := project(t, false)
	other := filepath.Join(t.TempDir(), "on-a-drive-not-mounted")
	gittest.Git(t, root, "worktree", "add", "-q", "--detach", other)
	os.RemoveAll(other)
	p := Pool{Home: t.TempDir(), Root: root}
	if got := p.Sweep(context.Background(), nil, &strings.Builder{}); got != nil {
		t.Errorf("swept %v", got)
	}
	if !strings.Contains(gittest.Git(t, root, "worktree", "list"), "on-a-drive-not-mounted") {
		t.Error("the sweep pruned the human's own worktree")
	}
	if _, err := os.Stat(p.Dir()); err == nil {
		t.Error("the sweep made a pool")
	}
}

func TestDown(t *testing.T) {
	root, head := project(t, true)
	path := calls(t)
	p := Pool{Home: t.TempDir(), Root: root}
	lanes(t, p, head, "h1", "h2", "h1")
	busy, _ := p.tryLane(3)
	defer busy.Release()
	var out strings.Builder
	if got := p.Down("h1", &out); !slices.Equal(got, []int{1}) {
		t.Errorf("brought down %v, want [1]", got)
	}
	if want := []string{"down"}; !slices.Equal(readCalls(path), want) {
		t.Errorf("calls = %v, want %v", readCalls(path), want)
	}
	if p.upBy(1) != "" || p.upBy(2) != "h2" || p.upBy(3) != "h1" {
		t.Errorf("up markers: 1 %q, 2 %q, 3 %q", p.upBy(1), p.upBy(2), p.upBy(3))
	}
	for n := 1; n <= 3; n++ {
		if _, err := os.Stat(p.LaneDir(n)); err != nil {
			t.Errorf("lane %d's worktree went with its down: %v", n, err)
		}
	}
}

func TestLanes(t *testing.T) {
	root, head := project(t, true)
	p := Pool{Home: t.TempDir(), Root: root}
	if got := p.Lanes(); len(got) != 0 {
		t.Errorf("a pool with no lanes lists %+v", got)
	}
	lanes(t, p, head, "h1", "")
	busy, _ := p.tryLane(2)
	defer busy.Release()
	if got, want := p.Lanes(), []Lane{{1, p.LaneDir(1), false, "h1"}, {2, p.LaneDir(2), true, ""}}; !slices.Equal(got, want) {
		t.Errorf("lanes %+v, want %+v", got, want)
	}
	if !HasScript(root) || HasScript(t.TempDir()) {
		t.Errorf("HasScript: %v for the project, %v for an empty directory", HasScript(root), HasScript(t.TempDir()))
	}
}

// The detached runner: the test binary runs a job as a helper process started with Spawn.

type helperReport struct {
	Result Result
	PID    int
	PPID   int
	PGID   int
}

func TestMain(m *testing.M) {
	if dir := os.Getenv("LANE_HELPER"); dir != "" {
		os.Exit(helper(dir))
	}
	os.Exit(m.Run())
}

func helper(dir string) int {
	time.Sleep(200 * time.Millisecond) // long enough for whoever started this to be gone
	log, err := CreateLog(filepath.Join(dir, "job.log"))
	if err != nil {
		return 1
	}
	defer log.Close()
	r := &Runner{Pool: Pool{Home: filepath.Join(dir, "home"), Root: os.Getenv("LANE_HELPER_ROOT")}, Host: "h1", Log: log, Tick: 50 * time.Millisecond}
	res := r.Run(context.Background(), Job{ID: "j1", Command: "sleep 1; echo done waiting; exit 1", Commit: os.Getenv("LANE_HELPER_COMMIT")})
	b, _ := json.Marshal(helperReport{Result: res, PID: os.Getpid(), PPID: os.Getppid(), PGID: pgid(0)})
	return writeFile(filepath.Join(dir, "report.json"), b)
}

func writeFile(path string, b []byte) int {
	if err := os.WriteFile(path+".tmp", b, 0o644); err != nil {
		return 1
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return 1
	}
	return 0
}

func TestASpawnedRunnerRunsDetached(t *testing.T) {
	root, head := project(t, false)
	dir := t.TempDir()
	env := append(os.Environ(), "LANE_HELPER="+dir, "LANE_HELPER_ROOT="+root, "LANE_HELPER_COMMIT="+head)
	pid, err := Spawn([]string{os.Args[0], "-test.run=^$"}, env, root, filepath.Join(dir, "runner.out"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	var b []byte
	for time.Now().Before(deadline) {
		if b, err = os.ReadFile(filepath.Join(dir, "report.json")); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		out, _ := os.ReadFile(filepath.Join(dir, "runner.out"))
		t.Fatalf("the runner never reported: %v\n%s", err, out)
	}
	var rep helperReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Result.Status != Failed || rep.Result.Exit != 1 || rep.Result.Lane != 1 || rep.Result.Took < time.Second {
		t.Errorf("result: %+v", rep.Result)
	}
	if rep.PID != pid {
		t.Errorf("Spawn returned pid %d, but the runner is %d", pid, rep.PID)
	}
	if rep.PPID == os.Getpid() {
		t.Error("the runner is still the test's child")
	}
	if rep.PGID == pgid(0) {
		t.Error("the runner is in the test's process group")
	}
	log, _ := os.ReadFile(filepath.Join(dir, "job.log"))
	if !strings.Contains(string(log), "done waiting") || !strings.Contains(string(log), "] failed (exit 1)") {
		t.Errorf("job log:\n%s", log)
	}
}

// A job is told when it got its lane, whether it found one free or waited for one, and a job that needs
// none as its command begins; its result says how long it waited.
func TestAJobIsToldWhenItGetsItsLane(t *testing.T) {
	type told struct {
		lane   int
		waited time.Duration
	}
	for _, c := range []struct {
		name     string
		lane     bool // the job runs in a lane
		hold     time.Duration
		wantLane int
	}{
		{"a free lane", true, 0, 1},
		{"a lane after a wait", true, 300 * time.Millisecond, 1},
		{"no lane needed", false, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, head := project(t, false)
			r := testRunner(t, root)
			r.Gap = time.Hour
			var got []told
			r.Acquired = func(_ time.Time, lane int, waited time.Duration) { got = append(got, told{lane, waited}) }
			if c.hold > 0 {
				held, _ := r.Pool.TryAcquire(1)
				time.AfterFunc(c.hold, func() { held.Release() })
			}
			j := Job{ID: "j1", Command: "true"}
			if c.lane {
				j.Commit = head
			}
			res := r.Run(context.Background(), j)
			if res.Status != Passed || len(got) != 1 || got[0].lane != c.wantLane {
				t.Fatalf("%+v; told %+v, want once, of lane %d", res, got, c.wantLane)
			}
			if w := got[0].waited; w < c.hold*3/4 || w > c.hold+5*time.Second || res.Waited != w || res.Took < w {
				t.Errorf("told it waited %s, result %+v; want about %s", w, res, c.hold)
			}
			if came := strings.Contains(logOf(t, r), "lane 1 came free after"); came != (c.hold > 0) {
				t.Errorf("the log says a lane came free: %v\n%s", came, logOf(t, r))
			}
		})
	}
}

// The runner's own lines in the log say when it wrote them.
func TestRunnerLinesCarryTheTime(t *testing.T) {
	root, _ := project(t, false)
	r := testRunner(t, root)
	r.Now = func() time.Time { return time.Date(2026, 10, 17, 14, 2, 11, 0, time.Local) }
	if res := r.Run(context.Background(), Job{ID: "j1", Command: "true"}); res.Status != Passed {
		t.Fatalf("%+v", res)
	}
	for _, want := range []string{"[baton 14:02:11] job j1: true, in ", "[baton 14:02:11] passed after 0s\n"} {
		if log := logOf(t, r); !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	var b strings.Builder
	Say(&b, time.Date(2026, 10, 17, 9, 5, 0, 0, time.Local), "lane %d", 2)
	if b.String() != "[baton 09:05:00] lane 2\n" {
		t.Errorf("Say wrote %q", b.String())
	}
}

// A job's command gets its variables of the gate protocol, and finds the baton running it first on its
// PATH.
func TestAJobsCommandGetsItsEnvironment(t *testing.T) {
	for _, c := range []struct {
		name  string
		rerun string
		qa    bool
		want  string
	}{
		{"a gate", "", false, "j7 /runs/s/jobs/j7.stages none none /opt/baton/v1"},
		{"a rerun", "/runs/s/jobs/j3.stages", false, "j7 /runs/s/jobs/j7.stages /runs/s/jobs/j3.stages none /opt/baton/v1"},
		{"a QA phase's gate", "", true, "j7 /runs/s/jobs/j7.stages none 1 /opt/baton/v1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, head := project(t, false)
			r := testRunner(t, root)
			r.Pool.Bin = "/opt/baton/v1"
			out := filepath.Join(t.TempDir(), "env.txt")
			cmd := `echo "$BATON_JOB" "$BATON_STAGES" "${BATON_RERUN-none}" "${BATON_QA_PHASE-none}" "${PATH%%:*}" > ` + out
			res := r.Run(context.Background(), Job{ID: "j7", Command: cmd, Commit: head, Stages: "/runs/s/jobs/j7.stages", Rerun: c.rerun, QAPhase: c.qa})
			if res.Status != Passed {
				t.Fatalf("%+v\n%s", res, logOf(t, r))
			}
			if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != c.want {
				t.Errorf("the command saw %q, want %q", strings.TrimSpace(string(b)), c.want)
			}
		})
	}
}

func TestFirstOnPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	for _, c := range []struct {
		name string
		env  []string
		want []string
	}{
		{"a PATH", []string{"HOME=/h", "PATH=/usr/bin" + sep + "/bin"}, []string{"HOME=/h", "PATH=/b" + sep + "/usr/bin" + sep + "/bin"}},
		{"first already", []string{"PATH=/b" + sep + "/bin"}, []string{"PATH=/b" + sep + "/bin"}},
		{"only it", []string{"PATH=/b"}, []string{"PATH=/b"}},
		{"an empty PATH", []string{"PATH="}, []string{"PATH=/b"}},
		{"no PATH", []string{"HOME=/h"}, []string{"HOME=/h", "PATH=/b"}},
		{"a longer name that starts with it", []string{"PATH=/bin"}, []string{"PATH=/b" + sep + "/bin"}},
	} {
		if got := firstOnPath(slices.Clone(c.env), "/b"); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
