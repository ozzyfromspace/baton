package cli

import (
	"context"
	"fmt"
	goio "io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The model hands baton what it would otherwise sit and wait on, and works on while baton runs it: verify
// runs a gate on the commit in hand, in a lane of its own (package lane), or, with --here, in the
// session's own working tree (state.HereGate), and await waits on something outside the project. The host
// starts a runner for each (hostRunners), and `baton lane-run` runs it to its end.

func init() {
	register("verify", "run a gate on HEAD in a lane of its own while you work on: verify \"<command>\" [--here] [--qa \"<why a full check now>\"] [--replace] [--why \"<what this run can show that the last could not>\"] --notes \"<where the phase stands>\", or the command's words after --. "+
		"One gate at a time, beside one await: the same command again replaces the gate that runs, and another command only with --replace. "+
		"baton questions a gate out of proportion once; --why answers it, and baton tells the human your reason. "+
		"A red gate stands until the same command passes, or --replace makes another command the phase's gate (the human is told). "+
		"verify --failed [--stages <name>,<name>] --notes \"…\" reruns what failed in a red gate built from stages: the stages that passed are carried, and a pass clears it; "+
		"of a red gate built from none, verify --failed --notes \"…\" -- <a command that names each test that failed> reruns only them, and a pass clears it", cmdVerify)
	register("await", "wait on something outside the project (a CI run, a deploy) while you work on: await \"<command>\" [--qa \"<why a full check now>\"] [--replace] [--why \"<what this run can show that the last could not>\"] --notes \"<where the phase stands>\", or the command's words after --. "+
		"One await at a time, beside one gate: the same command again replaces the await that runs, and another command only with --replace", cmdAwait)
}

func cmdVerify(args []string, io IO) int { return queueJob(args, io, state.JobVerify) }
func cmdAwait(args []string, io IO) int  { return queueJob(args, io, state.JobAwait) }

// queueJob queues a job of kind for the phase in hand. A phase runs one job of each kind at a time, side by
// side (state.QueueJob): the same check queued again replaces the one of its kind that runs, and another
// does only with --replace (state.Clash).
func queueJob(args []string, io IO, kind string) int {
	bools := []string{"replace"}
	usage := " [--qa \"<why a full check now>\"] [--replace] [--why \"<what this run can show that the last could not>\"]"
	vals := []string{"notes", "qa", "why"}
	if kind == state.JobVerify {
		bools, vals, usage = append(bools, "here", "failed"), append(vals, "stages"), " [--here]"+usage
	}
	p, err := parseArgs(args, vals, bools)
	command := strings.TrimSpace(strings.Join(p.pos, " "))
	if p.dash >= 0 {
		// The command as words, as a phase agent gives it: Claude Code refuses an isolated agent's
		// command that hands a program text starting with a shell or git (`baton verify "sh check.sh"`).
		command = strings.TrimSpace(strings.Join(p.pos[:p.dash], " ") + " " + lane.Join(p.pos[p.dash:]))
	}
	failed, bring := p.bools["failed"], stageList(p.vals["stages"])
	if err != nil || command == "" && !failed || len(bring) > 0 && !failed {
		rerun := ""
		if kind == state.JobVerify {
			rerun = ", or baton verify --failed [--stages <name>,<name>] --notes \"<where the phase stands>\" [-- <a command that names each test that failed>] to rerun what failed in the red gate"
		}
		return fail(io, "usage: baton %s \"<command>\"%s --notes \"<where the phase stands>\", or baton %s%s --notes \"<where the phase stands>\" -- <the command's words>%s%s", kind, usage, kind, usage, rerun, errSuffix(err))
	}
	here, replace := p.bools["here"], p.bools["replace"]
	isHosted := hosted(io) // read before the run's lock: it reads processes
	if !isHosted {
		return fail(io, "not queued — the baton host runs jobs, and this session is not hosted by baton. Run the command yourself.")
	}
	s, pl, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	notes, qa, why := strings.TrimSpace(p.vals["notes"]), decide.Sanitize(p.vals["qa"]), decide.Sanitize(p.vals["why"])
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	if err := jobRefusal(isHosted, &st, notes); err != nil {
		return refused(io, s, kind, err)
	}
	phase := st.Current
	var re *rerunAsk
	if failed {
		a, err := askRerun(s, st, phase, command, bring)
		if err != nil {
			return refused(io, s, kind, err)
		}
		command, here, re = a.command(), here || a.r.Of.Here, &a
	}
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
		if worker := s.Worker() != ""; (worker || !here) && namesCheckout(command, s.Checkout, io.Env("HOME")) {
			worktree := ""
			if worker {
				worktree = s.Root
			}
			return refused(io, s, kind, refusal{"names the checkout", decide.CheckoutNamed(command, s.Checkout, worktree, lane.HasScript(s.Root))})
		}
		if work = checkWork(s, phase); len(work.fresh) > 0 {
			s.Event("refused", map[string]any{"command": kind, "phase": phase, "why": "uncommitted work", "files": len(work.fresh)})
			return fail(io, "not queued — verify checks HEAD (%s), and %d %s changed in %s %s not committed, so the gate would not see %s:\n%s\n"+
				"Commit them (by path), then verify again. To check them uncommitted, run the gate in place instead.",
				shortCommit(commit), len(work.fresh), plural(len(work.fresh), "file", "files"), phase, plural(len(work.fresh), "is", "are"),
				plural(len(work.fresh), "it", "them"), fileList(work.fresh))
		}
	}
	qaAsked := false // the QA question went with baton's questions about this gate, so it is not put again
	var asked gateAsk
	var said gateSaid // what the human is told of a gate queued over baton's questions
	if kind == state.JobVerify && re == nil {
		asked = questionGate(io, s, st, pl, phase, command, commit, qa)
		if len(asked.questions) > 0 && why == "" {
			return asked.refuse(io, s, phase, command, commit)
		}
		qaAsked, said = asked.qaAsked, asked.said(phase, command, why)
	}
	most := config.EscalationFromEnv(io.Env).MaxAuto
	var queued state.Job
	var replaced, clash, beside, red *state.Job
	var gateNote *state.Decision
	var count int
	st, err = s.Update(func(st *state.State) error {
		if err := jobRefusal(isHosted, st, notes); err != nil {
			return err
		}
		if st.Current != phase {
			return refusal{"phase changed", fmt.Sprintf("not queued — the phase in hand is %s now, not %s. Run it again if it is still wanted.", st.Current, phase)}
		}
		j := state.Job{Phase: phase, Kind: kind, Command: command, Commit: commit, Here: here, Notes: notes, QA: qa, Why: why}
		if said.event == "gate_unexplained" {
			j.Unexplained = said.line
		}
		if re != nil {
			if err := re.still(*st, phase); err != nil {
				return err
			}
			j.Reruns, j.Narrows = re.r.Of.ID, re.names()
		}
		if c := st.Clash(j); c != nil && !replace && !re.standsIn(*c) {
			x := *c
			clash = &x
			return refusal{"another check runs", decide.Clash(kind, command, x)}
		}
		// A red gate stands until the same command passes: a gate of another command clears it only when the
		// model makes it the phase's gate in its place (--replace), a decision without the human.
		if g := st.RedGate(phase); kind == state.JobVerify && g != nil && g.Command != command && !re.standsIn(*g) {
			x := *g
			red = &x
			if replace {
				if st.ReviewDue != "" && st.Mode == state.ModeRunning {
					return reviewRefusal(st)
				}
				j.Replaces = g.ID
			}
		}
		queued, replaced, _ = state.QueueJob(st, j, io.Now())
		if red != nil && replace && st.Mode == state.ModeRunning {
			what, undo := decide.GateReplaced(*red, queued)
			d, err := state.NoteOn(st, phase, what, undo, io.Now())
			if err != nil {
				return err
			}
			gateNote = &d
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "note", Text: d.What, At: io.Now()})
			if count = st.Unattended(phase); decide.CapReached(count, most) {
				state.DueReview(st, phase, io.Now())
			}
		}
		if b := st.Beside(queued); b != nil {
			x := *b
			beside = &x
		}
		line := fmt.Sprintf("baton: job %s queued for %s — `%s`, from the working tree", queued.ID, phase, command)
		switch {
		case here:
			line = fmt.Sprintf("baton: job %s queued for %s — `%s` on %s, in the working tree (no lane): edits there wait until it ends", queued.ID, phase, command, shortCommit(commit))
		case kind == state.JobVerify:
			line = fmt.Sprintf("baton: job %s queued for %s — `%s` on %s, in a lane", queued.ID, phase, command, shortCommit(commit))
		}
		if replaced != nil {
			line += fmt.Sprintf("; it replaces %s, which baton stops", replaced.ID)
		}
		if re != nil {
			line += fmt.Sprintf("; it reruns what failed in %s", re.r.Of.ID)
		}
		if red != nil && replace {
			line += fmt.Sprintf("; it is %s's gate now, in place of %s (`%s`), which is red", phase, red.ID, red.Command)
		}
		if beside != nil {
			line += "; " + decide.BesideShort(*beside)
		}
		st.Run.Announce = append(st.Run.Announce, line)
		if said.line != "" {
			st.Run.Announce = append(st.Run.Announce, "baton: "+said.line)
		}
		for _, k := range asked.keys { // answered by --why: asked, as a refusal would have recorded them
			if !slices.Contains(st.Run.GatesQuestioned, k) {
				st.Run.GatesQuestioned = append(st.Run.GatesQuestioned, k)
			}
		}
		if qa != "" {
			st.Run.Announce = append(st.Run.Announce, decide.QAEarly(phase, queued.ID, qa))
		}
		return nil
	})
	if clash != nil {
		s.Event("refused", map[string]any{"command": kind, "why": "another check runs", "phase": phase, "running": clash.ID, "asked": command})
		return fail(io, "%v", err)
	}
	if err != nil {
		return refused(io, s, kind, err)
	}
	if re != nil {
		re.save(s, queued)
	}
	pruneLogs(s, st)
	queuedEvent := map[string]any{"job": queued.ID, "phase": phase, "type": kind, "command": command, "commit": commit}
	if here {
		queuedEvent["here"] = true
	}
	if re != nil {
		queuedEvent["reruns"] = re.r.Of.ID
		if ids := re.names(); len(ids) > 0 {
			queuedEvent["narrows"] = ids
		}
		if len(re.bring) > 0 {
			queuedEvent["stages"] = re.bring
		}
	}
	if replaced != nil {
		queuedEvent["replaced"] = replaced.ID
		if !replaced.Same(queued) {
			queuedEvent["replace"] = true
		}
	}
	if beside != nil {
		queuedEvent["beside"] = beside.ID
	}
	if red != nil && replace {
		queuedEvent["replaces_gate"] = red.ID
	} else if red != nil {
		queuedEvent["red_gate"] = red.ID // stands, whatever this job's result
	}
	if why != "" {
		queuedEvent["why"] = why
		if len(asked.applies) > 0 {
			queuedEvent["answered"] = asked.applies // the questions --why answered
		}
	}
	s.Event("job_queued", queuedEvent)
	said.logged(s, asked, queued)
	if said.line != "" {
		tellLead(s, said.line)
	}
	if gateNote != nil {
		s.Event("note", map[string]any{"id": gateNote.ID, "phase": phase, "what": gateNote.What, "undo": gateNote.Undo, "replaces_gate": red.ID})
		if st.ReviewDue != "" {
			s.Event("review_due", map[string]any{"phase": phase, "decisions": count})
		}
		if up, err := s.ParentRun(); err == nil {
			// Nobody watches a worker's session: the human reads its decision in the lead's.
			up.Update(func(st *state.State) error {
				st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: the worker on %s noted — %s (%s)", s.Worker(), gateNote.What, gateNote.ID))
				return nil
			})
		}
	}
	if qa != "" {
		s.Event("qa_early", map[string]any{"job": queued.ID, "phase": phase, "why": qa, "command": command})
	}
	// baton cannot tell a focused check from a full one: it asks, once per job, and the model decides. A wait
	// on CI is often a full check in disguise.
	ask := ""
	if next := qaAhead(io, pl, phase); qa == "" && next != "" && !qaAsked {
		ask = "baton: " + decide.QAAsk(kind, phase, next)
	}

	where := "run from the working tree"
	switch {
	case here:
		where = fmt.Sprintf("on %s, in the working tree, with no lane", shortCommit(commit))
	case kind == state.JobVerify:
		where = fmt.Sprintf("on %s, in a lane of its own", shortCommit(commit))
	}
	fmt.Fprintf(io.Out, "baton: queued %s — `%s` (%s), %s. baton starts it within seconds; its output goes to %s.\n", queued.ID, command, phase, where, s.JobLog(queued.ID))
	if re != nil {
		re.say(io, s, queued, qaAhead(io, pl, phase))
	}
	if said.line != "" {
		fmt.Fprintln(io.Out, "baton: "+decide.HumanTold(said.line))
	}
	if replaced != nil {
		fmt.Fprintln(io.Out, "baton: "+decide.Replaced(*replaced, !replaced.Same(queued)))
	}
	if beside != nil {
		fmt.Fprintln(io.Out, "baton: "+decide.Beside(queued, *beside))
	}
	switch {
	case gateNote != nil:
		fmt.Fprintln(io.Out, "baton: "+decide.GateReplacedSay(gateNote.ID, *red, queued))
	case red != nil && replace: // paused: no decision without the human
		fmt.Fprintln(io.Out, "baton: "+decide.GateReplacedSay("", *red, queued))
	case red != nil:
		fmt.Fprintln(io.Out, "baton: "+decide.RedGateStands(*red, queued))
	}
	cfg := config.Load(batonRoot(io), io.Env)
	timeout, _ := cfg.JobTimeout()
	after, _ := cfg.SwitchWait()
	if ask != "" {
		fmt.Fprintln(io.Out, ask)
	}
	if here {
		landing, others := hereLandings(s, st)
		fmt.Fprintln(io.Out, "baton: "+decide.HereQueued(s.Worker() != "", decide.Span(timeout), landing, others))
		if work.warn != "" {
			fmt.Fprintln(io.Out, work.warn)
		}
		if n := len(work.kept); n > 0 {
			fmt.Fprintln(io.Out, "baton: "+decide.HereKept(n, phase))
		}
		return 0
	}
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

// qaAhead names the QA phase ahead of phase in pl ("P5 (QA)") while the plan leaves its heavy checks to
// its QA phases (aggregate_qa, as the host passed it down), "" when it does not, phase is a QA phase, or
// none is ahead.
func qaAhead(io IO, pl plan.Plan, phase string) string {
	if !config.AggregatesFromEnv(io.Env) {
		return ""
	}
	return pl.QAAhead(phase)
}

// jobRefusal turns down a job when there is no phase in hand to run it for, or no notes to resume the
// phase from.
func jobRefusal(isHosted bool, st *state.State, notes string) error {
	if err := guard(isHosted, st, "not queued", true); err != nil {
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

// namesCheckout reports whether command names the directory checkout, or a path in it: by its absolute
// path, as given or with symlinks resolved, or from the home directory, home (~/…, $HOME/…). A gate
// that does runs on the checkout's files, wherever baton runs it.
func namesCheckout(command, checkout, home string) bool {
	if checkout == "" {
		return false
	}
	forms := []string{checkout}
	if real, err := filepath.EvalSymlinks(checkout); err == nil && real != checkout {
		forms = append(forms, real)
	}
	if home != "" {
		if rel, err := filepath.Rel(home, checkout); err == nil && rel != "." && filepath.IsLocal(rel) {
			rel = filepath.ToSlash(rel)
			forms = append(forms, "~/"+rel, "$HOME/"+rel, "${HOME}/"+rel)
		}
	}
	name := func(c byte) bool {
		return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '_' || c == '-'
	}
	for _, f := range forms {
		for i := 0; i < len(command); {
			k := strings.Index(command[i:], f)
			if k < 0 {
				break
			}
			at, end := i+k, i+k+len(f)
			// Only the whole path, or one inside it: /a/foo names neither /a/foobar nor /x/a/foo.
			if (at == 0 || !name(command[at-1]) && command[at-1] != '/') && (end == len(command) || !name(command[end])) {
				return true
			}
			i = at + 1
		}
	}
	return false
}

// pruneLogs removes the logs and stages files of jobs no longer on record: dropped to keep the record
// short, or left by a plan set aside (whose record took the logs of its failed jobs, Store.Record). The log
// of every failed job of the plan st runs stays, dropped from the record or not (KeptLogs).
func pruneLogs(s *state.Store, st state.State) {
	dir := jobsDir(s)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if id := state.JobFileID(e.Name()); st.Job(id) == nil && !slices.Contains(st.KeptLogs, id) {
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

// lanePool is the lanes of s's project: those of its checkout, which a worker in a worktree of its own
// shares with every other session of the project.
func lanePool(io IO, s *state.Store) lane.Pool {
	return lane.Pool{Home: batonRoot(io), Root: s.Checkout}
}

// lanesDown brings down the lanes host brought up that no job holds: its run is over.
func lanesDown(io IO, root, projectDir, host string) {
	if host == "" {
		return
	}
	w := laneLog(projectDir)
	defer w.Close()
	lane.Pool{Home: batonRoot(io), Root: root}.Down(host, w)
}

// hostRunners runs the jobs of a host's sessions (loop.Runners), each in a `baton lane-run` of its own,
// detached, and keeps the lanes they run in (loop.Lanes).
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

func (h hostRunners) Spawn(ref, id string) (int, error) {
	return lane.Spawn([]string{h.exe, "lane-run", ref, id}, h.env, h.proj.Root, filepath.Join(h.proj.Dir, "baton.log"))
}

func (h hostRunners) Alive(pid int) bool { return lane.Alive(pid) }
func (h hostRunners) Kill(pgid int)      { lane.KillGroup(pgid) }

// Down brings down the lanes this host brought up that no job holds: its plan is complete.
func (h hostRunners) Down() []int {
	w := laneLog(h.proj.Dir)
	defer w.Close()
	return lane.Pool{Home: h.home, Root: h.proj.Root}.Down(h.instance, w)
}

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
		if j.Here {
			what += " in the working tree"
		}
		if j.QA != "" {
			what += " (a full check ahead of QA: " + j.QA + ")"
		}
		log := j.Log
		if log == "" {
			log = s.JobLog(j.ID)
		}
		fmt.Fprintf(io.Out, "  %-4s %-8s %-6s %s  %s\n       log: %s\n", j.ID, j.Phase, j.Kind, jobProgress(j, io.Now()), what, log)
	}
	pool := lanePool(io, s)
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
