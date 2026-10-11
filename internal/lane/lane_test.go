package lane

import (
	"context"
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

	// A job waits for a lane, and takes the one that comes free.
	r := testRunner(t, root)
	r.Pool = p
	held, _ := p.TryAcquire(1)
	time.AfterFunc(300*time.Millisecond, func() { held.Release() })
	got := r.Run(context.Background(), Job{ID: "j1", Command: "true", Commit: head})
	if got.Status != Passed || got.Lane != 1 {
		t.Errorf("a job that waited for a lane: %+v", got)
	}
	if log := logOf(t, r); !strings.Contains(log, "every lane is in use (1); waiting for one") {
		t.Errorf("the log does not say the job waited:\n%s", log)
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
