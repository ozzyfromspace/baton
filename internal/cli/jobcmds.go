package cli

import (
	"context"
	"fmt"
	goio "io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The model hands baton what it would otherwise sit and wait on, and works on while baton runs it: verify
// runs a gate on the commit in hand, in a lane of its own (package lane), and await waits on something
// outside the project. The host starts a runner for each (hostRunners), and `baton lane-run` runs it to
// its end.

func init() {
	register("verify", "run a gate on HEAD in a lane of its own while you work on: verify \"<command>\" --notes \"<where the phase stands>\"", cmdVerify)
	register("await", "wait on something outside the project (a CI run, a deploy) while you work on: await \"<command>\" --notes \"<where the phase stands>\"", cmdAwait)
}

func cmdVerify(args []string, io IO) int { return queueJob(args, io, state.JobVerify) }
func cmdAwait(args []string, io IO) int  { return queueJob(args, io, state.JobAwait) }

// queueJob queues a job of kind for the phase in hand.
func queueJob(args []string, io IO, kind string) int {
	p, err := parseArgs(args, []string{"notes"}, nil)
	command := strings.TrimSpace(strings.Join(p.pos, " "))
	if err != nil || command == "" {
		return fail(io, "usage: baton %s \"<command>\" --notes \"<where the phase stands>\"%s", kind, errSuffix(err))
	}
	if !hosted(io) {
		return fail(io, "not queued — the baton host runs jobs, and this session is not hosted by baton. Run the command yourself.")
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	notes := strings.TrimSpace(p.vals["notes"])
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	if err := jobRefusal(io, &st, notes); err != nil {
		return refused(io, s, kind, err)
	}
	phase := st.Current
	var commit string
	var work workLeft
	if kind == state.JobVerify {
		if !gitx.Usable(s.Root) {
			return refused(io, s, kind, refusal{"no git", "not queued — verify runs a gate on a commit, in a lane of its own, and this project does not use git. " +
				"Run the gate in place instead. If it takes long, `baton await \"<command>\" --notes \"…\"` runs it from the working tree while you work on, " +
				"but what you change meanwhile can change what it checks."})
		}
		if commit = gitx.Head(s.Root); commit == "" {
			return refused(io, s, kind, refusal{"no commit", fmt.Sprintf("not queued — the repository has no commit yet, so there is nothing to check. Commit %s's work, then verify again.", phase)})
		}
		if work = checkWork(s, phase); len(work.fresh) > 0 {
			s.Event("refused", map[string]any{"command": kind, "phase": phase, "why": "uncommitted work", "files": len(work.fresh)})
			return fail(io, "not queued — verify checks HEAD (%s), and %d %s changed in %s %s not committed, so the gate would not see %s:\n%s\n"+
				"Commit them (by path), then verify again. To check them uncommitted, run the gate in place instead.",
				shortCommit(commit), len(work.fresh), plural(len(work.fresh), "file", "files"), phase, plural(len(work.fresh), "is", "are"),
				plural(len(work.fresh), "it", "them"), fileList(work.fresh))
		}
	}
	var queued state.Job
	var replaced *state.Job
	st, err = s.Update(func(st *state.State) error {
		if err := jobRefusal(io, st, notes); err != nil {
			return err
		}
		if st.Current != phase {
			return refusal{"phase changed", fmt.Sprintf("not queued — the phase in hand is %s now, not %s. Run it again if it is still wanted.", st.Current, phase)}
		}
		queued, replaced, _ = state.QueueJob(st, state.Job{Phase: phase, Kind: kind, Command: command, Commit: commit, Notes: notes}, io.Now())
		line := fmt.Sprintf("baton: job %s queued for %s — `%s`, from the working tree", queued.ID, phase, command)
		if kind == state.JobVerify {
			line = fmt.Sprintf("baton: job %s queued for %s — `%s` on %s, in a lane", queued.ID, phase, command, shortCommit(commit))
		}
		if replaced != nil {
			line += fmt.Sprintf("; it replaces %s, which baton stops", replaced.ID)
		}
		st.Run.Announce = append(st.Run.Announce, line)
		return nil
	})
	if err != nil {
		return refused(io, s, kind, err)
	}
	pruneLogs(s, st)
	queuedEvent := map[string]any{"job": queued.ID, "phase": phase, "type": kind, "command": command, "commit": commit}
	if replaced != nil {
		queuedEvent["replaced"] = replaced.ID
	}
	s.Event("job_queued", queuedEvent)

	where := "run from the working tree"
	if kind == state.JobVerify {
		where = fmt.Sprintf("on %s, in a lane of its own", shortCommit(commit))
	}
	fmt.Fprintf(io.Out, "baton: queued %s — `%s` (%s), %s. baton starts it within seconds; its output goes to %s.\n", queued.ID, command, phase, where, s.JobLog(queued.ID))
	if replaced != nil {
		fmt.Fprintf(io.Out, "baton: it replaces %s (`%s`), which baton stops: a phase has one job at a time.\n", replaced.ID, replaced.Command)
	}
	cfg := config.Load(batonRoot(io), io.Env)
	timeout, _ := cfg.JobTimeout()
	after, _ := cfg.SwitchWait()
	next := fmt.Sprintf("Keep working on what does not depend on its result; baton tells you when it ends, and baton status shows it meanwhile. "+
		"With nothing left to do until then, commit your work and end your turn: if the job runs past %s (or is known to take that long) and another phase can go on, "+
		"baton sets %s aside for it, and %s resumes from your notes once the job ends. If the job runs past %s, baton stops it and counts it as failed.",
		decide.Span(after), phase, phase, decide.Span(timeout))
	if kind == state.JobVerify {
		next += " What you commit from now on is not in this check."
	}
	fmt.Fprintln(io.Out, "baton: "+next)
	if work.warn != "" {
		fmt.Fprintln(io.Out, work.warn)
	}
	if n := len(work.kept); n > 0 {
		fmt.Fprintf(io.Out, "baton: note — %d uncommitted %s already there when %s started; the gate checks HEAD, without %s.\n",
			n, plural(n, "file was", "files were"), phase, plural(n, "it", "them"))
	}
	return 0
}

// jobRefusal turns down a job when there is no phase in hand to run it for, or no notes to resume the
// phase from.
func jobRefusal(io IO, st *state.State, notes string) error {
	if err := guard(io, st, "not queued", true); err != nil {
		return err
	}
	switch {
	case st.Mode != state.ModeRunning && st.Mode != state.ModePaused:
		return refusal{"no run", fmt.Sprintf("not queued — no plan is running here (mode: %s).", st.Mode)}
	case st.Current == "":
		return refusal{"no phase in hand", "not queued — no phase is in hand: every phase left waits for a job to finish. End your turn; baton wakes you when there is something to do."}
	case st.BoundaryOwed:
		return refusal{"phase not started", fmt.Sprintf("not queued — %s has not started yet: end your turn now, so baton can compact the context before %s begins.", st.Current, st.Current)}
	case notes == "":
		return refusal{"no notes", fmt.Sprintf("not queued — say where %s stands with --notes \"<what is done, what is left, what this job checks>\": "+
			"if baton sets %s aside while the job runs, your notes are what it resumes from.", st.Current, st.Current)}
	}
	return nil
}

// pruneLogs removes the logs of jobs no longer on record: dropped to keep the record short, or left by a
// plan set aside.
func pruneLogs(s *state.Store, st state.State) {
	dir := jobsDir(s)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if st.Job(strings.TrimSuffix(e.Name(), ".log")) == nil {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func jobsDir(s *state.Store) string { return filepath.Dir(s.JobLog("j")) }

// runnerFresh is how recently a runner must have been heard from to be signalled. One silent for longer
// may be gone, and its pid another process's by now.
const runnerFresh = 2 * time.Minute

// stopRunners stops the runners of jobs taken off the run, so that their commands stop and their lanes
// come down now, rather than at their next beat. Of a runner not heard from lately, what its command left
// running is killed instead.
func stopRunners(jobs []state.Job, now time.Time) {
	for _, j := range jobs {
		heard := j.Heartbeat
		if j.Spawned.After(heard) {
			heard = j.Spawned
		}
		switch {
		case j.PID == 0:
		case now.Sub(heard) < runnerFresh:
			lane.Terminate(j.PID)
		case j.PGID != 0:
			lane.KillGroup(j.PGID)
		}
	}
}

// endJobs stops the jobs of the run this host drives, as the session ends: nothing would see their
// results. It says so on the terminal.
func endJobs(io IO, s *state.Store, instance string) {
	var jobs []state.Job
	s.Update(func(st *state.State) error {
		if st.IsOwner(instance, io.Now()) {
			jobs = state.CancelJobs(st, func(state.Job) bool { return true }, "the baton session ended", io.Now())
		}
		return nil
	})
	if len(jobs) == 0 {
		return
	}
	stopRunners(jobs, io.Now())
	s.Event("jobs_stopped", map[string]any{"jobs": jobIDs(jobs), "why": "the session ended"})
	fmt.Fprintf(io.Out, "baton: stopped %s, as the session ended.\n", jobLabels(jobs))
}

func jobIDs(jobs []state.Job) []string {
	var ids []string
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	return ids
}

// jobLabels names jobs for the human: "job j3 (`make test`, P2)".
func jobLabels(jobs []state.Job) string {
	var names []string
	for _, j := range jobs {
		names = append(names, fmt.Sprintf("%s (`%s`, %s)", j.ID, j.Command, j.Phase))
	}
	return plural(len(jobs), "job ", "jobs ") + strings.Join(names, ", ")
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// laneLog is where lanes' own output goes when no job's log is theirs: the project's baton.log.
func laneLog(projectDir string) goio.WriteCloser {
	f, err := os.OpenFile(filepath.Join(projectDir, "baton.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nopCloser{goio.Discard}
	}
	return f
}

type nopCloser struct{ goio.Writer }

func (nopCloser) Close() error { return nil }

// lanesDown brings down the lanes host brought up that no job holds: its run is over.
func lanesDown(io IO, root, projectDir, host string) {
	if host == "" {
		return
	}
	w := laneLog(projectDir)
	defer w.Close()
	lane.Pool{Home: batonRoot(io), Root: root}.Down(host, w)
}

// hostRunners runs a hosted session's jobs (loop.Runners): each in a `baton lane-run` of its own, detached.
type hostRunners struct {
	exe, home, instance string
	proj                *state.Project
	env                 []string // the runners' environment
	now                 func() time.Time
	logf                func(string, ...any)
}

func newHostRunners(io IO, exe string, proj *state.Project, instance string) hostRunners {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BATON_") {
			env = append(env, kv)
		}
	}
	env = append(env, "BATON_DIR="+proj.Dir, "BATON_HOME="+batonRoot(io), "BATON_INSTANCE="+instance)
	if v := io.Env("BATON_VERIFY_TIMEOUT"); v != "" {
		env = append(env, "BATON_VERIFY_TIMEOUT="+v)
	}
	return hostRunners{exe: exe, home: batonRoot(io), instance: instance, proj: proj, env: env, now: io.Now,
		logf: fileLogger(filepath.Join(proj.Dir, "baton.log"), instance, io.Now)}
}

func (h hostRunners) Spawn(run, id string) (int, error) {
	return lane.Spawn([]string{h.exe, "lane-run", run, id}, h.env, h.proj.Root, filepath.Join(h.proj.Dir, "baton.log"))
}

func (h hostRunners) Alive(pid int) bool { return lane.Alive(pid) }
func (h hostRunners) Kill(pgid int)      { lane.KillGroup(pgid) }

// Sweep keeps the lanes of every host still running, this one included.
func (h hostRunners) Sweep() {
	live := map[string]bool{h.instance: true}
	for _, r := range h.proj.Runs() {
		if r.Live(h.now()) {
			live[r.State.Owner.Instance] = true
		}
	}
	w := laneLog(h.proj.Dir)
	defer w.Close()
	if swept := (lane.Pool{Home: h.home, Root: h.proj.Root}).Sweep(context.Background(), func(host string) bool { return live[host] }, w); len(swept) > 0 {
		h.logf("host: removed lanes %v, which hosts that are gone left behind", swept)
	}
}

// printJobs lists the run's jobs, newest first, with their logs, and the checkout's lanes.
func printJobs(io IO, s *state.Store, st state.State) {
	if len(st.Jobs) == 0 {
		return
	}
	const most = 10
	fmt.Fprintln(io.Out, "jobs (newest first):")
	verify := false
	for i, n := len(st.Jobs)-1, 0; i >= 0; i, n = i-1, n+1 {
		j := st.Jobs[i]
		verify = verify || j.Kind == state.JobVerify
		if n == most {
			fmt.Fprintf(io.Out, "  … and %d older (baton status --json lists them all)\n", i+1)
			break
		}
		what := "`" + j.Command + "`"
		if j.Commit != "" {
			what += " on " + shortCommit(j.Commit)
		}
		log := j.Log
		if log == "" {
			log = s.JobLog(j.ID)
		}
		fmt.Fprintf(io.Out, "  %-4s %-8s %-6s %s  %s\n       log: %s\n", j.ID, j.Phase, j.Kind, jobProgress(j, io.Now()), what, log)
	}
	pool := lane.Pool{Home: batonRoot(io), Root: s.Root}
	if lanes := pool.Lanes(); len(lanes) > 0 {
		fmt.Fprintf(io.Out, "lanes (%s):\n", pool.Dir())
		for _, l := range lanes {
			how := "idle"
			if l.Busy {
				how = "busy"
				for _, j := range st.Jobs {
					if j.Status == state.JobRunning && j.Lane == l.N {
						how += " (" + j.ID + ")"
					}
				}
			}
			if l.UpBy != "" {
				how += ", up"
			}
			fmt.Fprintf(io.Out, "  lane %d  %s\n", l.N, how)
		}
	}
	if verify && !lane.HasScript(s.Root) {
		fmt.Fprintf(io.Out, "lanes: this project has no %s, so a gate runs with sh -c in a bare checkout of its commit, one at a time; "+
			"commit an executable %s to set lanes up (dependencies, services) and run more at once.\n", lane.Script, lane.Script)
	}
}

// jobProgress is where a job stands: "running 4m, lane 1", "failed (exit 1) after 12m, lane 1".
func jobProgress(j state.Job, now time.Time) string {
	in := ""
	if j.Lane > 0 {
		in = fmt.Sprintf(", lane %d", j.Lane)
	}
	switch j.Status {
	case state.JobQueued:
		return "queued"
	case state.JobRunning:
		return "running " + decide.Span(j.Took(now)) + in
	}
	return decide.Outcome(j) + in
}
