package cli

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// cleanRig is a project as sessions leave it, for baton clean: three runs, each with a worker on P1 and
// a job, and what lies around them.
//
//   - run-a was abandoned an hour ago: its worker on P1 is suspended, with a commit that never landed and
//     work it never committed; its sub-run on P2 has a worktree but no worker on the roster (an older
//     baton wiped it); job j1's pid now belongs to another program, j2's is still its runner;
//   - run-live is live, and run-recent ended a minute ago: everything of theirs survives;
//   - a stray worktree of a run that is gone, a lane nobody holds (and one up by run-live's host),
//     temporary files, terminal records, and processes in the injected table.
type cleanRig struct {
	t                    *testing.T
	now                  time.Time
	repo, home           string
	proj                 *state.Project
	pl                   plan.Plan
	worker, orphan       string // run-a's worktrees on P1 and P2
	stray                string
	live, recent         string // the live and the recent run's worktrees
	table                []procs.Proc
	stopped, terminated  []int
	runners              map[int]bool
	oldRecord, newRecord string
	oldTemps, newTemp    []string
}

func newCleanRig(t *testing.T) *cleanRig {
	gittest.Isolate(t)
	x := &cleanRig{t: t, now: time.Now().Truncate(time.Second), repo: leftover.Resolved(t.TempDir()), home: leftover.Resolved(t.TempDir()),
		runners: map[int]bool{4343: true, 7001: true, 8001: true}}
	gittest.Init(t, x.repo)
	gittest.Git(t, x.repo, "checkout", "-q", "-b", "main")
	gittest.Write(t, x.repo, "a.txt", "a\n")
	gittest.Commit(t, x.repo, "first")
	var err error
	if x.proj, err = state.OpenProject(filepath.Join(x.repo, ".baton"), func() time.Time { return x.now }); err != nil {
		t.Fatal(err)
	}
	state.ExcludeFromGit(x.repo)
	x.pl = plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}}, {ID: "P2", Title: "two", After: []string{}}}}
	trees := filepath.Join(x.home, "worktrees", lane.Key(x.repo))

	// run-a: abandoned an hour ago.
	hour := x.now.Add(-time.Hour)
	x.worker = filepath.Join(trees, "run-a", "P1")
	x.addTree(x.worker, "baton/run-a/P1")
	gittest.Write(t, x.worker, "one.txt", "committed\n")
	gittest.Commit(t, x.worker, "P1's work")
	gittest.Write(t, x.worker, "draft.txt", "never committed\n")
	x.orphan = filepath.Join(trees, "run-a", "P2")
	x.addTree(x.orphan, "baton/run-a/P2")
	x.run("run-a", "host-a", 999991, hour, []rigWorker{{"P1", x.worker, state.WorkerSuspended, hour}, {"P2", x.orphan, "", hour}},
		state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, PID: 4242, PGID: 4242},
		state.Job{ID: "j2", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, PID: 4343, PGID: 4343})

	// run-live: its host beat 5 seconds ago.
	x.live = filepath.Join(trees, "run-live", "P1")
	os.MkdirAll(x.live, 0o755)
	x.run("run-live", "host-live", 999992, x.now.Add(-5*time.Second), []rigWorker{{"P1", x.live, state.WorkerWorking, x.now}},
		state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, PID: 7001, PGID: 7000})

	// run-recent: its host was killed a minute ago.
	minute := x.now.Add(-time.Minute)
	x.recent = filepath.Join(trees, "run-recent", "P1")
	os.MkdirAll(x.recent, 0o755)
	x.run("run-recent", "host-recent", 999993, minute, []rigWorker{{"P1", x.recent, state.WorkerSuspended, minute}},
		state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, PID: 8001, PGID: 8001})

	// A worktree of a run that is gone, which git still knows.
	x.stray = filepath.Join(trees, "gone-run", "P5")
	os.MkdirAll(filepath.Dir(x.stray), 0o755)
	gittest.Git(t, x.repo, "worktree", "add", "-q", "--detach", x.stray, "main")
	gittest.Write(t, x.stray, "left.txt", "never committed\n")

	// Lane 1 nobody holds; lane 2 is up by run-live's host.
	pool := lane.Pool{Home: x.home, Root: x.repo}
	for n, up := range map[int]string{1: "", 2: "host-live"} {
		os.MkdirAll(pool.LaneDir(n), 0o755)
		os.WriteFile(filepath.Join(pool.Dir(), fmt.Sprintf("%d.lock", n)), nil, 0o644)
		if up != "" {
			os.WriteFile(filepath.Join(pool.Dir(), fmt.Sprintf("%d.up", n)), []byte(up+"\n"), 0o644)
		}
	}

	// Temporary files: two that interrupted writes left long ago, one being written now.
	x.oldTemps = []string{filepath.Join(x.proj.Dir, "runs", "run-a", ".state.json.tmp-123"), filepath.Join(x.proj.Dir, "runs", "run-a", "jobs", ".j1.log.tmp-9")}
	for _, p := range x.oldTemps {
		x.file(p, hour)
	}
	x.newTemp = []string{filepath.Join(x.proj.Dir, "runs", "run-live", ".state.json.tmp-456")}
	x.file(x.newTemp[0], x.now)

	// Terminal records: one from two days ago, one from today.
	x.oldRecord, x.newRecord = filepath.Join(x.home, "elevate", "ttys001.json"), filepath.Join(x.home, "elevate", "ttys002.json")
	x.file(x.oldRecord, x.now.Add(-48*time.Hour))
	x.file(x.newRecord, x.now)

	env := func(inst string) map[string]string {
		return map[string]string{"BATON_DIR": x.proj.Dir, "BATON_INSTANCE": inst}
	}
	lanes := pool.Dir()
	x.table = []procs.Proc{
		{PID: 5001, PGID: 5001, Comm: "node", Cwd: x.worker, Env: env("host-a-P1")},                                                             // a dev server in run-a's worker's worktree: stopped
		{PID: 5002, PGID: 5002, Comm: "node", Cwd: x.repo, Env: env("host-a")},                                                                  // run-a's lead's dev server: listed
		{PID: 5003, PGID: 7000, Comm: "sh", Cwd: pool.LaneDir(2)},                                                                               // run-live's gate: untouched
		{PID: 5004, PGID: 5004, Comm: "node", Cwd: x.repo, Env: env("host-live")},                                                               // run-live's: untouched
		{PID: 5005, PGID: 5005, Comm: "node", Cwd: x.recent, Env: env("host-recent-P1")},                                                        // run-recent's: untouched
		{PID: 5006, PGID: 5006, Comm: "node", Cwd: "/elsewhere", Env: map[string]string{"BATON_DIR": "/other/.baton", "BATON_INSTANCE": "zzz"}}, // another project's
		{PID: 5007, PGID: 5002, Comm: "sleep", Cwd: x.repo},                                                                                     // hidden, in 5002's group: listed
		{PID: 5008, PGID: 5008, Comm: "sleep", Cwd: x.worker},                                                                                   // hidden, in run-a's worktree: listed
		{PID: 5009, PGID: 5009, Comm: "sleep", Cwd: x.live},                                                                                     // hidden, in run-live's worktree: untouched
		{PID: 5010, PGID: 5010, Comm: "zsh", Cwd: lanes},                                                                                        // hidden, in the lanes: listed
		{PID: 4242, PGID: 4242, Comm: "python3", Cwd: "/", Env: env("host-a")},                                                                  // j1's pid, now another program's: untouched
		{PID: 999992, PGID: 999992, Comm: "baton"},                                                                                              // run-live's host
		{PID: 6100, PPID: 6000, PGID: 6100, Comm: "zsh", Cwd: x.repo},                                                                           // the shell that runs baton clean
		{PID: 6000, PPID: 1, PGID: 6000, Comm: "node", Cwd: x.repo, Env: env("host-a")},                                                         // and its parent, of run-a's session
		{PID: 6101, PPID: 6100, PGID: 6100, Comm: "node", Cwd: x.worker, Env: env("host-a-P1")},                                                 // in the shell's own job
	}
	return x
}

func (x *cleanRig) addTree(dir, branch string) {
	os.MkdirAll(filepath.Dir(dir), 0o755)
	if _, err := gitx.AddBranch(context.Background(), x.repo, dir, branch, "main"); err != nil {
		x.t.Fatal(err)
	}
}

func (x *cleanRig) file(path string, at time.Time) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("x"), 0o644)
	os.Chtimes(path, at, at)
}

// rigWorker is a worker of a run in the rig: its phase, its worktree, and its status since when ("" for a
// sub-run with no worker on the roster).
type rigWorker struct {
	phase, tree, status string
	since               time.Time
}

// run makes run id with a plan, whose host (instance, pid) beat last at beat, with workers and jobs.
// Each worker's sub-run is made first, as the coordinator does, and its host beat last at beat too.
func (x *cleanRig) run(id, instance string, pid int, beat time.Time, workers []rigWorker, jobs ...state.Job) {
	t := x.t
	s, err := x.proj.Bind(state.Binding{Session: id, Instance: instance})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range workers {
		sub, err := x.proj.NewWorker(id, w.phase, instance, w.tree)
		if err != nil {
			t.Fatal(err)
		}
		sub.Update(func(st *state.State) error { st.Owner.Heartbeat = beat; return nil })
	}
	s.WritePlan(x.pl)
	s.Update(func(st *state.State) error {
		*st = state.Reattach(*st, x.pl, beat, state.Origin{})
		st.RunBranch = "main"
		st.Owner = &state.Owner{Instance: instance, PID: pid, Started: beat, Heartbeat: beat}
		for _, w := range workers {
			if w.status == "" {
				continue
			}
			ref := state.WorkerRef(id, w.phase)
			if err := state.Assign(st, x.pl, state.Worker{Phase: w.phase, Ref: ref,
				Worktree: w.tree, Branch: "baton/" + ref, Base: gitx.RevParse(x.repo, "refs/heads/main")}, w.since); err != nil {
				t.Fatal(err)
			}
			e := &st.Workers[len(st.Workers)-1]
			e.Status, e.Since = w.status, w.since
		}
		st.Jobs = append(st.Jobs, jobs...)
		return nil
	})
	os.Chtimes(filepath.Join(s.Dir, "state.json"), beat, beat)
}

// cleaner is baton clean run from a shell (env adds to its environment) on the rig, with the rig's
// process table, and fakes that record what it stops.
func (x *cleanRig) cleaner(dry bool, env map[string]string) *cleaner {
	e := map[string]string{"BATON_HOME": x.home}
	for k, v := range env {
		e[k] = v
	}
	io, _, _ := testIO("", e)
	io.Now = func() time.Time { return x.now }
	c, err := newCleaner(io, x.proj.Dir)
	if err != nil {
		x.t.Fatal(err)
	}
	c.dry, c.parent, c.group = dry, 6100, 6100
	c.procs = func() ([]procs.Proc, error) {
		return slices.DeleteFunc(slices.Clone(x.table), func(p procs.Proc) bool { return slices.Contains(x.stopped, p.PID) }), nil
	}
	c.stop = func(pids []int) []int { x.stopped = append(x.stopped, pids...); return nil }
	c.isRunner = func(pid int) bool { return x.runners[pid] }
	c.terminate = func(pid int) error { x.terminated = append(x.terminated, pid); return nil }
	c.alive = func(int) bool {
		x.t.Error("asked whether a pid is alive, with a process table to look in")
		return true
	}
	return c
}

// clean runs baton clean on the rig, and returns its report and what it did.
func (x *cleanRig) clean(dry bool) (string, *cleaner) {
	c := x.cleaner(dry, nil)
	var out bytes.Buffer
	c.clean(&out)
	return out.String(), c
}

func (x *cleanRig) store(id string) *state.Store {
	s, err := x.proj.ExistingRun(id, "")
	if err != nil {
		x.t.Fatal(err)
	}
	return s
}

func (x *cleanRig) state(id string) state.State {
	st, err := x.store(id).Load()
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

func (x *cleanRig) events(id string) string {
	b, _ := os.ReadFile(filepath.Join(x.store(id).Dir, "events.jsonl"))
	return string(b)
}

func (x *cleanRig) branch(name string) bool { return gitx.RevParse(x.repo, "refs/heads/"+name) != "" }

func (x *cleanRig) snapshot(phase string) bool {
	return slices.ContainsFunc(gitx.Snapshots(x.repo), func(s gitx.Saved) bool { return strings.HasPrefix(s.Ref, gitx.SnapshotRefs+phase+"-") })
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func whats(c *cleaner) []string {
	var out []string
	for _, a := range c.actions {
		out = append(out, a.what)
	}
	return out
}

// baton clean ends what no live session uses, and nothing else: run-a's worker, its orphaned sub-run,
// its jobs and its leftover processes, the stray worktree, the lane nobody holds, old temporary files
// and the old record. Everything of the live run and of the run that ended a minute ago
// survives.
func TestCleanEndsWhatNoLiveSessionUses(t *testing.T) {
	x := newCleanRig(t)
	out, c := x.clean(false)
	a, live, recent := x.state("run-a"), x.state("run-live"), x.state("run-recent")
	w := a.Workers[0]
	pool := lane.Pool{Home: x.home, Root: x.repo}
	for _, check := range []struct {
		name string
		ok   bool
	}{
		{"run-a's worker is retired, and its phase is pending", w.Status == state.WorkerFailed && w.Why == "baton clean" && a.Phases["P1"].Status == state.PhasePending},
		{"its uncommitted work is saved", x.snapshot("P1")},
		{"its worktree is removed", !exists(x.worker)},
		{"its branch is kept for its commit", x.branch("baton/run-a/P1") && w.Unlanded == 1 && w.Branch == "baton/run-a/P1"},
		{"the orphaned sub-run's worktree is removed, and its branch, with nothing on it", !exists(x.orphan) && !x.branch("baton/run-a/P2")},
		{"the stray worktree is removed, its work saved", !exists(x.stray) && x.snapshot("P5") && !strings.Contains(gittest.Git(t, x.repo, "worktree", "list"), x.stray)},
		{"run-a's jobs are interrupted", a.Jobs[0].Status == state.JobInterrupted && a.Jobs[1].Status == state.JobInterrupted && a.Jobs[0].Reason == "baton clean"},
		{"only the runner still a runner is told to stop", reflect.DeepEqual(x.terminated, []int{4343})},
		{"the lane nobody holds is removed, the one up by a live host stays", !exists(pool.LaneDir(1)) && exists(pool.LaneDir(2))},
		{"old temporary files are removed, the one being written stays", !exists(x.oldTemps[0]) && !exists(x.oldTemps[1]) && exists(x.newTemp[0])},
		{"the old terminal record is removed, today's stays", !exists(x.oldRecord) && exists(x.newRecord)},
		{"only the process under baton's home is stopped", reflect.DeepEqual(x.stopped, []int{5001})},
		{"run-live's worker, worktree and job survive", live.Workers[0].Status == state.WorkerWorking && exists(x.live) && live.Jobs[0].Status == state.JobRunning},
		{"run-recent's worker, worktree and job survive", recent.Workers[0].Status == state.WorkerSuspended && exists(x.recent) && recent.Jobs[0].Status == state.JobRunning},
		{"the live run's sub-run survives", len(x.proj.Workers("run-live")) == 1 && exists(x.live)},
		{"the run that ended a minute ago keeps its links and its sub-run", x.proj.HostRun("host-recent") == "run-recent" && len(x.proj.Workers("run-recent")) == 1},
		{"the links of the abandoned run's host are removed", x.proj.HostRun("host-a") == ""},
	} {
		if !check.ok {
			t.Errorf("%s", check.name)
		}
	}
	for _, want := range []string{
		`"kind":"worker_ended"`, `"by":"baton clean"`, `"why":"baton clean"`, `"orphaned":true`,
		`"kind":"cleaned"`, `"workers":["P1"]`, `"subruns":["P2"]`, `"jobs":["j1","j2"]`, `"processes":[5001]`, `"kind":"jobs_stopped"`,
	} {
		if !strings.Contains(x.events("run-a"), want) {
			t.Errorf("no %s in run-a's events:\n%s", want, x.events("run-a"))
		}
	}
	for _, id := range []string{"run-live", "run-recent"} {
		if ev := x.events(id); strings.Contains(ev, "cleaned") || strings.Contains(ev, "worker_ended") {
			t.Errorf("%s's events:\n%s", id, ev)
		}
	}
	for _, want := range []string{
		"\nLeft alone:\n", "run run-live is live (heartbeat 5s ago)", "run run-rece… was active 1m0s ago",
		"\nEnded:\n", "worker P1 of run run-a: uncommitted work saved as refs/baton/snapshots/P1-", "worktree removed; branch baton/run-a/P1 kept: 1 commit that never landed; P1 is pending again in its run",
		"what the worker on P2 of run run-a left (no worker is on it): worktree removed; branch removed",
		"stray worktree " + x.stray + ": uncommitted work saved as", "2 jobs of run run-a (j1, j2): marked interrupted; 1 runner told to stop; pid 4242 of j1 is no longer its runner",
		"lane 1 in " + pool.Dir() + ", which nobody holds: removed", "process 5001 (node) in " + x.worker + ": stopped",
		"2 temporary files that interrupted writes left", "1 terminal record older than a day (ttys001.json in ",
		"\nFound and left running", "process 5002 (node) in " + x.repo + ", left by a session that is gone: kill 5002",
		"process 5007 (sleep) in " + x.repo + ", whose environment is hidden, in the process group of 5002: kill 5007",
		"process 5008 (sleep) in " + x.worker + ", whose environment is hidden: kill 5008", "process 5010 (zsh) in " + pool.Dir() + ", whose environment is hidden: kill 5010",
		"\nKept:\n", "branch baton/run-a/P1: 1 commit that never landed (git branch -D baton/run-a/P1)", "snapshots of uncommitted work",
		"\nbaton: clean\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in the report:\n%s", want, out)
		}
	}
	for _, pid := range []string{"5003", "5004", "5005", "5006", "5009", "4242 (", "999992", "6000", "6100", "6101"} {
		if strings.Contains(out, "process "+pid) {
			t.Errorf("the report names process %s:\n%s", pid, out)
		}
	}
	if len(c.notes) > 0 {
		t.Errorf("notes: %q", c.notes)
	}

	// The second time, there is nothing left to clean.
	x.terminated = nil
	if out, _ := x.clean(false); !strings.HasSuffix(out, "\nbaton: nothing to clean\n") || strings.Contains(out, "\nEnded:") || len(x.terminated) > 0 || !reflect.DeepEqual(x.stopped, []int{5001}) {
		t.Errorf("a second clean:\n%s", out)
	}
}

// fingerprint is every file under dirs (but git's own), with its size and time, and every ref and
// worktree git has: what a dry run must not change.
func (x *cleanRig) fingerprint() string {
	var b strings.Builder
	for _, dir := range []string{x.repo, x.home} {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && d.Name() == ".git" {
				return fs.SkipDir
			}
			if !d.IsDir() && d.Name() == "lock" { // taking a lock records its holder in the file (package state)
				fmt.Fprintf(&b, "%s\n", path)
				return nil
			}
			fi, _ := d.Info()
			if d.IsDir() && runtime.GOOS == "windows" { // NTFS updates a directory's time in its listing lazily
				fmt.Fprintf(&b, "%s/\n", path)
				return nil
			}
			fmt.Fprintf(&b, "%s %v %d %d\n", path, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
			return nil
		})
	}
	b.WriteString(gittest.Git(x.t, x.repo, "for-each-ref"))
	b.WriteString(gittest.Git(x.t, x.repo, "worktree", "list", "--porcelain"))
	return b.String()
}

// A dry run changes nothing, and reports the actions baton clean then takes.
func TestCleanDryRun(t *testing.T) {
	x := newCleanRig(t)
	before := x.fingerprint()
	out, dry := x.clean(true)
	if after := x.fingerprint(); after != before {
		t.Errorf("a dry run changed things:\n%s", diffLines(before, after))
	}
	if len(x.stopped) > 0 || len(x.terminated) > 0 {
		t.Errorf("a dry run stopped %v and told %v to stop", x.stopped, x.terminated)
	}
	for _, want := range []string{"baton clean --dry-run in " + x.repo + ": nothing is changed", "\nWould end:\n",
		"worker P1 of run run-a: would save its uncommitted work (1 file); remove its worktree; keep its branch baton/run-a/P1 (1 commit that never landed); make P1 pending again in its run",
		"what the worker on P2 of run run-a left (no worker is on it): would remove its worktree; remove its branch baton/run-a/P2",
		"2 jobs of run run-a (j1, j2): would mark them interrupted, and tell 1 runner to stop; pid 4242 of j1 is no longer its runner",
		"process 5001 (node) in " + x.worker + ": would stop it", "\nbaton: dry run: nothing was changed (baton clean does the above)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in the dry run's report:\n%s", want, out)
		}
	}
	if len(dry.actions) < 9 {
		t.Fatalf("a dry run planned %d actions:\n%s", len(dry.actions), out)
	}
	_, done := x.clean(false)
	if !reflect.DeepEqual(whats(dry), whats(done)) {
		t.Errorf("a dry run says\n%s\nbaton clean did\n%s", strings.Join(whats(dry), "\n"), strings.Join(whats(done), "\n"))
	}
	if out, _ := x.clean(true); !strings.HasSuffix(out, "\nbaton: nothing to clean\n") {
		t.Errorf("a dry run after a clean:\n%s", out)
	}
}

func diffLines(a, b string) string {
	var out []string
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for _, l := range al {
		if !slices.Contains(bl, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range bl {
		if !slices.Contains(al, l) {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}

// In a session, the session's own run is live: baton clean leaves it alone, and says how to clean it.
// A worker's session is refused it.
func TestCleanInASession(t *testing.T) {
	x := newCleanRig(t)
	c := x.cleaner(true, map[string]string{"BATON_HOST": "1", "BATON_DIR": x.proj.Dir, "BATON_INSTANCE": "host-live", "CLAUDE_CODE_SESSION_ID": "run-live"})
	var out bytes.Buffer
	c.clean(&out)
	if !strings.Contains(out.String(), "run run-live is this session's, and live: /baton drop first to clean the run itself") {
		t.Errorf("the report:\n%s", out.String())
	}

	// In run-live's phase agent on P1, which works in P1's worktree.
	io, stdout, stderr := testIO("", map[string]string{"BATON_HOST": "1", "BATON_DIR": x.proj.Dir,
		"BATON_INSTANCE": "host-live", "CLAUDE_CODE_SESSION_ID": "run-live", "BATON_HOME": x.home})
	io.Dir = x.live
	if code := Main([]string{"clean"}, io); code == 0 || !strings.Contains(stderr.String(), "baton clean belongs to the lead session") || stdout.Len() > 0 {
		t.Errorf("a phase agent's baton clean: exit %d\n%s%s", code, stdout, stderr)
	}
	if code := Main([]string{"clean", "now"}, io); code == 0 {
		t.Error("baton clean took an argument")
	}
}

// BATON_ABANDON_AFTER changes how long a run must have been quiet before baton clean takes it for
// abandoned (tests shorten it): the run whose host was killed a minute ago is cleaned when 30s is
// enough, and left alone, saying how long baton waits, when 5m is not over yet.
func TestCleanWaitsAsLongAsATestAsks(t *testing.T) {
	for _, c := range []struct {
		after, want string
		cleaned     bool
	}{
		{"30s", "worker P1 of run run-rece…: ", true},
		{"5m", "run run-rece… was active 1m0s ago: baton waits until a run has been quiet for 5m0s", false},
	} {
		t.Run(c.after, func(t *testing.T) {
			x := newCleanRig(t)
			var out bytes.Buffer
			x.cleaner(false, map[string]string{"BATON_ABANDON_AFTER": c.after}).clean(&out)
			if !strings.Contains(out.String(), c.want) {
				t.Errorf("no %q in the report:\n%s", c.want, out.String())
			}
			if w := x.state("run-recent").Workers[0]; (w.Status == state.WorkerFailed && w.Why == cleanWhy) != c.cleaned {
				t.Errorf("run-recent's worker: %+v", w)
			}
			if w := x.state("run-live").Workers[0]; w.Status != state.WorkerWorking {
				t.Errorf("run-live's worker: %+v", w)
			}
		})
	}
}

// Where baton reads no process table, the step says it cannot look, and the rest goes on: whether a
// run's host still runs is told from its pid alone.
func TestCleanWithoutAProcessTable(t *testing.T) {
	x := newCleanRig(t)
	c := x.cleaner(false, nil)
	c.procs = func() ([]procs.Proc, error) { return nil, procs.ErrUnsupported }
	c.alive = func(pid int) bool { return pid == 999993 } // run-recent's host
	var out bytes.Buffer
	c.clean(&out)
	if !strings.Contains(out.String(), "\nNot looked at:\n  · leftover processes: baton cannot read the process table on this system\n") || len(x.stopped) > 0 {
		t.Errorf("the report:\n%s", out.String())
	}
	if w := x.state("run-a").Workers[0]; w.Status != state.WorkerFailed {
		t.Errorf("run-a's worker %+v", w)
	}
	if !strings.Contains(out.String(), "run run-rece… has a baton still running (pid 999993), though its heartbeat is 1m0s old") {
		t.Errorf("the report:\n%s", out.String())
	}
}

// A process that carries a gone session's variables is the session's leftover only where the session
// worked: in the project, its worktrees and lanes, or baton's home. One elsewhere (a gpg-agent or
// ssh-agent a hosted shell started, in / or the home directory), or where it cannot be told, is the
// human's own: baton clean neither stops nor lists it, nor what shares its process group.
func TestCleanLeavesTheHumansDaemonsAlone(t *testing.T) {
	x := newCleanRig(t)
	gone := map[string]string{"BATON_DIR": x.proj.Dir, "BATON_INSTANCE": "host-a"}
	lanes := lane.Pool{Home: x.home, Root: x.repo}.Dir()
	cases := []struct {
		name string
		proc procs.Proc
		want string // "stopped", "listed", or "" for neither
	}{
		{"a gpg-agent in /", procs.Proc{PID: 5101, PPID: 1, PGID: 5101, Comm: "gpg-agent", Cwd: "/", Env: gone}, ""},
		{"what shares the gpg-agent's group, its environment hidden", procs.Proc{PID: 5102, PPID: 5101, PGID: 5101, Comm: "scdaemon", Cwd: "/"}, ""},
		{"an ssh-agent in the directory above the project", procs.Proc{PID: 5103, PPID: 1, PGID: 5103, Comm: "ssh-agent", Cwd: filepath.Dir(x.repo), Env: gone}, ""},
		{"one beside the project, whose name starts the same", procs.Proc{PID: 5104, PPID: 1, PGID: 5104, Comm: "node", Cwd: x.repo + "-other", Env: gone}, ""},
		{"one whose working directory cannot be read", procs.Proc{PID: 5105, PPID: 1, PGID: 5105, Comm: "node", Env: gone}, ""},
		{"a dev server in the project", procs.Proc{PID: 5106, PPID: 1, PGID: 5106, Comm: "node", Cwd: filepath.Join(x.repo, "web"), Env: gone}, "listed"},
		{"one in the model's own worktree", procs.Proc{PID: 5107, PPID: 1, PGID: 5107, Comm: "node", Cwd: filepath.Join(x.repo, ".claude", "worktrees", "w1"), Env: gone}, "listed"},
		{"one under baton's home", procs.Proc{PID: 5108, PPID: 1, PGID: 5108, Comm: "node", Cwd: filepath.Join(x.home, "worktrees", "elsewhere"), Env: gone}, "stopped"},
		{"one in the lanes", procs.Proc{PID: 5109, PPID: 1, PGID: 5109, Comm: "node", Cwd: lanes, Env: gone}, "stopped"},
	}
	for _, c := range cases {
		x.table = append(x.table, c.proc)
	}
	out, _ := x.clean(false)
	for _, c := range cases {
		pid := c.proc.PID
		var got string
		switch named, stopped := strings.Contains(out, fmt.Sprintf("process %d (", pid)), slices.Contains(x.stopped, pid); {
		case stopped && strings.Contains(out, fmt.Sprintf("process %d (%s) in %s: stopped", pid, c.proc.Comm, c.proc.Cwd)):
			got = "stopped"
		case !stopped && strings.Contains(out, fmt.Sprintf("process %d (%s) in %s, left by a session that is gone: kill %d", pid, c.proc.Comm, c.proc.Cwd, pid)):
			got = "listed"
		case named || stopped:
			got = fmt.Sprintf("named (%v) or stopped (%v)", named, stopped)
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q\n%s", c.name, got, c.want, out)
		}
	}
}
