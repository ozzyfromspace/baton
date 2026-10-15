package cli

import (
	"context"
	"errors"
	"fmt"
	goio "io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton clean ends what no live session uses, in the project of the working directory, what baton's
// home holds for its checkout (lanes, workers' worktrees), and says what it did. Nothing a run that is not abandoned owns is touched (state.Abandoned): such runs are named and left
// alone. Each step is idempotent, and every step is planned from one look at the project before any is
// taken, so a dry run (--dry-run) reports the same actions, and a second run says there is nothing to
// clean. The steps, in order:
//
//  1. the workers of abandoned runs are retired (loop.RetireWorker);
//  2. what a sub-run of an abandoned run left with no worker on it (loop.RetireOrphan);
//  3. directories under baton's worktrees for the checkout that no run names, of runs abandoned or gone
//     (loop.RemoveStray);
//  4. the unfinished jobs of abandoned runs: marked interrupted, and each runner still running told to stop;
//  5. the lanes nobody holds (lane.Pool.Sweep);
//  6. processes that sessions which are gone left running (spike 28): those working under baton's home
//     are stopped, the others listed with the command to stop each;
//  7. records of runs and sessions that are gone, and temporary files that interrupted writes left;
//  8. elevation records older than a day.
//
// What it keeps is listed, with the command to remove each.

func init() {
	register("clean", "end what no live session uses: workers, jobs, lanes, worktrees, leftover files [--dry-run]", leadOnly("clean", cmdClean))
}

const (
	cleanWhy    = "baton clean" // why, on the record of what it ends
	tempAge     = 10 * time.Minute
	elevateAge  = 24 * time.Hour
	stopGrace   = 5 * time.Second
	cleanGitCap = 30 * time.Second
)

func cmdClean(args []string, io IO) int {
	p, err := parseArgs(args, nil, []string{"dry-run"})
	if err != nil || len(p.pos) > 0 {
		return fail(io, "usage: baton clean [--dry-run]")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(io, "%v", err)
	}
	c, err := newCleaner(io, state.Locate(cwd, io.Env))
	if err != nil {
		return fail(io, "%v", err)
	}
	c.dry = p.bools["dry-run"]
	c.clean(io.Out)
	return 0
}

// cleaner is one baton clean, with what it reads and how it acts on the machine injectable.
type cleaner struct {
	proj     *state.Project
	home     string // baton's home
	now      time.Time
	quiet    time.Duration // how long a run must have been quiet to count as abandoned
	dry      bool
	instance string // the host instance it runs under, recorded on events ("" from a shell)
	self     string // the run of the session it runs in ("" from a shell)
	// parent and group are this process's parent and process group: the command line that runs baton
	// clean, which it never lists.
	parent, group int

	procs     func() ([]procs.Proc, error)
	stop      func(pids []int) []int // stops each process on its own, and returns those still running
	isRunner  func(pid int) bool     // a pid on a job's record is still its runner (lane-run)
	terminate func(pid int) error
	alive     func(pid int) bool // where no process table is read: a run's owner may still run
	snapshot  func(root string, at time.Time, phase, why string) (gitx.Saved, error)

	look    *look
	actions []*cleanAction
	alone   []string // runs (and workers) left alone, and why
	listed  []string // processes found and left running, with the command to stop each
	notes   []string // steps that could not look
	kept    []string // worktrees kept in this clean
}

// newCleaner is baton clean for the project whose state is in dir, which it does not create.
func newCleaner(io IO, dir string) (*cleaner, error) {
	proj := &state.Project{Dir: dir, Root: filepath.Dir(dir), Now: io.Now}
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		var err error
		if proj, err = state.OpenProject(dir, io.Now); err != nil {
			return nil, fmt.Errorf("cannot use %s: %v", dir, err)
		}
	}
	c := &cleaner{proj: proj, home: batonRoot(io), now: io.Now(), quiet: abandonAfter(io), instance: instance(io),
		procs: procs.List, stop: func(pids []int) []int { return procs.Stop(pids, stopGrace) },
		isRunner: lane.IsRunner, terminate: lane.Terminate, alive: lane.Alive, parent: os.Getppid(), group: procs.Group()}
	if hosted(io) {
		if s, ok := proj.Lookup(io.Env("CLAUDE_CODE_SESSION_ID"), c.instance); ok {
			c.self = s.Ref
		}
	}
	return c, nil
}

// abandonAfter is how long a run must have been quiet before baton takes it for abandoned
// (state.Abandoned): state.AbandonAfter, unless a test shortens it (BATON_ABANDON_AFTER).
func abandonAfter(io IO) time.Duration {
	if d, err := time.ParseDuration(io.Env("BATON_ABANDON_AFTER")); err == nil && d > 0 {
		return d
	}
	return state.AbandonAfter
}

// agentRamp is how long after one phase agent of a run started the host lets another start
// (loop.AgentRamp), unless a test shortens it (BATON_AGENT_RAMP).
func agentRamp(io IO) time.Duration {
	if d, err := time.ParseDuration(io.Env("BATON_AGENT_RAMP")); err == nil && d > 0 {
		return d
	}
	return loop.AgentRamp
}

// cleanAction is one thing baton clean ends.
type cleanAction struct {
	what  string        // its subject, the same in a dry run
	plan  string        // what baton clean would do
	do    func() string // does it, and says what it did
	run   string        // the run whose cleaned event records it ("" for none)
	field string        // under which field
	value any
}

// look is what baton clean found before it acted.
type look struct {
	runs      []*cleanRun
	table     []procs.Proc
	tableErr  error
	baton     func(pid int) bool // pid is a running baton
	protected map[string]bool    // instances of runs left alone
	trees     []string           // worktrees and lanes of runs left alone
	named     map[string]bool    // every worktree a sub-run or a roster entry names
	jobPIDs   map[int]bool       // runners and process groups of unfinished jobs
	jobGroups map[int]bool
}

// cleanRun is a run of the project as baton clean found it.
type cleanRun struct {
	state.RunInfo
	subs      []state.RunInfo // its workers' sub-runs
	abandoned bool
	alone     string // why it is left alone
}

// clean does it (or, dry, says what it would do) and reports to w.
func (c *cleaner) clean(w goio.Writer) {
	c.look = c.read()
	for _, step := range []func(){c.workers, c.orphans, c.strays, c.jobs, c.lanes, c.processes, c.records, c.machine} {
		step()
	}
	events := map[string]map[string]any{}
	results := make([]string, len(c.actions))
	for i, a := range c.actions {
		if c.dry {
			results[i] = a.plan
			continue
		}
		results[i] = a.do()
		if a.run != "" && a.field != "" {
			if events[a.run] == nil {
				events[a.run] = map[string]any{"by": cleanWhy}
			}
			list, _ := events[a.run][a.field].([]any)
			if ids, ok := a.value.([]string); ok {
				for _, id := range ids {
					list = append(list, id)
				}
			} else {
				list = append(list, a.value)
			}
			events[a.run][a.field] = list
		}
	}
	for ref, fields := range events {
		if s, err := c.proj.ExistingRun(ref, c.instance); err == nil {
			s.Event("cleaned", fields)
		}
	}
	c.report(w, results)
}

// read takes the one look every step plans from.
func (c *cleaner) read() *look {
	l := &look{protected: map[string]bool{}, named: map[string]bool{}, jobPIDs: map[int]bool{}, jobGroups: map[int]bool{}}
	l.table, l.tableErr = c.procs()
	batons := map[int]bool{}
	for _, p := range l.table {
		if p.Comm == "baton" {
			batons[p.PID] = true
		}
	}
	l.baton = func(pid int) bool {
		if l.tableErr != nil {
			return c.alive(pid) // it cannot be told: it might be
		}
		return batons[pid]
	}
	if c.instance != "" {
		l.protected[c.instance] = true
	}
	for _, info := range c.proj.Runs() {
		r := &cleanRun{RunInfo: info, subs: c.proj.Workers(info.ID)}
		r.abandoned = state.Abandoned(info, nil, l.baton, c.now, c.quiet)
		if !r.abandoned {
			r.alone = c.whyAlone(info, l.baton)
		}
		l.runs = append(l.runs, r)
		for _, e := range info.State.Workers {
			l.named[resolved(e.Worktree)] = true
		}
		for _, s := range r.subs {
			l.named[resolved(s.Worktree)] = true
		}
		for _, st := range append([]state.State{info.State}, states(r.subs)...) {
			for _, j := range st.Unfinished() {
				if j.PID > 0 {
					l.jobPIDs[j.PID] = true
				}
				if j.PGID > 0 {
					l.jobGroups[j.PGID] = true
				}
			}
		}
		for _, s := range r.subs {
			if o := s.State.Owner; o != nil && (!r.abandoned || o.Live(c.now)) { // a worker that runs is left alone (step 1)
				l.protected[o.Instance] = true
				l.trees = append(l.trees, s.Worktree)
			}
		}
		if r.abandoned {
			continue
		}
		if o := info.State.Owner; o != nil {
			l.protected[o.Instance] = true
		}
		for _, e := range info.State.LiveWorkers() {
			if o := info.State.Owner; o != nil {
				l.protected[state.WorkerInstance(o.Instance, e.Phase)] = true
			}
			l.trees = append(l.trees, e.Worktree)
		}
		for _, s := range r.subs {
			l.trees = append(l.trees, s.Worktree)
		}
	}
	for _, ln := range c.pool().Lanes() {
		if ln.Busy || ln.UpBy != "" && l.protected[ln.UpBy] {
			l.trees = append(l.trees, ln.Dir)
		}
	}
	delete(l.named, "")
	return l
}

func states(runs []state.RunInfo) []state.State {
	out := make([]state.State, len(runs))
	for i, r := range runs {
		out[i] = r.State
	}
	return out
}

// whyAlone says why a run that is not abandoned is left alone.
func (c *cleaner) whyAlone(r state.RunInfo, baton func(int) bool) string {
	o := r.State.Owner
	switch {
	case r.Live(c.now):
		why := fmt.Sprintf("is live (heartbeat %s ago)", ago(c.now.Sub(o.Heartbeat)))
		if r.ID == c.self {
			why = "is this session's, and live: /baton drop first to clean the run itself"
		}
		return why
	case o != nil && o.PID > 0 && baton(o.PID):
		return fmt.Sprintf("has a baton still running (pid %d), though its heartbeat is %s old", o.PID, ago(c.now.Sub(o.Heartbeat)))
	}
	return fmt.Sprintf("was active %s ago: baton waits until a run has been quiet for %s", ago(c.now.Sub(r.Updated)), ago(c.quiet))
}

func ago(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Hour).String()
}

// runName names a run in a report: its id's first 8 characters (a session id is 36).
func runName(ref string) string {
	run, phase, sub := strings.Cut(ref, "/")
	if len(run) > 9 {
		run = run[:8] + "…"
	}
	if sub {
		return run + "/" + phase
	}
	return run
}

// holds reports a run that holds something baton clean ends when it is abandoned.
func (r *cleanRun) holds() bool {
	if len(r.State.LiveWorkers()) > 0 || len(r.State.Unfinished()) > 0 {
		return true
	}
	return slices.ContainsFunc(r.subs, func(s state.RunInfo) bool { return isDir(s.Worktree) || len(s.State.Unfinished()) > 0 })
}

func (c *cleaner) add(a *cleanAction) { c.actions = append(c.actions, a) }

func (c *cleaner) store(ref string) *state.Store {
	s, err := c.proj.ExistingRun(ref, c.instance)
	if err != nil {
		return nil
	}
	return s
}

// workers: step 1.
func (c *cleaner) workers() {
	for _, r := range c.look.runs {
		if !r.abandoned {
			if r.alone != "" && (r.Live(c.now) || r.holds()) {
				c.alone = append(c.alone, fmt.Sprintf("run %s %s", runName(r.ID), r.alone))
			}
			continue
		}
		run := c.store(r.ID)
		if run == nil {
			continue
		}
		for _, e := range r.State.LiveWorkers() {
			what := fmt.Sprintf("worker %s of run %s", e.Phase, runName(r.ID))
			if sub := subRun(r, e.Ref); sub != nil && sub.State.Owner.Live(c.now) {
				c.alone = append(c.alone, what+" runs in another baton session")
				continue
			}
			if !state.Abandoned(r.RunInfo, &e, c.look.baton, c.now, c.quiet) {
				c.alone = append(c.alone, fmt.Sprintf("%s was active %s ago", what, ago(c.now.Sub(latest(e.Since, e.Ended)))))
				continue
			}
			c.add(&cleanAction{what: what, plan: c.wouldRetire(r, e, true), run: r.ID, field: "workers", value: e.Phase,
				do: func() string {
					before := c.before(e)
					got := loop.RetireWorker(loop.Retirement{Project: c.proj, Home: c.home, Run: run, Entry: e, Why: cleanWhy, By: cleanWhy, Now: c.now, Snapshot: c.snapshot})
					return c.retired(got, before, e.Phase+" is pending again in its run")
				}})
		}
	}
}

func subRun(r *cleanRun, ref string) *state.RunInfo {
	for i := range r.subs {
		if r.subs[i].ID == ref {
			return &r.subs[i]
		}
	}
	return nil
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// orphans: step 2, what a sub-run of an abandoned run left with no worker on it.
func (c *cleaner) orphans() {
	for _, r := range c.look.runs {
		if !r.abandoned {
			continue
		}
		run := c.store(r.ID)
		if run == nil {
			continue
		}
		for _, s := range r.subs {
			e, ok := c.orphan(r, s)
			if !ok {
				continue
			}
			c.add(&cleanAction{what: fmt.Sprintf("what the worker on %s of run %s left (no worker is on it)", e.Phase, runName(r.ID)),
				plan: c.wouldRetire(r, e, false), run: r.ID, field: "subruns", value: e.Phase,
				do: func() string {
					before := c.before(e)
					got := loop.RetireOrphan(loop.Retirement{Project: c.proj, Home: c.home, Run: run, Entry: e, Why: cleanWhy, By: cleanWhy, Now: c.now, Snapshot: c.snapshot})
					return c.retired(got, before, "")
				}})
		}
	}
}

// orphan describes sub-run s of run r as loop.RetireOrphan takes it, when it has a worktree and no live
// worker on r's roster, and its worktree was not kept when its worker was retired (that one is listed).
func (c *cleaner) orphan(r *cleanRun, s state.RunInfo) (state.Worker, bool) {
	var last *state.Worker
	for i := range r.State.Workers {
		if w := &r.State.Workers[i]; w.Ref == s.ID {
			if w.Live() {
				return state.Worker{}, false // step 1's
			}
			last = w
		}
	}
	if !isDir(s.Worktree) || last != nil && last.WorktreeKept != "" {
		return state.Worker{}, false
	}
	_, phase, _ := strings.Cut(s.ID, "/")
	e := state.Worker{Phase: phase, Ref: s.ID, Branch: "baton/" + s.ID, Generation: -1}
	if last != nil {
		e = *last
	}
	e.Worktree = s.Worktree
	return e, true
}

// before is what a worker had before it was retired, for saying what retiring it did.
type had struct{ tree, branch bool }

func (c *cleaner) before(e state.Worker) had {
	return had{isDir(e.Worktree), e.Branch != "" && gitx.RevParse(c.proj.Root, "refs/heads/"+e.Branch) != ""}
}

// retired says what retiring a worker did.
func (c *cleaner) retired(got loop.Retired, before had, phase string) string {
	if got.Skipped != "" {
		return "left alone: " + got.Skipped
	}
	var did []string
	if got.Snapshot != "" {
		did = append(did, "uncommitted work saved as "+got.Snapshot)
	}
	switch {
	case got.Kept != "":
		did = append(did, "worktree kept: "+got.Kept)
	case before.tree:
		did = append(did, "worktree removed")
	}
	if len(got.Jobs) > 0 {
		did = append(did, "stopped "+strings.Join(got.Jobs, ", "))
	}
	switch {
	case got.Unlanded > 0:
		did = append(did, fmt.Sprintf("branch %s kept: %s that never landed", got.Branch, count(got.Unlanded, "commit", "commits")))
	case got.Branch != "":
		did = append(did, "branch "+got.Branch+" kept")
	case before.branch:
		did = append(did, "branch removed")
	}
	if phase != "" {
		did = append(did, phase)
	}
	if len(did) == 0 {
		return "nothing was left of it"
	}
	return strings.Join(did, "; ")
}

// wouldRetire says what retiring worker e of run r (or what its sub-run left) would do.
func (c *cleaner) wouldRetire(r *cleanRun, e state.Worker, live bool) string {
	var would []string
	co := c.proj.Root
	if isDir(e.Worktree) {
		if dirty, err := gitx.Dirty(e.Worktree); err == nil && len(dirty) > 0 {
			would = append(would, fmt.Sprintf("save its uncommitted work (%s)", count(len(dirty), "file", "files")))
		}
		if gitx.Within(e.Worktree, c.worktrees()) {
			would = append(would, "remove its worktree")
		} else {
			would = append(would, "keep its worktree, which is not under baton's")
		}
	}
	if sub := subRun(r, e.Ref); sub != nil {
		if jobs := sub.State.Unfinished(); len(jobs) > 0 {
			would = append(would, "stop "+strings.Join(jobIDs(jobs), ", "))
		}
	}
	if e.Branch != "" && gitx.RevParse(co, "refs/heads/"+e.Branch) != "" {
		into := e.Base
		if r.State.RunBranch != "" {
			into = "refs/heads/" + r.State.RunBranch
		}
		ctx, cancel := context.WithTimeout(context.Background(), cleanGitCap)
		n, err := gitx.Ahead(ctx, co, into, "refs/heads/"+e.Branch)
		cancel()
		switch {
		case err != nil:
			would = append(would, "keep its branch "+e.Branch)
		case n > 0 && (e.Generation < r.State.Generation || r.State.Mode == state.ModeIdle):
			would = append(would, fmt.Sprintf("keep its branch %s under baton/%s/kept/ (%s that never landed)", e.Branch, r.ID, count(n, "commit", "commits")))
		case n > 0:
			would = append(would, fmt.Sprintf("keep its branch %s (%s that never landed)", e.Branch, count(n, "commit", "commits")))
		default:
			would = append(would, "remove its branch "+e.Branch)
		}
	}
	if live {
		would = append(would, "make "+e.Phase+" pending again in its run")
	}
	if len(would) == 0 {
		return "would take it off its run's roster"
	}
	return "would " + strings.Join(would, "; ")
}

// strays: step 3, directories under baton's worktrees for the checkout that no run names.
func (c *cleaner) strays() {
	base := c.worktrees()
	runs, _ := os.ReadDir(base)
	for _, rd := range runs {
		if !rd.IsDir() || c.leftAlone(rd.Name()) {
			continue
		}
		phases, _ := os.ReadDir(filepath.Join(base, rd.Name()))
		for _, pd := range phases {
			dir := filepath.Join(base, rd.Name(), pd.Name())
			if !pd.IsDir() || c.look.named[resolved(dir)] {
				continue
			}
			run := c.store(rd.Name())
			a := &cleanAction{what: "stray worktree " + dir, plan: "would remove it", do: func() string {
				snap, kept := loop.RemoveStray(loop.Stray{Home: c.home, Checkout: c.proj.Root, Dir: dir, Run: run, Phase: pd.Name(), Why: cleanWhy,
					Now: c.now, Snapshot: c.snapshot})
				switch {
				case kept != "":
					c.kept = append(c.kept, fmt.Sprintf("worktree %s: %s (save what you need from it, then: rm -rf %s)", dir, kept, dir))
					return "kept: " + kept
				case snap != "":
					return "uncommitted work saved as " + snap + "; removed"
				}
				return "removed"
			}}
			if dirty, err := gitx.Dirty(dir); err == nil && len(dirty) > 0 && isFile(filepath.Join(dir, ".git")) {
				a.plan = fmt.Sprintf("would save its uncommitted work (%s), and remove it", count(len(dirty), "file", "files"))
			}
			if run != nil {
				a.run, a.field, a.value = run.Ref, "worktrees", dir
			}
			c.add(a)
		}
	}
}

// worktrees is where baton keeps the workers' worktrees for the checkout, by its real path.
func (c *cleaner) worktrees() string {
	return filepath.Join(resolved(c.home), "worktrees", lane.Key(c.proj.Root))
}

// leftAlone reports a run on record that is not abandoned.
func (c *cleaner) leftAlone(id string) bool {
	return slices.ContainsFunc(c.look.runs, func(r *cleanRun) bool { return r.ID == id && !r.abandoned })
}

// jobs: step 4, the unfinished jobs of abandoned runs, and of their sub-runs that steps 1 and 2 leave.
func (c *cleaner) jobs() {
	for _, r := range c.look.runs {
		if !r.abandoned {
			continue
		}
		c.stopJobs(r.ID, r.State)
		for _, s := range r.subs {
			if _, orphaned := c.orphan(r, s); !orphaned && !liveOn(r.State, s.ID) {
				c.stopJobs(s.ID, s.State)
			}
		}
	}
}

func liveOn(st state.State, ref string) bool {
	return slices.ContainsFunc(st.Workers, func(w state.Worker) bool { return w.Ref == ref && w.Live() })
}

func (c *cleaner) stopJobs(ref string, st state.State) {
	jobs := st.Unfinished()
	if len(jobs) == 0 {
		return
	}
	s := c.store(ref)
	if s == nil {
		return
	}
	ids := jobIDs(jobs)
	runners := func() (stop []int, gone []string) {
		for _, j := range jobs {
			switch {
			case j.PID == 0:
			case c.isRunner(j.PID):
				stop = append(stop, j.PID)
			default:
				gone = append(gone, fmt.Sprintf("pid %d of %s is no longer its runner, and is left alone", j.PID, j.ID))
			}
		}
		return stop, gone
	}
	stop, gone := runners()
	plan := "would mark them interrupted"
	if len(stop) > 0 {
		plan += fmt.Sprintf(", and tell %s to stop", count(len(stop), "runner", "runners"))
	}
	run, _, _ := strings.Cut(ref, "/")
	c.add(&cleanAction{what: fmt.Sprintf("%s of run %s (%s)", count(len(jobs), "job", "jobs"), runName(ref), strings.Join(ids, ", ")),
		plan: strings.Join(append([]string{plan}, gone...), "; "), run: run, field: "jobs", value: ids,
		do: func() string {
			var cancelled []state.Job
			s.Update(func(st *state.State) error {
				cancelled = state.CancelJobs(st, func(j state.Job) bool { return slices.Contains(ids, j.ID) }, cleanWhy, c.now)
				return nil
			})
			if len(cancelled) == 0 {
				return "they had ended meanwhile"
			}
			stop, gone := runners()
			for _, pid := range stop {
				c.terminate(pid) // the runner stops its gate's process group, and brings its lane down
			}
			s.Event("jobs_stopped", map[string]any{"jobs": jobIDs(cancelled), "why": cleanWhy})
			did := "marked interrupted"
			if len(stop) > 0 {
				did += fmt.Sprintf("; %s told to stop", count(len(stop), "runner", "runners"))
			}
			return strings.Join(append([]string{did}, gone...), "; ")
		}})
}

func (c *cleaner) pool() lane.Pool { return lane.Pool{Home: c.home, Root: c.proj.Root} }

// lanes: step 5, the lanes nobody holds.
func (c *cleaner) lanes() {
	pool := c.pool()
	live := func(host string) bool { return c.look.protected[host] }
	n := pool.Sweepable(live)
	if len(n) == 0 {
		return
	}
	c.add(&cleanAction{what: fmt.Sprintf("%s %s in %s, which nobody holds", plural(len(n), "lane", "lanes"), laneList(n), pool.Dir()),
		plan: "would bring down any that is up, and remove " + them(len(n)),
		do: func() string {
			w := laneLog(c.proj.Dir)
			defer w.Close()
			switch swept := pool.Sweep(context.Background(), live, w); {
			case slices.Equal(swept, n):
				return "removed"
			case len(swept) == 0:
				return "none was left to remove"
			default:
				return "removed " + laneList(swept)
			}
		}})
}

func laneList(ns []int) string {
	var s []string
	for _, n := range ns {
		s = append(s, strconv.Itoa(n))
	}
	return strings.Join(s, ", ")
}

// processes: step 6, what sessions that are gone left running.
func (c *cleaner) processes() {
	l := c.look
	if l.tableErr != nil {
		why := l.tableErr.Error()
		if !errors.Is(l.tableErr, procs.ErrUnsupported) {
			why = "the process table could not be read: " + why
		}
		c.notes = append(c.notes, "leftover processes: "+why)
		return
	}
	homes := []string{filepath.Clean(c.home), resolved(c.home)}
	ours := []string{filepath.Join(c.home, "worktrees", lane.Key(c.proj.Root)), c.pool().Dir()}
	ours = append(ours, filepath.Join(resolved(c.home), "worktrees", lane.Key(c.proj.Root)), filepath.Join(resolved(c.home), "lanes", lane.Key(c.proj.Root)))
	var trees []string
	for _, t := range l.trees {
		trees = append(trees, filepath.Clean(t), resolved(t))
	}
	project := resolved(c.proj.Dir)
	mine := c.ancestors()
	skip := func(p procs.Proc) bool {
		return l.jobPIDs[p.PID] || l.jobGroups[p.PGID] || mine[p.PID] || c.group > 1 && p.PGID == c.group
	}
	groups := map[int]int{} // a candidate's process group, and one of its processes
	var stopped []procs.Proc
	for _, p := range l.table {
		inst := p.Env["BATON_INSTANCE"]
		if skip(p) || p.Hidden() || inst == "" || l.protected[inst] || resolved(p.Env["BATON_DIR"]) != project {
			continue
		}
		groups[p.PGID] = p.PID
		if under(p.Cwd, homes) {
			stopped = append(stopped, p)
			continue
		}
		c.listed = append(c.listed, fmt.Sprintf("process %d (%s) in %s, left by a session that is gone: kill %d", p.PID, p.Comm, cwdOr(p.Cwd), p.PID))
	}
	for _, p := range l.table {
		if skip(p) || !p.Hidden() || under(p.Cwd, trees) {
			continue
		}
		switch pid, ok := groups[p.PGID]; {
		case ok && pid != p.PID:
			c.listed = append(c.listed, fmt.Sprintf("process %d (%s) in %s, whose environment is hidden, in the process group of %d: kill %d", p.PID, p.Comm, cwdOr(p.Cwd), pid, p.PID))
		case under(p.Cwd, ours):
			c.listed = append(c.listed, fmt.Sprintf("process %d (%s) in %s, whose environment is hidden: kill %d", p.PID, p.Comm, p.Cwd, p.PID))
		}
	}
	stopAll := sync.OnceValue(func() map[int]bool {
		var pids []int
		for _, p := range stopped {
			pids = append(pids, p.PID)
		}
		left := map[int]bool{}
		for _, pid := range c.stop(pids) {
			left[pid] = true
		}
		return left
	})
	for _, p := range stopped {
		a := &cleanAction{what: fmt.Sprintf("process %d (%s) in %s", p.PID, p.Comm, p.Cwd), plan: "would stop it: a session that is gone left it under baton's home",
			do: func() string {
				if stopAll()[p.PID] {
					return fmt.Sprintf("could not be stopped: kill -9 %d", p.PID)
				}
				return "stopped"
			}}
		if run := c.runOf(p.Env["BATON_INSTANCE"]); run != "" {
			a.run, a.field, a.value = run, "processes", p.PID
		}
		c.add(a)
	}
}

// ancestors are the processes that run baton clean: its parent, and theirs.
func (c *cleaner) ancestors() map[int]bool {
	parents := map[int]int{}
	for _, p := range c.look.table {
		parents[p.PID] = p.PPID
	}
	out := map[int]bool{}
	for pid := c.parent; pid > 1 && !out[pid]; pid = parents[pid] {
		out[pid] = true
	}
	return out
}

// runOf is the run whose host or worker ran as instance, if it is on record.
func (c *cleaner) runOf(instance string) string {
	for _, r := range c.look.runs {
		if o := r.State.Owner; o != nil && (o.Instance == instance || slices.ContainsFunc(r.State.Workers, func(w state.Worker) bool {
			return state.WorkerInstance(o.Instance, w.Phase) == instance
		})) {
			return r.ID
		}
	}
	return ""
}

func cwdOr(cwd string) string {
	if cwd == "" {
		return "a directory that cannot be read"
	}
	return cwd
}

// under reports a path inside (or equal to) one of dirs, compared as strings: a removed working
// directory cannot be resolved.
func under(path string, dirs []string) bool {
	if path == "" {
		return false
	}
	path = filepath.Clean(path)
	for _, d := range dirs {
		if d != "" && d != "." && (path == d || strings.HasPrefix(path, d+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// resolved is path with its symbolic links resolved, or cleaned when it is gone.
func resolved(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// records: step 7, records of runs and sessions that are gone, and temporary files.
func (c *cleaner) records() {
	if !isDir(c.proj.Dir) {
		return
	}
	keep := func(instance string) bool { return c.look.protected[instance] }
	if swept := c.proj.Swept(true, keep); len(swept) > 0 {
		c.add(&cleanAction{what: "records of runs and sessions that are gone (" + strings.Join(swept, ", ") + ")", plan: "would remove " + them(len(swept)),
			do: func() string { c.proj.Swept(false, keep); return "removed" }})
	}
	var temps []string
	filepath.WalkDir(c.proj.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), ".") || !strings.Contains(d.Name(), ".tmp-") {
			return nil
		}
		if fi, err := d.Info(); err == nil && c.now.Sub(fi.ModTime()) >= tempAge {
			temps = append(temps, path)
		}
		return nil
	})
	if len(temps) > 0 {
		c.add(&cleanAction{what: count(len(temps), "temporary file", "temporary files") + " that interrupted writes left in " + c.proj.Dir, plan: "would remove " + them(len(temps)),
			do: func() string { return removeAll(temps) }})
	}
}

func removeAll(paths []string) string {
	var failed []string
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return "could not remove: " + strings.Join(failed, "; ")
	}
	return "removed"
}

// machine: step 8, old elevation records.
func (c *cleaner) machine() {
	var old []string
	dir := elevate.Dir(c.home)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") && !strings.HasSuffix(e.Name(), ".json.tmp") {
			continue
		}
		if fi, err := e.Info(); err == nil && c.now.Sub(fi.ModTime()) >= elevateAge {
			old = append(old, filepath.Join(dir, e.Name()))
		}
	}
	if len(old) > 0 {
		c.add(&cleanAction{what: count(len(old), "terminal record", "terminal records") + " older than a day (" + strings.Join(names(old), ", ") + " in " + dir + ")",
			plan: "would remove " + them(len(old)), do: func() string { return removeAll(old) }})
	}
}

func names(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// keeps lists what baton clean keeps, each with the command to remove it.
func (c *cleaner) keeps() []string {
	out := slices.Clone(c.kept)
	co := c.proj.Root
	runs := map[string]*cleanRun{}
	inUse := map[string]bool{}
	for _, r := range c.look.runs {
		runs[r.ID] = r
	}
	for _, info := range c.proj.Runs() {
		for _, e := range info.State.Workers {
			if e.Live() {
				inUse[e.Branch] = true
			}
			if e.WorktreeKept != "" && isDir(e.Worktree) {
				out = append(out, fmt.Sprintf("worktree %s: %s (save what you need from it, then: git worktree remove --force %s)", e.Worktree, e.WorktreeKept, e.Worktree))
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanGitCap)
	defer cancel()
	for _, b := range gitx.Branches(co, "baton/") {
		if inUse[b] {
			continue
		}
		into := "HEAD"
		if parts := strings.SplitN(b, "/", 3); len(parts) == 3 {
			if r := runs[parts[1]]; r != nil && r.State.RunBranch != "" {
				into = "refs/heads/" + r.State.RunBranch
			}
		}
		if n, err := gitx.Ahead(ctx, co, into, "refs/heads/"+b); err == nil && n > 0 {
			out = append(out, fmt.Sprintf("branch %s: %s that never landed (git branch -D %s)", b, count(n, "commit", "commits"), b))
		}
	}
	if n := len(gitx.Snapshots(co)); n > 0 {
		out = append(out, fmt.Sprintf("%s of uncommitted work (git for-each-ref %s lists them; git update-ref -d <ref> removes one)",
			count(n, "snapshot", "snapshots"), gitx.SnapshotRefs))
	}
	drafts, logs := 0, 0
	for _, info := range c.proj.Runs() {
		dir := filepath.Join(c.proj.Dir, "runs", info.ID)
		if isFile(filepath.Join(dir, "events.jsonl")) {
			logs++
		}
		for _, d := range append([]string{dir}, globDirs(filepath.Join(dir, "workers", "*"))...) {
			entries, _ := os.ReadDir(filepath.Join(d, state.DraftsDirName))
			drafts += len(entries)
		}
	}
	if drafts > 0 {
		out = append(out, fmt.Sprintf("%s saved out of the input box (in %s/runs/<run>/drafts/; rm <file> removes one)", count(drafts, "draft", "drafts"), c.proj.Dir))
	}
	if logs > 0 {
		out = append(out, fmt.Sprintf("the history of %s (%s/runs/<run>/events.jsonl; rm -r %s/runs/<run> removes a run that has ended, with its history)",
			count(logs, "run", "runs"), c.proj.Dir, c.proj.Dir))
	}
	return out
}

// them is "it" or "them".
func them(n int) string { return plural(n, "it", "them") }

// count is "1 thing" or "n things".
func count(n int, one, many string) string { return strconv.Itoa(n) + " " + plural(n, one, many) }

func globDirs(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return path != "" && err == nil && fi.IsDir()
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// report says what baton clean did (or would do), what it left alone, and what it keeps.
func (c *cleaner) report(w goio.Writer, results []string) {
	if c.dry {
		fmt.Fprintf(w, "baton clean --dry-run in %s: nothing is changed\n", c.proj.Root)
	} else {
		fmt.Fprintf(w, "baton clean in %s\n", c.proj.Root)
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		for _, l := range lines {
			fmt.Fprintf(w, "  · %s\n", l)
		}
	}
	section("Left alone:", c.alone)
	var done []string
	for i, a := range c.actions {
		done = append(done, a.what+": "+results[i])
	}
	title := "Ended:"
	if c.dry {
		title = "Would end:"
	}
	section(title, done)
	section("Found and left running (stop each with the command after it):", c.listed)
	section("Kept:", c.keeps())
	section("Not looked at:", c.notes)
	fmt.Fprintln(w)
	switch {
	case len(c.actions) == 0:
		fmt.Fprintln(w, "baton: nothing to clean")
	case c.dry:
		fmt.Fprintln(w, "baton: dry run: nothing was changed (baton clean does the above)")
	default:
		fmt.Fprintln(w, "baton: clean")
	}
}
