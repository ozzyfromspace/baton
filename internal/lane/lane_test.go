package lane

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
)

// needSh skips tests that run shell commands and lane scripts, which baton's lanes are written for.
func needSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("lanes run POSIX shell commands")
	}
}

// project is a repository with one commit: a.txt, a .gitignore for *.cache, and (withScript) the
// stand-in lane script from testdata as its .baton-lane.
func project(t *testing.T, withScript bool) (root, head string) {
	t.Helper()
	needSh(t)
	gittest.Isolate(t)
	root = t.TempDir()
	gittest.Init(t, root)
	gittest.Write(t, root, "a.txt", "a\n")
	gittest.Write(t, root, ".gitignore", "*.cache\n")
	if withScript {
		b, err := os.ReadFile("testdata/lane.sh")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, Script), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Commit(t, root, "first")
	return root, gittest.Git(t, root, "rev-parse", "HEAD")
}

// calls is where the stand-in lane script records its calls in this test.
func calls(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "calls")
	t.Setenv("LANE_CALLS", path)
	return path
}

func readCalls(path string) []string {
	b, _ := os.ReadFile(path)
	return strings.Fields(string(b))
}

// testRunner runs jobs for host h1 in a pool of its own, beating every few milliseconds.
func testRunner(t *testing.T, root string) *Runner {
	t.Helper()
	log, err := CreateLog(filepath.Join(t.TempDir(), "job.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	return &Runner{Pool: Pool{Home: t.TempDir(), Root: root}, Host: "h1", Log: log, Tick: 10 * time.Millisecond, Grace: 2 * time.Second}
}

func logOf(t *testing.T, r *Runner) string {
	t.Helper()
	b, _ := os.ReadFile(r.Log.(*Log).Path())
	return string(b)
}

func TestOutcome(t *testing.T) {
	for exit, want := range map[int]string{0: Passed, 1: Failed, 2: Failed, 124: Failed, 125: LaneError, 126: LaneError, 127: LaneError, 128: Failed, 137: Failed, -1: Failed} {
		if got := Outcome(exit); got != want {
			t.Errorf("Outcome(%d) = %s, want %s", exit, got, want)
		}
	}
}

func TestExitCodes(t *testing.T) {
	root, head := project(t, false)
	r := testRunner(t, root)
	for _, c := range []struct {
		command string
		status  string
		exit    int
	}{
		{"exit 0", Passed, 0},
		{"echo broken >&2; exit 3", Failed, 3},
		{"exit 125", LaneError, 125},
		{"exit 126", LaneError, 126},
		{"exit 127", LaneError, 127},
		{"no-such-command-baton-test", LaneError, 127},
		{"kill -9 $$", Failed, -1},
	} {
		got := r.Run(context.Background(), Job{ID: "j1", Command: c.command, Commit: head})
		if got.Status != c.status || got.Exit != c.exit || got.Lane != 1 {
			t.Errorf("%q: %+v, want %s, exit %d, in lane 1", c.command, got, c.status, c.exit)
		}
		if c.status == LaneError && got.Reason == "" {
			t.Errorf("%q: a lane error with no reason", c.command)
		}
	}
	if log := logOf(t, r); !strings.Contains(log, "broken") || !strings.Contains(log, "[baton] failed (exit 3)") {
		t.Errorf("the log has neither the command's output nor its verdict:\n%s", log)
	}
}

func TestTheLaneContract(t *testing.T) {
	root, head := project(t, true)
	path := calls(t)
	r := testRunner(t, root)
	t.Setenv("BATON_DIR", "/leaked")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "leaked")
	t.Setenv("GIT_AUTHOR_NAME", "leaked")

	got := r.Run(context.Background(), Job{ID: "j1", Command: "echo hello from the gate", Commit: head})
	if got.Status != Passed || got.Lane != 1 {
		t.Fatalf("first job: %+v\n%s", got, logOf(t, r))
	}
	if want := []string{"slots", "up", "run"}; !slices.Equal(readCalls(path), want) {
		t.Errorf("calls = %v, want %v", readCalls(path), want)
	}
	b, _ := os.ReadFile(path + ".env")
	env := strings.Fields(string(b))
	slices.Sort(env)
	dir := r.Pool.LaneDir(1)
	if want := []string{"BATON_COMMIT=" + head, "BATON_LANE=1", "BATON_LANE_DIR=" + dir, "BATON_ROOT=" + root}; !slices.Equal(env, want) {
		t.Errorf("the lane script's environment: %q, want %q", env, want)
	}
	if log := logOf(t, r); !strings.Contains(log, "hello from the gate") || !strings.Contains(log, "up in "+dir) {
		t.Errorf("the log misses the script's output:\n%s", log)
	}

	r.Run(context.Background(), Job{ID: "j2", Command: "true", Commit: head})
	if want := []string{"slots", "up", "run", "slots", "run"}; !slices.Equal(readCalls(path), want) {
		t.Errorf("a lane was brought up twice for one host: calls = %v, want %v", readCalls(path), want)
	}
	r.Host = "h2"
	r.Run(context.Background(), Job{ID: "j3", Command: "true", Commit: head})
	if want := []string{"slots", "up", "run", "slots", "run", "slots", "down", "up", "run"}; !slices.Equal(readCalls(path), want) {
		t.Errorf("a new host did not bring the lane down and up again: calls = %v, want %v", readCalls(path), want)
	}
	if by := r.Pool.upBy(1); by != "h2" {
		t.Errorf("lane 1 is up by %q, want h2", by)
	}
}

func TestUpFailingTwiceIsALaneError(t *testing.T) {
	for _, c := range []struct {
		fails  string
		status string
		calls  []string
	}{
		{"1", Passed, []string{"slots", "up", "up", "run"}},
		{"2", LaneError, []string{"slots", "up", "up"}},
	} {
		t.Run("fails "+c.fails, func(t *testing.T) {
			root, head := project(t, true)
			path := calls(t)
			t.Setenv("LANE_UP_FAILS", c.fails)
			r := testRunner(t, root)
			got := r.Run(context.Background(), Job{ID: "j1", Command: "true", Commit: head})
			if got.Status != c.status || !slices.Equal(readCalls(path), c.calls) {
				t.Errorf("%+v, calls %v; want %s, calls %v", got, readCalls(path), c.status, c.calls)
			}
			if up := r.Pool.upBy(1); (up == "h1") != (c.status == Passed) {
				t.Errorf("the lane is up by %q after %s", up, got.Status)
			}
			if c.status == LaneError && !strings.Contains(got.Reason, "up failed twice") {
				t.Errorf("reason = %q", got.Reason)
			}
		})
	}
}

func TestSlots(t *testing.T) {
	root, _ := project(t, true)
	calls(t)
	p := Pool{Home: t.TempDir(), Root: root}
	for answer, want := range map[string]int{"1": 1, "3": 3, "4": 4, "9": 4, "0": 1, "-2": 1, "many": 1, "": 1} {
		t.Setenv("LANE_SLOTS", answer)
		if got := p.Slots(context.Background()); got != want {
			t.Errorf("slots answering %q = %d, want %d", answer, got, want)
		}
	}
	bare, _ := project(t, false)
	if got := (Pool{Home: t.TempDir(), Root: bare}).Slots(context.Background()); got != 1 {
		t.Errorf("slots without a lane script = %d, want 1", got)
	}
}

func TestSlotContention(t *testing.T) {
	root, head := project(t, false)
	p := Pool{Home: t.TempDir(), Root: root}
	a, _ := p.TryAcquire(2)
	b, _ := p.TryAcquire(2)
	if a == nil || b == nil || a.N != 1 || b.N != 2 {
		t.Fatalf("two slots of two: %+v, %+v", a, b)
	}
	if c, err := p.TryAcquire(2); c != nil || err != nil {
		t.Fatalf("a third slot of two: %+v, %v", c, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if c, err := p.Acquire(ctx, 2); c != nil || err == nil {
		t.Fatalf("Acquire with every slot held: %+v, %v", c, err)
	}
	a.Release()
	c, err := p.Acquire(context.Background(), 2)
	if err != nil || c.N != 1 {
		t.Fatalf("Acquire after a release: %+v, %v", c, err)
	}
	c.Release()

	// A job waits for a lane, says so, and takes the one that comes free.
	r := testRunner(t, root)
	r.Pool = p
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return at }
	var waits []time.Time
	r.LaneWait = func(since time.Time, slots, ahead int) { waits = append(waits, since) }
	held, _ := p.TryAcquire(1)
	time.AfterFunc(300*time.Millisecond, func() { held.Release() })
	got := r.Run(context.Background(), Job{ID: "j1", Command: "true", Commit: head})
	if got.Status != Passed || got.Lane != 1 {
		t.Errorf("a job that waited for a lane: %+v", got)
	}
	if log := logOf(t, r); !strings.Contains(log, "every lane is in use (1); waiting for one") {
		t.Errorf("the log does not say the job waited:\n%s", log)
	}
	if !slices.Equal(waits, []time.Time{at, {}}) {
		t.Errorf("told of the wait %v, want from %s, then over", waits, at)
	}

	// A job that finds a lane free waits for none.
	waits = nil
	if got := r.Run(context.Background(), Job{ID: "j2", Command: "true", Commit: head}); got.Status != Passed || len(waits) != 0 {
		t.Errorf("%+v; told of a wait %v", got, waits)
	}
	b.Release()
}

func TestALaneIsReusedWithItsCaches(t *testing.T) {
	root, first := project(t, false)
	r := testRunner(t, root)
	got := r.Run(context.Background(), Job{ID: "j1", Command: "echo deps > deps.cache; echo out > artifact.txt; echo edit >> a.txt", Commit: first})
	if got.Status != Passed {
		t.Fatalf("first job: %+v", got)
	}
	gittest.Write(t, root, "a.txt", "a2\n")
	gittest.Commit(t, root, "second")
	second := gittest.Git(t, root, "rev-parse", "HEAD")
	got = r.Run(context.Background(), Job{ID: "j2", Command: `test -f deps.cache && test ! -f artifact.txt && test "$(cat a.txt)" = a2`, Commit: second})
	if got.Status != Passed || got.Lane != 1 {
		t.Errorf("the reused lane did not hold the second commit with the cache alone kept: %+v\n%s", got, logOf(t, r))
	}
}

func TestABrokenLaneIsMadeAfresh(t *testing.T) {
	root, head := project(t, true)
	path := calls(t)
	r := testRunner(t, root)
	r.Run(context.Background(), Job{ID: "j1", Command: "true", Commit: head})
	os.Remove(filepath.Join(r.Pool.LaneDir(1), ".git"))
	got := r.Run(context.Background(), Job{ID: "j2", Command: "test -f a.txt", Commit: head})
	if got.Status != Passed {
		t.Fatalf("a job in a broken lane: %+v\n%s", got, logOf(t, r))
	}
	if want := []string{"slots", "up", "run", "slots", "up", "run"}; !slices.Equal(readCalls(path), want) {
		t.Errorf("the new lane was not brought up: calls = %v, want %v", readCalls(path), want)
	}
	if got := gittest.Git(t, r.Pool.LaneDir(1), "rev-parse", "HEAD"); got != head {
		t.Errorf("the new lane is at %s, want %s", got, head)
	}
}

func TestACommitThatIsNotThereIsALaneError(t *testing.T) {
	root, _ := project(t, false)
	r := testRunner(t, root)
	got := r.Run(context.Background(), Job{ID: "j1", Command: "true", Commit: strings.Repeat("0", 40)})
	if got.Status != LaneError || !strings.Contains(got.Reason, "checking") {
		t.Errorf("%+v", got)
	}
}

func TestAJobWithNoCommitRunsInTheWorkingTree(t *testing.T) {
	root, _ := project(t, false)
	r := testRunner(t, root)
	got := r.Run(context.Background(), Job{ID: "j1", Command: `echo "$BATON_ROOT" > here.txt`})
	if got.Status != Passed || got.Lane != 0 {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "here.txt")); strings.TrimSpace(string(b)) != root {
		t.Errorf("here.txt = %q, want %q", b, root)
	}
	if _, err := os.Stat(r.Pool.Dir()); err == nil {
		t.Error("a job with no commit made a lane")
	}
}

// A worker's await runs in the worker's own worktree, while BATON_ROOT still names the project's checkout.
func TestAJobWithNoCommitRunsInItsWorkDir(t *testing.T) {
	root, _ := project(t, false)
	tree := t.TempDir()
	r := testRunner(t, root)
	r.WorkDir = tree
	got := r.Run(context.Background(), Job{ID: "j1", Command: `echo "$BATON_ROOT" > here.txt`})
	if got.Status != Passed || got.Lane != 0 {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(tree, "here.txt")); strings.TrimSpace(string(b)) != root {
		t.Errorf("here.txt = %q, want %q", b, root)
	}
	if _, err := os.Stat(filepath.Join(root, "here.txt")); err == nil {
		t.Error("the job ran in the checkout")
	}
}

// A gate run Here runs in its WorkDir with no lane, even with a lane script, and checks nothing out: it
// runs on what the tree has, which is not the commit it names here.
func TestAGateHereRunsInItsWorkDir(t *testing.T) {
	root, head := project(t, true)
	calls := calls(t)
	gittest.Write(t, root, "a.txt", "changed\n")
	r := testRunner(t, root)
	r.WorkDir = root
	got := r.Run(context.Background(), Job{ID: "j1", Command: "grep -q changed a.txt", Commit: head, Here: true})
	if got.Status != Passed || got.Lane != 0 {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(r.Pool.Dir()); err == nil {
		t.Error("a gate run here made a lane")
	}
	if c := readCalls(calls); len(c) != 0 {
		t.Errorf("a gate run here called the lane script: %q", c)
	}
	if log := logOf(t, r); !strings.Contains(log, "job j1: grep -q changed a.txt, on "+head[:12]+" in "+root+", with no lane") {
		t.Errorf("log:\n%s", log)
	}
}

// A gate run Here in a worktree outside the project's checkout (a phase agent's) holds one of the
// project's lanes while it runs, as a gate in a lane does, with BATON_LANE set to it: two such gates with
// one slot run in turn, the second waiting as a gate in a lane waits. It checks nothing out in the lane,
// and of the lane script only asks how many slots there are.
func TestAGateHereInAWorktreeHoldsALane(t *testing.T) {
	root, head := project(t, true)
	path := calls(t)
	pool := Pool{Home: t.TempDir(), Root: root}
	busy := filepath.Join(t.TempDir(), "busy") // there while a gate runs
	// Each gate holds its lane until the second gate waits for it (go), so the wait never rests on timing; at
	// most 30 seconds, so a second gate that never waits fails the test rather than hanging it.
	goFile := filepath.Join(t.TempDir(), "go")
	gate := fmt.Sprintf(`test ! -e %[1]s && touch %[1]s && echo "$BATON_LANE $BATON_LANE_DIR" > lane.txt && i=0 && `+
		`while [ ! -e %[2]s ] && [ "$i" -lt 3000 ]; do sleep 0.01; i=$((i + 1)); done && rm %[1]s`, busy, goFile)
	trees := []string{t.TempDir(), t.TempDir()}
	runner := func(tree string) *Runner {
		r := testRunner(t, root)
		r.Pool, r.WorkDir = pool, tree
		return r
	}

	first := make(chan Result)
	go func() {
		first <- runner(trees[0]).Run(context.Background(), Job{ID: "P1.j1", Command: gate, Commit: head, Here: true})
	}()
	for deadline := time.Now().Add(30 * time.Second); !isFile(busy); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first gate never ran")
		}
	}
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	r := runner(trees[1])
	r.Now = func() time.Time { return at }
	type wait struct {
		since        time.Time
		slots, ahead int
	}
	var waits []wait
	r.LaneWait = func(since time.Time, slots, ahead int) {
		waits = append(waits, wait{since, slots, ahead})
		if err := os.WriteFile(goFile, nil, 0o644); err != nil {
			t.Error(err)
		}
	}
	second := r.Run(context.Background(), Job{ID: "P2.j1", Command: gate, Commit: head, Here: true})
	for i, got := range []Result{<-first, second} {
		if got.Status != Passed || got.Lane != 1 {
			t.Errorf("gate %d: %+v", i+1, got)
		}
		if b, _ := os.ReadFile(filepath.Join(trees[i], "lane.txt")); strings.TrimSpace(string(b)) != "1 "+trees[i] {
			t.Errorf("gate %d saw BATON_LANE and BATON_LANE_DIR %q", i+1, b)
		}
	}
	if !slices.Equal(waits, []wait{{at, 1, 0}, {time.Time{}, 1, 0}}) {
		t.Errorf("told of the wait %+v", waits)
	}
	if log := logOf(t, r); !strings.Contains(log, "every lane is in use (1); waiting for one") || !strings.Contains(log, "in "+trees[1]+", holding lane 1") {
		t.Errorf("log:\n%s", log)
	}
	if want := []string{"slots", "slots"}; !slices.Equal(readCalls(path), want) {
		t.Errorf("calls = %v, want %v", readCalls(path), want)
	}
	if isFile(filepath.Join(pool.LaneDir(1), ".git")) {
		t.Error("a gate run here checked a lane out")
	}

	// A gate in a lane waits for one run here.
	held, _ := pool.TryAcquire(1)
	time.AfterFunc(200*time.Millisecond, func() { held.Release() })
	if got := runner(trees[0]).Run(context.Background(), Job{ID: "j2", Command: "true", Commit: head}); got.Status != Passed || got.Lane != 1 {
		t.Errorf("a gate in a lane: %+v", got)
	}
}

// A job waiting for a lane learns how many jobs that began to wait before it wait still: a waiter whose
// process ended counts no more, and its file goes.
func TestWaitersCountThoseAhead(t *testing.T) {
	p := Pool{Home: t.TempDir(), Root: t.TempDir()}
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	wait := func(since time.Time, want int) *Waiter {
		t.Helper()
		w, ahead := p.Wait(since)
		if w == nil || ahead != want {
			t.Fatalf("waiting since %s: %v, %d ahead, want %d", since.Format(time.TimeOnly), w, ahead, want)
		}
		return w
	}
	a := wait(at, 0)
	b := wait(at.Add(time.Second), 1)
	for range 9 { // the tenth waiter of this process is still told from the first
		waits.Add(1)
	}
	c := wait(at, 1) // begun at the same instant as a, after it
	a.Done()
	d := wait(at.Add(2*time.Second), 2)
	stale := filepath.Join(p.Dir(), "waits", fmt.Sprintf("%020d-%010d-%010d", at.Add(-time.Hour).UnixNano(), 1, 1))
	os.WriteFile(stale, nil, 0o644)
	e := wait(at.Add(3*time.Second), 3)
	if _, err := os.Stat(stale); err == nil {
		t.Error("a waiter no process holds is still there")
	}
	for _, w := range []*Waiter{b, c, d, e, nil} {
		w.Done()
	}
	if left, _ := os.ReadDir(filepath.Join(p.Dir(), "waits")); len(left) != 0 {
		t.Errorf("waiters left: %v", left)
	}
}

func TestRunnerArgs(t *testing.T) {
	for args, want := range map[string]bool{
		"/home/x/.baton/bin/baton lane-run sess-1 j3":          true,
		"/Users/A B/.baton/bin/baton lane-run sess-1/P3 P3.j1": true,
		"./dist/baton lane-run":                                false,
		"sleep 300":                                            false,
		"vim lane-run":                                         false,
		"":                                                     false,
	} {
		if got := runnerArgs(strings.Fields(args)); got != want {
			t.Errorf("runnerArgs(%q) = %v, want %v", args, got, want)
		}
	}
}
