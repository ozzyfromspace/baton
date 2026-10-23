package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	goio "io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/hostcheck"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/replan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
)

// These commands are run by the model (through the Bash tool) and by the /baton skill. Their output is
// read by the model, so it is short, literal, and says what to do next.

func init() {
	register("attach", "attach a plan document: --suggest shows the phases, --suggested or --spec '<json>' attaches them", leadOnly("attach", cmdAttach))
	register("status", "show the attached plan and where the run is (--json for machines)", cmdStatus)
	register("done", "mark the current phase finished (verifying while its job runs), or a worker's phase whose work is on the run's branch already: done <phase> [--notes TEXT] [--deviation \"<how it departed from the plan, or none>\"] [--keep-dirty WHY] [--despite-red WHY] [--land-check COMMAND]", cmdDone)
	register("blocked", "stop until the human answers, or for 30 minutes before looking again, when every way forward is irreversible: blocked <reason> --tried TEXT [--keep-dirty WHY]", cmdBlocked)
	register("checkpoint", "ask for a mid-phase compaction at this safe point [--notes TEXT]", cmdCheckpoint)
	register("run", "run this session's plan: resumes it if paused or blocked; says when a plan file needs attaching: run [plan file]", leadOnly("run", cmdRun))
	register("pause", "stop baton from acting until /baton run (the human takes the wheel)", leadOnly("pause", cmdPause))
	register("drop", "drop the run: its plan is set aside, and baton does nothing until the next plan", leadOnly("drop", cmdDrop))
}

// project opens the project a command runs in: the one the host named (BATON_DIR) inside a hosted
// session, or else the one the current directory is in.
func project(io IO) (*state.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return state.OpenProject(state.Locate(cwd, io.Env), io.Now)
}

// instance is the host instance a command runs under, or "" outside a hosted session.
func instance(io IO) string {
	if hosted(io) {
		return io.Env("BATON_INSTANCE")
	}
	return ""
}

// store returns the run a command is about. Inside a Claude Code session (the Bash tool has the session
// id) that is the session's own run; a plain session with none has no plan. From a shell, it is the run
// the command can only mean (state.Project.Pick).
func store(io IO) (*state.Store, error) {
	p, err := project(io)
	if err != nil {
		return nil, err
	}
	if s, ok := inPhase(io, p); ok {
		return s, nil
	}
	sid, inst := io.Env("CLAUDE_CODE_SESSION_ID"), instance(io)
	if s, ok := p.Lookup(sid, inst); ok {
		return s, nil
	}
	switch {
	case inst != "" && sid != "":
		return p.Bind(state.Binding{Session: sid, Instance: inst}) // hosted, before any hook bound it
	case sid != "" || inst != "":
		return nil, state.ErrNoPlan
	}
	return p.Pick(inst)
}

// attachTo returns the run a plan is attached to: the session's own, created if need be, or from a
// shell the run the next baton session started here takes over.
func attachTo(io IO) (*state.Store, error) {
	p, err := project(io)
	if err != nil {
		return nil, err
	}
	if s, ok := inPhase(io, p); ok {
		return s, nil
	}
	sid, inst := io.Env("CLAUDE_CODE_SESSION_ID"), instance(io)
	if sid != "" || inst != "" {
		return p.Bind(state.Binding{Session: sid, Instance: inst})
	}
	return p.Pending("")
}

// inPhase is the sub-run of the phase agent a command runs in, if it runs in one: hosted, in the lead's
// session, inside one of the worktrees of its run's phases (state.Project.PhaseAt). A phase agent shares
// the lead's session and environment; only where it works says that a command is the agent's.
func inPhase(io IO, p *state.Project) (*state.Store, bool) {
	if !hosted(io) {
		return nil, false
	}
	cwd, err := io.wd()
	if err != nil {
		return nil, false
	}
	return p.PhaseAt(io.Env("CLAUDE_CODE_SESSION_ID"), io.Env("BATON_INSTANCE"), cwd)
}

func fail(io IO, format string, a ...any) int {
	fmt.Fprintf(io.Err, "baton: "+format+"\n", a...)
	return 1
}

// hosted reports whether a live baton host is above the session the command runs in (hostcheck.Hosted).
func hosted(io IO) bool { return io.host() == hostcheck.Hosted }

// inherited reports a session that only inherited baton's environment, with no baton host above it
// (hostcheck.Inherited): baton leaves it alone, and says how to give it back (decide.Inherited).
func inherited(io IO) bool { return io.host() == hostcheck.Inherited }

// hostLabel is how `baton status` names the session's standing with baton.
func hostLabel(io IO) string {
	switch io.host() {
	case hostcheck.Hosted:
		return "hosted"
	case hostcheck.Inherited:
		return decide.InheritedStatus
	}
	return "not hosted"
}

// wayBack tells the human, in a session that only inherited baton's environment, how to give it back to
// baton.
func wayBack(io IO) {
	if inherited(io) {
		fmt.Fprintln(io.Out, decide.Inherited(io.Env("CLAUDE_CODE_SESSION_ID")))
	}
}

// notHostedNote is the note for a command that did its part in a session no baton host is above, where
// nothing compacts the context: how to give the session to baton.
func notHostedNote(io IO, what string) string {
	if inherited(io) {
		return "baton: note — " + what + ". " + strings.TrimPrefix(decide.Inherited(io.Env("CLAUDE_CODE_SESSION_ID")), "baton: ")
	}
	return "baton: note — " + what + ". Run /baton start (or start claude with `baton`)."
}

// worker reports whether a command runs for a worker, which works on one phase of its run: in a phase
// agent (inPhase).
func worker(io IO) bool {
	if !hosted(io) {
		return false
	}
	p, err := project(io)
	if err != nil {
		return false
	}
	_, ok := inPhase(io, p)
	return ok
}

func cmdAttach(args []string, io IO) int {
	p, err := parseArgs(args, []string{"spec"}, []string{"suggest", "suggested", "replace"})
	if err != nil || len(p.pos) != 1 {
		return fail(io, "usage: baton attach <plan.md> (--suggest | --suggested | --spec JSON|FILE|-) [--replace]%s", errSuffix(err))
	}
	file, err := filepath.Abs(p.pos[0])
	if err != nil {
		return fail(io, "%v", err)
	}
	doc, err := os.ReadFile(file)
	if err != nil {
		return fail(io, "cannot read the plan document: %v", err)
	}
	if p.bools["suggest"] {
		spec := plan.Suggest(doc)
		out, _ := json.MarshalIndent(spec, "", "  ")
		fmt.Fprintln(io.Out, string(out))
		fmt.Fprintln(io.Err, "baton: this is a suggestion. Check every phase is present and in order, ids and titles are right, and each "+
			"anchor is a verbatim snippet that starts its phase. Each phase's \"after\" lists the phases it waits for, read from its After: line in the plan "+
			"(a phase without one follows the phase before it): to change them, edit the plan, and leave \"after\" out of a corrected spec. "+
			"If it is right as printed: baton attach "+p.pos[0]+" --suggested. Otherwise pass the corrected spec inline: baton attach "+p.pos[0]+" --spec '<json>'")
		if pl, err := plan.Build(file, doc, spec); err == nil {
			fmt.Fprintln(io.Err, "baton: as suggested, "+pl.Dependencies()+".")
		}
		return 0
	}
	src, ok := p.vals["spec"]
	var raw []byte
	switch {
	case p.bools["suggested"]:
		raw, err = json.Marshal(plan.Suggest(doc))
	case !ok:
		return fail(io, "pass --suggest to see the phases baton found, then --suggested to attach them as they are, or --spec '<json>' to attach a corrected spec")
	case src == "-":
		raw, err = goio.ReadAll(io.In)
	case strings.HasPrefix(strings.TrimSpace(src), "{"):
		raw = []byte(src) // inline JSON: a single plain command, which an allow rule for baton covers
	default:
		raw, err = os.ReadFile(src)
	}
	if err != nil {
		return fail(io, "cannot read the spec: %v", err)
	}
	var spec plan.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fail(io, "the spec is not valid JSON: %v", err)
	}
	if proj, err := project(io); err == nil {
		spec.Root = proj.Root // where its record file, if it names one, is checked
	}
	pl, err := plan.Build(file, doc, spec)
	if err != nil {
		return fail(io, "the spec does not match the plan document:\n%v", err)
	}
	if !p.bools["replace"] {
		if c, ok := carry(io, file); ok { // a run of this plan another session left: go on where it stood
			return runCarried(io, c)
		}
	}
	s, err := attachTo(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	cur, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	if (cur.Mode == state.ModeRunning || cur.Mode == state.ModePaused) && !p.bools["replace"] {
		return fail(io, "a plan is already %s here; pass --replace to discard its progress", cur.Mode)
	}
	replaced, _ := s.LoadPlan()
	root := s.Root
	if err := state.ExcludeFromGit(root); err != nil {
		fmt.Fprintf(io.Err, "baton: warning: could not add .baton/ to .git/info/exclude: %v\n", err)
	}
	origin, branch := state.OriginOf(root), gitx.Branch(root) // read before the lock, as is whether it is hosted
	tip := ""
	if branch != "" {
		tip = gitx.RevParse(root, "refs/heads/"+branch)
	}
	inSession, isHosted := io.Env("CLAUDE_CODE_SESSION_ID") != "", hosted(io)
	aside := lookAside(io, s, cur, file)
	var boundary, moved bool
	var unmoved error
	var rec *state.Record
	st, err := s.Update(func(st *state.State) error {
		// The plan this replaces keeps its record (its state, plan and notes) as it was before anything
		// overwrites it, written once the lock is let go.
		var err error
		if rec, err = s.Record(*st); err != nil {
			return fmt.Errorf("could not keep the record of the plan this replaces: %w", err)
		}
		// The session's plan-mode file moves out of Claude Code's way first, and the plan follows it.
		moved, unmoved = aside.Move(&pl)
		if err := s.WritePlan(pl); err != nil {
			if moved {
				state.Unmove(&pl)
				moved = false
			}
			return err
		}
		// The first phase starts from a brief, after a compaction, unless there is nothing before it to
		// compact: a hosted conversation whose first turn this is, or a shell (the next session starts
		// fresh). A plain session is resumed under baton to run the plan, and compacts then.
		boundary = inSession && (!isHosted || st.Run.History)
		if boundary {
			*st = state.AttachAtBoundary(*st, pl, io.Now())
		} else {
			*st = state.Reattach(*st, pl, io.Now(), origin)
		}
		st.RunBranch, st.BranchFrom = branch, tip // workers branch off it, and their work lands on it
		st.PlanLooked = true
		return nil
	})
	if err != nil {
		return fail(io, "%v", err)
	}
	kept := keepRecord(io, s, rec)
	replan.KeepApproved(s, pl, kept) // what every replan of it is compared with
	paused := st.Mode == state.ModePaused
	chain, _ := pl.LongestChain()
	attached := map[string]any{"plan": pl.File, "phases": len(pl.Phases), "longest_chain": chain, "git": gitx.Usable(root), "compact_first": boundary}
	if branch != "" {
		attached["branch"] = branch
	}
	if paused {
		attached["paused"] = true
	}
	s.Event("attached", attached)
	movedAside(io, s, pl, aside, file, moved, unmoved)
	first := pl.Phases[0]
	fmt.Fprintf(io.Out, "baton: attached %q — %s (%s). ", pl.Title, decide.PhaseCount(len(pl.Phases)), pl.Dependencies())
	switch {
	case paused:
		fmt.Fprintln(io.Out, decide.AttachedPaused(first.ID+" ("+first.Title+")"))
	case boundary && isHosted:
		fmt.Fprintf(io.Out, "End your turn now: baton compacts this conversation, then starts %s (%s) with a fresh brief.\n", first.ID, first.Title)
	case boundary:
		fmt.Fprintf(io.Out, "%s (%s) starts once baton has compacted this conversation.\n", first.ID, first.Title)
	default:
		fmt.Fprintf(io.Out, "Current phase: %s (%s).\n", first.ID, first.Title)
	}
	if kept != "" {
		fmt.Fprintf(io.Out, "baton: the record of %q (its state, plan and notes) is kept in %s.\n", replaced.Title, shown(s, kept))
	}
	if !isHosted {
		fmt.Fprintln(io.Out, notHostedNote(io, "this session is not hosted by baton, so nothing will compact automatically"))
	} else if line := workersAttached(io, s.Checkout, pl); line != "" {
		fmt.Fprintln(io.Out, "baton: tell the human: "+line) // it is theirs to act on, not the model's
	}
	return 0
}

// shown is a path in the run's directory as the human finds it from the project's checkout.
func shown(s *state.Store, path string) string {
	if rel, err := filepath.Rel(s.Checkout, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

// workersAttached says, as plan pl is attached in checkout, how many workers will work beside the session,
// and warns when they would stop at permission prompts. "" with no workers.
func workersAttached(io IO, checkout string, pl plan.Plan) string {
	if len(pl.Phases) < 2 {
		return "" // nothing could go ahead beside a plan's one phase
	}
	n, auto, err := config.Load(batonRoot(io), io.Env).WorkerCap(io.reading)
	if err != nil {
		return ""
	}
	mode := config.WorkerMode(io.Env, checkout)
	return decide.WorkersAttached(n, auto, pl.Width(), mode)
}

func errSuffix(err error) string {
	if err != nil {
		return " (" + err.Error() + ")"
	}
	return ""
}

func cmdStatus(args []string, io IO) int {
	p, err := parseArgs(args, nil, []string{"json"})
	if err != nil {
		return fail(io, "%v", err)
	}
	s, err := store(io)
	var several state.ErrSeveralRuns
	switch {
	case errors.As(err, &several):
		printRuns(io, several.Runs, p.bools["json"])
		return 0
	case errors.Is(err, state.ErrNoPlan):
		switch {
		case p.bools["json"] && inherited(io):
			fmt.Fprintln(io.Out, `{"state": null, "hosted": false, "inherited": true}`)
		case p.bools["json"]:
			fmt.Fprintln(io.Out, `{"state": null}`)
		default:
			fmt.Fprintln(io.Out, "baton: no plan attached here.")
			wayBack(io)
		}
		return 0
	case err != nil:
		return fail(io, "%v", err)
	}
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	pl, perr := s.LoadPlan()
	snaps := gitx.Snapshots(s.Root)
	if p.bools["json"] {
		out := map[string]any{"state": st, "baton_dir": s.Dir, "run": s.Ref, "hosted": hosted(io)}
		if inherited(io) {
			out["inherited"] = true
		}
		if perr == nil {
			out["plan"] = pl
		}
		if len(snaps) > 0 {
			out["snapshots"] = snaps
		}
		if lanes := lanePool(io, s).Lanes(); len(lanes) > 0 {
			out["lanes"] = lanes
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(io.Out, string(b))
		return 0
	}
	if st.Mode == state.ModeIdle || perr != nil {
		fmt.Fprintln(io.Out, "baton: no plan attached here.")
		wayBack(io)
		return 0
	}
	fmt.Fprintf(io.Out, "baton: %q — %s (%s)\nplan: %s\n", pl.Title, st.Mode, hostLabel(io), pl.File)
	wayBack(io)
	if sha := pl.SHA(); sha != pl.SHA256 && st.Mode != state.ModeComplete {
		fmt.Fprintln(io.Out, "  (the plan file changed since baton last read it; baton checks it again at the next stop)")
	}
	seq := pl.Sequential()
	for _, ph := range pl.Phases {
		mark, deps, extra := "·", "", ""
		if !seq {
			deps = "  — " + pl.DepsLabel(ph.ID)
		}
		ps := st.Phases[ph.ID]
		if ps != nil {
			switch ps.Status {
			case state.PhaseDone:
				mark = "✓"
			case state.PhaseActive:
				mark = "▶"
			case state.PhaseVerifying:
				mark = "⧗"
			case state.PhaseParked:
				mark = "⏸"
			case state.PhaseWorking:
				mark = "⚙"
			}
			if note := phaseNote(st, ph.ID, io.Now()); note != "" {
				extra = "  (" + note + ")"
			}
		}
		if ph.ID == st.Current && st.BoundaryOwed {
			mark, extra = "→", "  (next, after compaction)"
			if ps != nil && ps.Status == state.PhaseParked {
				extra = "  (resumes next, after compaction)"
			}
		}
		fmt.Fprintf(io.Out, "  %s %-8s %s%s%s\n", mark, ph.ID, ph.Title, deps, extra)
		if ps != nil && ps.Status == state.PhaseParked && ps.Park != nil && ps.Park.Notes != "" {
			fmt.Fprintf(io.Out, "%13snotes: %s\n", "", clipLine(ps.Park.Notes, 100))
		}
	}
	switch {
	case len(pl.Phases) == 1: // one phase depends on nothing
	case seq:
		fmt.Fprintln(io.Out, "dependencies: each phase follows the one before it")
	default:
		k, chain := pl.LongestChain()
		fmt.Fprintf(io.Out, "longest chain: %d of %d phases (%s)\n", k, len(pl.Phases), strings.Join(chain, " → "))
	}
	printSaved(io, st)
	printRisk(io, st)
	printReplans(io, s, st)
	if st.Blocked != nil {
		fmt.Fprintf(io.Out, "blocked: %s\n", st.Blocked.Reason)
		if st.Blocked.Tried != "" {
			fmt.Fprintf(io.Out, "  tried: %s\n", st.Blocked.Tried)
		}
	}
	printJobs(io, s, st)
	printMachine(io, s, st)
	printWorkers(io, s, st)
	printMergeQueue(io, st)
	printBackground(io, st)
	if st.CheckpointOwed {
		fmt.Fprintln(io.Out, "checkpoint: a mid-phase compaction is owed at the next stop")
	}
	if st.CheckpointDue {
		fmt.Fprintln(io.Out, "checkpoint: the conversation grew while baton was paused; Claude records where "+st.Current+" stands, then baton compacts")
	}
	if st.Mode == state.ModePaused && !st.Covered.IsZero() {
		fmt.Fprintf(io.Out, "compaction: the context was compacted while baton was paused; /baton run counts that for the compaction owed, unless the conversation grows by more than %s tokens first\n", valve.Tokens(state.ResumeGrowth))
	}
	printWaitingOnYou(io, &st)
	printRelay(io, s, st)
	printUsage(io, st)
	if cu := st.Run.Context; cu != nil {
		fmt.Fprintln(io.Out, "context: "+contextLine(*cu, io))
	}
	all := s.Decisions(st)
	printDecisions(io, all)
	printLeft(io, all)
	printSnapshots(io, snaps)
	printEarlier(io, s)
	printOthers(io, s)
	return 0
}

// printSaved says what phase agents saved a complete plan (state.SavingOf), when any worked on it.
func printSaved(io IO, st state.State) {
	if st.Mode != state.ModeComplete {
		return
	}
	if line := decide.Saved(state.SavingOf(st)); line != "" {
		fmt.Fprintln(io.Out, "time: "+line)
	}
}

// printEarlier lists the run's earlier plans, each kept as another replaced it (Store.Archive).
func printEarlier(io IO, s *state.Store) {
	earlier := s.Archives()
	if len(earlier) == 0 {
		return
	}
	fmt.Fprintln(io.Out, "earlier plans of this run:")
	for _, a := range earlier {
		title := a.Title
		if title == "" {
			title = "(no plan.json)"
		} else {
			title = fmt.Sprintf("%q", title)
		}
		fmt.Fprintf(io.Out, "  %s — %d of %d phases done, kept in %s\n", title, a.Done, a.Phases, shown(s, a.Dir))
	}
}

// phaseNote says where a phase not done stands, for baton status: how long it has been active, or why it
// is verifying or set aside, then the jobs it waits on and how each stands. "" for a phase done or not
// started.
func phaseNote(st state.State, id string, now time.Time) string {
	ps := st.Phases[id]
	var note string
	var jobs []state.Job
	switch ps.Status {
	case state.PhaseActive:
		note = "active"
		if !ps.StartedAt.IsZero() {
			note += " " + decide.Span(now.Sub(ps.StartedAt))
		}
		if id == st.Current {
			jobs = st.CurrentJobs()
		}
	case state.PhaseVerifying:
		note, jobs = "verifying", st.WaitsOn(id)
	case state.PhaseParked:
		note, jobs = "parked", st.WaitsOn(id)
		if pk := ps.Park; pk != nil {
			note += parkWhy[pk.Reason]
		}
	case state.PhaseWorking:
		if w := st.WorkerOn(id); w != nil {
			return "worker: " + workerState(*w, now)
		}
		return "worker"
	default:
		return ""
	}
	for i, j := range jobs {
		sep := "; "
		if i == 0 {
			sep = " — "
		}
		note += sep + "job " + j.ID + " " + jobProgress(j, now)
	}
	return note
}

// parkWhy is why a phase was set aside, as baton status says it.
var parkWhy = map[string]string{
	state.ParkJob:        " while its job runs",
	state.ParkRed:        ": its verification failed",
	state.ParkUnverified: ": its verification had no verdict",
	state.ParkCheckpoint: " at a checkpoint",
}

// clipLine is s on one line, cut to n characters.
func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// printBackground lists the model's own background work: what Claude Code reported at the last stop.
func printBackground(io IO, st state.State) {
	bg := st.Run.BusyBackground()
	if len(bg) == 0 {
		return
	}
	fmt.Fprintln(io.Out, "background work of Claude's own (each wakes Claude when it ends):")
	for _, t := range bg {
		what := t.Description
		if what == "" {
			what = t.ID
		}
		seen := ""
		if !t.Since.IsZero() {
			seen = "  (seen " + decide.Span(io.Now().Sub(t.Since)) + " ago)"
		}
		fmt.Fprintf(io.Out, "  %-8s %s%s\n", t.Type, clipLine(what, 80), seen)
	}
}

// printRuns lists a project's runs, for `baton status` from a shell when there is more than one.
func printRuns(io IO, runs []state.RunInfo, asJSON bool) {
	if asJSON {
		var out []map[string]any
		for _, r := range runs {
			out = append(out, map[string]any{"run": r.ID, "title": r.Title, "mode": r.State.Mode, "current": r.State.Current,
				"sessions": r.Sessions, "live": r.Live(io.Now())})
		}
		b, _ := json.MarshalIndent(map[string]any{"runs": out}, "", "  ")
		fmt.Fprintln(io.Out, string(b))
		return
	}
	fmt.Fprintln(io.Out, "baton: this project has several runs, one per session (newest first):")
	const most = 10
	for i, r := range runs {
		if i == most {
			fmt.Fprintf(io.Out, "  … and %d older (baton status --json lists them all)\n", len(runs)-most)
			break
		}
		fmt.Fprintf(io.Out, "  %s  %q — %s\n", runLabel(r, io.Now()), r.Title, runProgress(r))
	}
	fmt.Fprintln(io.Out, "Run /baton status inside a session for its run in full.")
}

// printOthers names the other sessions working in the same working tree right now. A worker has its
// worktree to itself.
func printOthers(io IO, s *state.Store) {
	if s.Worker() != "" {
		return
	}
	p, err := state.OpenProject(s.Project, io.Now)
	if err != nil {
		return
	}
	others := p.Others(s.Ref)
	if len(others) == 0 {
		return
	}
	fmt.Fprintln(io.Out, "other baton sessions working in this checkout:")
	for _, r := range others {
		title := r.Title
		if !r.HasPlan() {
			title = "no plan"
		}
		fmt.Fprintf(io.Out, "  %s  %q — %s\n", runLabel(r, io.Now()), title, runProgress(r))
	}
}

func runLabel(r state.RunInfo, now time.Time) string {
	label := short(r.ID)
	switch {
	case r.Pending:
		label += " (for the next session)"
	case r.Live(now):
		label += fmt.Sprintf(" (running, pid %d)", r.State.Owner.PID)
	}
	return label
}

func runProgress(r state.RunInfo) string {
	if r.State.Current != "" && r.State.Mode != state.ModeComplete {
		return r.State.Mode + ", on " + r.State.Current
	}
	return r.State.Mode
}

// printWaitingOnYou shows what waits on the human: what the watchdog escalated (a refused turn, an API
// error baton cannot retry, a stall, …), and of the run's decisions, a proposal, one held for them, or a
// review.
func printWaitingOnYou(io IO, st *state.State) {
	if e := st.Run.Escalation; e != nil && e.Watchdog {
		fmt.Fprintf(io.Out, "waiting on you since %s (%s): %s\n", e.Since.Local().Format("15:04"), e.Kind, e.Reason)
	}
	if p := st.PendingProposal(); p != nil {
		fmt.Fprintf(io.Out, "proposal %s: %s\n  because: %s\n", p.ID, p.What, p.Because)
		switch {
		case p.Relayed:
			fmt.Fprintln(io.Out, "  baton puts it to you in the lead's terminal, and gives this agent your answer")
		case p.Outward != "":
			fmt.Fprintf(io.Out, "  %s, so at %s baton answers Not now for you: Claude does it only if it is safely reversible and stays in the project, "+
				"and leaves it for you otherwise\n", p.Outward, p.Deadline.Local().Format("15:04"))
		case p.Asked:
			fmt.Fprintf(io.Out, "  goes ahead at %s unless you answer baton's question in the session\n", p.Deadline.Local().Format("15:04"))
		default:
			fmt.Fprintf(io.Out, "  goes ahead at %s unless you answer (Claude has yet to put the question to you)\n", p.Deadline.Local().Format("15:04"))
		}
		fmt.Fprintf(io.Out, "  undo: %s\n", decide.UndoOf(*p))
	}
	if e := st.Run.Escalation; e != nil && e.Kind == "decision" {
		fmt.Fprintf(io.Out, "held for you: %s — write in the session to answer it\n", e.Reason)
	}
	if st.ReviewDue != "" {
		fmt.Fprintf(io.Out, "review due: %s has made %d decisions without you, as many as it may before you go over them (below). "+
			"Answer baton's question in the session, or /baton run to let the run go on.\n", st.ReviewDue, st.Unattended(st.ReviewDue))
	}
}

// printRelay lists the questions the run's phase agents have for the human: those baton puts to them, one
// at a time, and those that wait for them, which /baton run asks them again.
func printRelay(io IO, s *state.Store, st state.State) {
	var open, here []string
	for i, r := range st.Relay {
		switch {
		case r.Answered == nil:
			where := "waits its turn"
			switch {
			case r.Asked && r.Outward != "":
				where = "asked; baton answers Not now for you at " + r.Deadline.Local().Format("15:04") + " unless you answer"
			case r.Asked && r.Timed():
				where = "asked; it goes ahead at " + r.Deadline.Local().Format("15:04") + " unless you answer"
			case r.Asked && r.Persistent():
				where = "asked; baton never answers it for you"
			case r.Asked && r.AboutAgent():
				where = "asked; unless you answer by " + r.Deadline.Local().Format("15:04") + ", baton decides from what " + r.Phase + " has done"
			case r.Asked:
				where = "asked; if you do not answer, it waits for you"
			case !r.Issued.IsZero():
				where = "asked as soon as no other dialog is open"
			case st.FrontRelay() == &st.Relay[i]:
				where = "next"
			}
			open = append(open, fmt.Sprintf("  ? %-6s %s — %s", r.Phase, relayWhat(r), where))
		case r.Persistent() && r.Answered.By != state.ByHuman && r.Answered.By != state.ByWithdrawn && st.AgentWaits(r):
			// It stepped aside unanswered, and comes back (loop's stoppedAgain).
			open = append(open, fmt.Sprintf("  ? %-6s %s — asked again when you next write in the session; /baton run starts a fresh agent on it instead",
				r.Phase, relayWhat(r)))
		case r.Answered.By != state.ByWithdrawn && leftHere(s, st, r):
			here = append(here, fmt.Sprintf("  ! %-6s %s", r.Phase, relayWhat(r)))
		}
	}
	if len(open) > 0 {
		fmt.Fprintln(io.Out, "questions from phase agents (baton puts them to you, one at a time):")
		fmt.Fprintln(io.Out, strings.Join(open, "\n"))
	}
	if len(here) > 0 {
		fmt.Fprintln(io.Out, "waiting for you (/baton run asks you again):")
		fmt.Fprintln(io.Out, strings.Join(here, "\n"))
	}
}

// relayWhat says what a phase agent's question is about.
func relayWhat(r state.Relay) string {
	switch r.Kind {
	case state.RelayProposal:
		return "proposal " + r.Decision + ": " + r.What
	case state.RelayReview:
		return fmt.Sprintf("a review of the %d decisions it made without you", r.Count)
	case state.RelayStopped:
		if r.Persistent() {
			return "you stopped its agent twice, and baton took it down: what should become of " + r.Phase + "?"
		}
		return "you stopped its agent: restart it?"
	case state.RelayRestarted:
		return "baton restarted its agent, as nobody answered: keep it going?"
	}
	return "blocked: " + r.Because
}

// leftHere reports phase agent question r, answered, that still waits for the human in the agent's
// sub-run (/baton run asks them again).
func leftHere(s *state.Store, st state.State, r state.Relay) bool {
	if r.AboutAgent() {
		return false // /baton run starts a fresh agent on a phase whose agent was stopped (takeUp)
	}
	if o := decide.RelayOutcome(r, r.Answered.Answer); o != decide.OutcomeWait && o != decide.OutcomePause {
		return false
	}
	e := st.WorkerOn(r.Phase)
	if e == nil {
		return false
	}
	p, err := state.OpenProject(s.Project, s.Now)
	if err != nil {
		return false
	}
	sub, err := p.ExistingRun(e.Ref, "")
	if err != nil {
		return false
	}
	sst, err := sub.Load()
	return err == nil && sst.Waits(r)
}

// printDecisions lists what the run (and its workers) decided without the human, each with its undo.
func printDecisions(io IO, all []state.Decision) {
	ds := decide.WithoutHuman(all)
	if len(ds) == 0 {
		return
	}
	fmt.Fprintln(io.Out, "decisions made without you (this run, oldest first):")
	for _, d := range ds {
		fmt.Fprintf(io.Out, "  %-4s %-8s %s  %s\n       undo: %s\n", d.ID, d.Phase, decide.Stamp(decide.When(d)), decide.Happened(d), decide.UndoOf(d))
	}
}

// printLeft lists the run's proposals (its workers' too) answered Not now: each was done only if it was
// safely reversible and stayed in the project, and a note then says so; otherwise it is left for the
// human.
func printLeft(io IO, all []state.Decision) {
	ds := decide.LeftForYou(all)
	if len(ds) == 0 {
		return
	}
	fmt.Fprintln(io.Out, "left for you (proposals answered Not now, oldest first):")
	for _, d := range ds {
		fmt.Fprintf(io.Out, "  %-4s %-8s %s  %s\n       undo: %s\n", d.ID, d.Phase, decide.Stamp(decide.When(d)), decide.LeftHow(d), decide.UndoOf(d))
	}
}

// printSnapshots lists the uncommitted work the host saved when the run halted, and how to get it back.
func printSnapshots(io IO, snaps []gitx.Saved) {
	if len(snaps) == 0 {
		return
	}
	const most = 5
	fmt.Fprintln(io.Out, "snapshots: uncommitted work baton saved when the run halted, newest first")
	for i, sn := range snaps {
		if i == most {
			fmt.Fprintf(io.Out, "  … and %d older (git for-each-ref %s)\n", len(snaps)-most, gitx.SnapshotRefs)
			break
		}
		fmt.Fprintf(io.Out, "  %s  %s  (%s)\n", sn.At.Local().Format("Jan 2 15:04"), sn.Ref, strings.TrimPrefix(sn.Subject, "baton snapshot: "))
	}
	fmt.Fprintln(io.Out, "  what one holds: git show --stat <ref>")
	fmt.Fprintln(io.Out, "  put it back in the working tree (overwrites those files): "+gitx.RestoreCommand("<ref>"))
}

// contextLine is the last context reading against the valves' thresholds. Outside the hosted session
// the settings come from baton's config, and the limit from the reading itself.
func contextLine(cu state.ContextUse, io IO) string {
	vs := valve.FromEnv(io.Env)
	if !hosted(io) {
		cfg := config.Load(batonRoot(io), io.Env)
		vs = valve.Settings{NoticePct: *cfg.NoticePct, CheckpointPct: *cfg.CheckpointPct, WarnPct: *cfg.WarnPct}
	}
	if vs.Cap == 0 {
		vs.Cap = cu.Limit
	}
	l := vs.Limits(cu.WindowSize)
	if l.Window == 0 {
		return fmt.Sprintf("%.0f%% of the window", cu.UsedPct)
	}
	parts := []string{valve.Tokens(cu.Used()) + " of " + valve.Tokens(l.Window)}
	if l.Notice > 0 {
		parts = append(parts, "heads-up at "+valve.Tokens(l.Notice))
	}
	if l.Checkpoint > 0 {
		parts = append(parts, "checkpoint at "+valve.Tokens(l.Checkpoint))
	}
	if l.Warn > 0 {
		parts = append(parts, "asks you at "+valve.Tokens(l.Warn))
	}
	return strings.Join(append(parts, "Claude Code compacts at about "+valve.Tokens(l.AutoAt)), " · ")
}

func cmdDone(args []string, io IO) int {
	p, err := parseArgs(args, []string{"notes", "keep-dirty", "despite-red", "land-check", "deviation"}, []string{"force"})
	if err != nil || len(p.pos) != 1 {
		return fail(io, "usage: baton done <phase> [--notes TEXT] [%s] [--keep-dirty WHY] [--despite-red WHY] [--land-check COMMAND] [--force]%s", decide.DeviationHelp, errSuffix(err))
	}
	id := p.pos[0]
	s, pl, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	cur, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	isHosted := hosted(io) // read before the run's lock: it reads processes
	if err := guard(isHosted, &cur, "not done", true); err != nil {
		return refused(io, s, "done", err)
	}
	// The plan file is read again as the phase ends, so a phase the human just added to it comes next. A
	// worker's plan is the run's narrowed to its phase: the plan file is the lead's to follow.
	var followed bool
	if s.Worker() == "" {
		adopted, changed, rerr := plan.Recheck(pl, state.Remaining(cur, pl))
		if followed = changed && rerr == nil && s.WritePlan(adopted) == nil; followed {
			pl = adopted
		}
	}
	if ps := cur.Phases[id]; s.Worker() == "" && ps != nil && ps.Status == state.PhaseWorking {
		return doneLanded(io, s, pl, cur, id, p.vals["notes"], followed)
	}
	if code, on := concludeMerge(io, s, "done", id); !on { // a phase agent's merge in progress (mergedone.go)
		return code
	}
	work := checkWork(s, id)
	keep := strings.TrimSpace(p.vals["keep-dirty"])
	if len(work.fresh) > 0 && keep == "" {
		s.Event("refused", map[string]any{"command": "done", "phase": id, "why": "uncommitted work", "files": len(work.fresh)})
		return fail(io, "%s", decide.UncommittedRefusal("done", id, fileList(work.fresh)))
	}
	if code, asked := askLeftovers(io, s, cur, id); asked {
		return code
	}
	ca := askConfirm(s, cur, pl, id) // a QA phase's full run after its reruns (confirm.go): it reads git and logs
	despite := decide.Sanitize(p.vals["despite-red"])
	landCheck := landCheckOf(s, p.vals["land-check"])
	deviation := decide.Sanitize(p.vals["deviation"])
	most := config.EscalationFromEnv(io.Env).MaxAuto
	var next, startHead string
	var note *state.Decision
	var count int
	var standing, reds []state.Job
	st, err := s.Update(func(st *state.State) error {
		if err := guard(isHosted, st, "not done", true); err != nil {
			return err
		}
		if ps := st.Phases[id]; ps != nil {
			startHead = ps.StartHead
		}
		if followed {
			st.PlanDrift = ""
			if added, _ := state.Extend(st, pl, io.Now()); len(added) > 0 {
				s.Event("plan_changed", map[string]any{"plan": pl.File, "adopted": true, "added": added})
			}
		}
		standing, reds = st.Standing(id), nil
		for _, j := range standing {
			if j.Red() {
				reds = append(reds, j)
			}
		}
		red := len(reds) > 0 && despite != ""
		if red {
			// Finishing a phase over a red verification is a decision without the human, like any note.
			if st.ReviewDue != "" {
				return reviewRefusal(st)
			}
			var what, logs []string
			for _, j := range reds {
				what = append(what, fmt.Sprintf("job %s, %s %s", j.ID, decide.Ran(j), decide.Outcome(j)))
				logs = append(logs, fmt.Sprintf("job %s found (its log: %s)", j.ID, jobLog(s, j)))
			}
			d, err := state.NoteOn(st, id, fmt.Sprintf("finished %s despite its red verification (%s): %s", id, strings.Join(what, "; "), despite),
				fmt.Sprintf("reopen %s: fix what %s, then verify it again", id, strings.Join(logs, " and what ")), io.Now())
			if err != nil {
				return err
			}
			note = &d
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "note", Text: d.What, At: io.Now()})
		}
		ca.queue(st, id, io.Now())
		var err error
		if next, err = state.Done(st, pl, id, io.Now(), state.DoneOpts{Force: p.bools["force"], DespiteRed: red}); err != nil {
			return err
		}
		recordLandCheck(st, id, landCheck)
		recordDone(st, id, deviation, red) // the risk ledger's (risk.go)
		if ps := st.Phases[id]; ps.Status == state.PhaseVerifying {
			if on := st.WaitsOn(id); len(on) > 0 {
				st.Run.Announce = append(st.Run.Announce, decide.VerifyingOn(id, on))
			}
		}
		if note != nil {
			if count = st.Unattended(id); decide.CapReached(count, most) {
				state.DueReview(st, id, io.Now())
			}
		}
		return nil
	})
	var re state.RedError
	if errors.As(err, &re) {
		return refusedRed(io, s, pl, re.Job, re.After)
	}
	if err != nil {
		return refused(io, s, "done", err)
	}
	s.AppendHandoff(id, p.vals["notes"])
	if note != nil {
		s.Event("note", map[string]any{"id": note.ID, "phase": id, "what": note.What, "undo": note.Undo, "despite_red": reds[0].ID})
		if st.ReviewDue != "" {
			s.Event("review_due", map[string]any{"phase": id, "decisions": count})
		}
	}
	verifying := st.Phases[id] != nil && st.Phases[id].Status == state.PhaseVerifying
	on := st.WaitsOn(id)
	verifying = verifying && len(on) > 0
	done := map[string]any{"phase": id, "next": next}
	if len(work.fresh) > 0 {
		done["keep_dirty"], done["files"] = keep, len(work.fresh)
	}
	if landCheck != "" {
		done["land_check"] = landCheck
	}
	if deviation != "" {
		done["deviation"] = deviation
	}
	if verifying {
		done["job"] = on[len(on)-1].ID
		if len(on) > 1 {
			done["beside"] = on[0].ID
		}
		s.Event("phase_verifying", done)
	} else {
		s.Event("phase_done", done)
	}
	work.report(io, id, keep)
	reportLandCheck(io, id, p.vals["land-check"], landCheck)
	if startHead != "" && startHead == gitx.Head(s.Root) {
		fmt.Fprintf(io.Out, "baton: warning — no commit since %s started. If this phase changed files, commit before ending your turn.\n", id)
	}
	switch {
	case note != nil:
		fmt.Fprintf(io.Out, "baton: noted (%s): %s finished despite its red verification; the human will be told.\n", note.ID, id)
	case despite != "":
		fmt.Fprintf(io.Out, "baton: note — %s's verification is not red, so --despite-red recorded nothing.\n", id)
	}
	for _, j := range standing {
		if j.Finished() && !j.Red() && j.Status != state.JobPassed {
			fmt.Fprintf(io.Out, "baton: note — %s's latest %s, job %s (%s), %s, so it checked nothing. Make sure it passes, in place if need be.\n",
				id, strings.TrimPrefix(decide.JobNoun(j.Kind), "a "), j.ID, decide.Ran(j), decide.Outcome(j))
		}
	}
	var mine []state.Decision
	for _, d := range decide.WithoutHuman(st.Decisions) {
		if d.Phase == id {
			mine = append(mine, d)
		}
	}
	if len(mine) > 0 {
		fmt.Fprintf(io.Out, "baton: %s made %d %s without the human (baton status lists them for the human, each with its undo):\n%s",
			id, len(mine), plural(len(mine), "decision", "decisions"), decide.Listed(mine, 0))
	}
	ca.report(io, s, id)
	finished := fmt.Sprintf("baton: %s done.", id)
	if verifying {
		finished = decide.FinishedVerifying(id, on)
	}
	if st.ReviewDue != "" {
		fmt.Fprintf(io.Out, "%s That makes as many decisions without the human as baton lets a phase make before they review them: "+
			"end your turn now, and baton stops for the human to review them.\n", finished)
		return 0
	}
	if s.Worker() != "" {
		// A worker's plan is its phase: done, its work is the run's to land.
		var jobs []state.Job
		if verifying {
			jobs = on
		}
		fmt.Fprintln(io.Out, "baton: "+decide.WorkerDone(id, jobs))
		return 0
	}
	if st.Mode == state.ModeComplete {
		fmt.Fprintf(io.Out, "%s That was the last phase — the plan is complete. %s\n", finished, decide.PlanComplete(s.Decisions(st)))
		return 0
	}
	if next == "" && st.HereGate() != nil {
		fmt.Fprintf(io.Out, "%s %s\n", finished, decide.HereHeld(*st.HereGate()))
		return 0
	}
	if next == "" && st.WoundDown() {
		fmt.Fprintf(io.Out, "%s %s\n", finished, decide.WindDownDone(st.Usage.GoesOnAt(), io.Now()))
		return 0
	}
	if next == "" {
		fmt.Fprintf(io.Out, "%s No other phase can start until a job finishes: every phase left waits for one. End your turn now; baton wakes you when there is something to do.\n", finished)
		return 0
	}
	np := pl.Phases[pl.Index(next)]
	fmt.Fprintf(io.Out, "%s Next phase: %s (%s). End your turn now: baton compacts the context, then starts %s with a fresh brief.\n", finished, np.ID, np.Title, np.ID)
	if inherited(io) {
		fmt.Fprintln(io.Out, notHostedNote(io, "this session is not hosted by baton, so no compaction will happen automatically"))
	} else if !hosted(io) {
		fmt.Fprintln(io.Out, "baton: note — this session is not hosted by baton, so no compaction will happen automatically.")
	}
	return 0
}

// refusedRed turns down done for a phase whose verification stands on red job j, naming the checks after
// it that did not clear it. A result the model has not been given yet comes with it, and counts as given.
func refusedRed(io IO, s *state.Store, pl plan.Plan, j state.Job, after []state.Job) int {
	var fresh bool
	s.Update(func(st *state.State) error {
		if x := st.Job(j.ID); x != nil && x.Delivered.IsZero() {
			x.Delivered, fresh = io.Now(), true
		}
		return nil
	})
	s.Event("refused", map[string]any{"command": "done", "phase": j.Phase, "why": "red", "job": j.ID})
	if fresh {
		fmt.Fprintln(io.Err, decide.JobResult(j, hooks.JobLog(s, j), nil, qaAhead(io, pl, j.Phase)))
	}
	return fail(io, "%s", decide.RedRefusal(j, after))
}

// jobLog is where job j's output is.
func jobLog(s *state.Store, j state.Job) string {
	if j.Log != "" {
		return j.Log
	}
	return s.JobLog(j.ID)
}

func cmdBlocked(args []string, io IO) int {
	p, err := parseArgs(args, []string{"tried", "keep-dirty"}, nil)
	reason := strings.TrimSpace(strings.Join(p.pos, " "))
	if err != nil || reason == "" {
		return fail(io, "usage: baton blocked \"<why>\" --tried \"<what you tried>\" [--keep-dirty \"<why it stays uncommitted>\"]%s", errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	tried := strings.TrimSpace(p.vals["tried"])
	if tried == "" {
		s.Event("refused", map[string]any{"command": "blocked", "why": "no --tried"})
		return fail(io, "not recorded — blocked stops the whole run until the human answers, so it needs --tried \"<what you tried>\".\n%s",
			words(io, s).Playbook())
	}
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	isHosted := hosted(io) // read before the run's lock: it reads processes
	if err := guard(isHosted, &st, "not recorded", true); err != nil {
		return refused(io, s, "blocked", err)
	}
	if isHosted && st.Mode == state.ModeRunning && !st.BoundaryOwed && !st.InHand() { // with a boundary owed, the next phase is the lead's
		return refused(io, s, "blocked", refusal{"no phase in hand", decide.NoPhase("not recorded")})
	}
	work := checkWork(s, st.Current)
	keep := strings.TrimSpace(p.vals["keep-dirty"])
	if len(work.fresh) > 0 && keep == "" {
		s.Event("refused", map[string]any{"command": "blocked", "phase": st.Current, "why": "uncommitted work", "files": len(work.fresh)})
		return fail(io, "%s", decide.UncommittedRefusal("blocked", st.Current, fileList(work.fresh)))
	}
	var since time.Time
	if _, err := s.Update(func(st *state.State) error {
		if err := guard(isHosted, st, "not recorded", true); err != nil {
			return err
		}
		if err := state.SetBlocked(st, reason, io.Now()); err != nil {
			return err
		}
		st.Blocked.Tried, since = tried, st.Blocked.Since
		return nil
	}); err != nil {
		return refused(io, s, "blocked", err)
	}
	blocked := map[string]any{"reason": reason, "tried": tried}
	if len(work.fresh) > 0 {
		blocked["keep_dirty"], blocked["files"] = keep, len(work.fresh)
	}
	s.Event("blocked", blocked)
	work.report(io, st.Current, keep)
	if s.Worker() != "" {
		if err := s.Relay(state.Relay{Kind: state.RelayBlocked, Since: since, Because: reason}); err != nil {
			fmt.Fprintf(io.Err, "baton: warning — could not queue it for the lead session yet (%v); baton queues it again when you end your turn.\n", err)
		}
		fmt.Fprintln(io.Out, "baton: "+decide.WorkerBlocked(st.Current))
		return 0
	}
	fmt.Fprintln(io.Out, "baton: recorded. End your turn; baton will bring the human in.")
	return 0
}

// workLeft is what the gate on uncommitted work found when a phase ends or the run halts. It applies only
// where git is usable (gitx.Usable); a plain folder has nothing to commit and is never refused.
//
// The gate prompts the model to commit before it leaves work behind; it is not the guarantee. A model
// can get past any gate it can satisfy itself (docs/research/escalation.md, S2), so the host also
// snapshots uncommitted work whenever the run halts.
type workLeft struct {
	fresh []string // new or changed since the phase started: refused without --keep-dirty
	kept  []string // already there when the phase started, and unchanged: only noted
	// others are new or changed since the phase started, while another session works in the same
	// checkout, and this session's edit tools never wrote them: they may be that session's, so they are
	// only noted.
	others []string
	warn   string // baton could not tell the two apart, so it warns instead
}

// checkWork compares the uncommitted work now with what there was when phase started.
func checkWork(s *state.Store, phase string) workLeft {
	w := s.PhaseWork(phase)
	out := workLeft{fresh: w.Fresh, kept: w.Kept, others: w.Others}
	switch {
	case w.Err != nil:
		out.warn = fmt.Sprintf("baton: warning — could not read git status (%v). Make sure this phase's work is committed.", w.Err)
	case w.NoStart:
		out.warn = fmt.Sprintf("baton: warning — there is uncommitted work, and baton has no record of what was uncommitted when %s started:\n%s\nIf this phase made it, commit it.", phase, fileList(w.Paths))
	case w.TooMany:
		out.warn = fmt.Sprintf("baton: warning — %d paths are uncommitted, too many for baton to check one by one. Make sure this phase's work is committed.", len(w.Paths))
	}
	return out
}

// report prints what the gate let through.
func (w workLeft) report(io IO, phase, keep string) {
	if w.warn != "" {
		fmt.Fprintln(io.Out, w.warn)
	}
	if len(w.fresh) > 0 && keep != "" {
		fmt.Fprintf(io.Out, "baton: leaving %d %s uncommitted (--keep-dirty: %s).\n", len(w.fresh), plural(len(w.fresh), "file", "files"), keep)
	}
	if n := len(w.others); n > 0 {
		fmt.Fprintf(io.Out, "baton: note — another baton session is working in this checkout, and %d uncommitted %s changed during %s without this session editing %s; "+
			"%s may be the other session's, so baton leaves %s alone (commit only your own files, by path):\n%s\n",
			n, plural(n, "file", "files"), phase, plural(n, "it", "them"), plural(n, "it", "they"), plural(n, "it", "them"), fileList(w.others))
	}
	if n := len(w.kept); n > 0 {
		fmt.Fprintf(io.Out, "baton: note — %d uncommitted %s already there when %s started and %s not changed; baton leaves %s alone:\n%s\n",
			n, plural(n, "file was", "files were"), phase, plural(n, "has", "have"), plural(n, "it", "them"), fileList(w.kept))
	}
}

// fileList indents paths one per line, at most 15 of them.
func fileList(paths []string) string {
	const most = 15
	var b strings.Builder
	for i, p := range paths {
		if i == most {
			fmt.Fprintf(&b, "    … and %d more\n", len(paths)-most)
			break
		}
		b.WriteString("    " + p + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func cmdCheckpoint(args []string, io IO) int {
	p, err := parseArgs(args, []string{"notes"}, nil)
	if err != nil || len(p.pos) != 0 {
		return fail(io, "usage: baton checkpoint [--notes TEXT]%s", errSuffix(err))
	}
	s, pl, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	notes := p.vals["notes"]
	cur, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	// A checkpoint is a switch point (state.SwitchAtCheckpoint), but never with the phase's work
	// uncommitted: it would sit in the checkout while the other phase works there.
	from := cur.Current
	work := checkWork(s, from)
	clean := len(work.fresh) == 0 && work.warn == ""
	var to, back, why string
	var epoch int
	isHosted := hosted(io) // read before the run's lock: it reads processes
	_, err = s.Update(func(st *state.State) error {
		if err := guard(isHosted, st, "not recorded", true); err != nil {
			return err
		}
		if err := state.SetCheckpoint(st, io.Now()); err != nil {
			return err
		}
		back = state.Returning(*st, pl)
		if back != "" {
			why = returnReason(*st, back)
		}
		if !clean || st.Current != from {
			return nil
		}
		if to = state.SwitchAtCheckpoint(st, pl, notes, io.Now()); to != "" {
			epoch = st.Run.Compaction.Epoch
			st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: checkpoint → %s set aside; compacting, then %s resumes (%s)", from, to, why))
		}
		return nil
	})
	if err != nil {
		return refused(io, s, "checkpoint", err)
	}
	s.AppendHandoff(from+" (checkpoint)", notes)
	s.Event("checkpoint", map[string]any{"phase": from})
	if to != "" {
		s.Event("phase_parked", map[string]any{"phase": from, "reason": state.ParkCheckpoint})
		s.Event("phase_switched", map[string]any{"from": from, "to": to, "by": "checkpoint"})
		s.Event("compact_queued", map[string]any{"reason": "switch", "epoch": epoch, "next": to, "by": "checkpoint"})
		fmt.Fprintf(io.Out, "baton: checkpoint recorded. %s can go on now (%s), so baton sets %s aside here and takes %s up again; %s resumes from your notes later. "+
			"End your turn now: baton compacts the context, then %s resumes with a fresh brief.\n", to, why, from, to, from, to)
		return 0
	}
	if len(work.fresh) > 0 {
		fmt.Fprintf(io.Out, "baton: note — %d %s changed in %s %s not committed yet:\n%s\nIf they are a finished step, commit them before you end your turn.\n",
			len(work.fresh), plural(len(work.fresh), "file", "files"), from, plural(len(work.fresh), "is", "are"), fileList(work.fresh))
	} else if work.warn != "" {
		fmt.Fprintln(io.Out, work.warn)
	}
	if back != "" && notes != "" && !clean {
		fmt.Fprintf(io.Out, "baton: note — %s can go on now (%s), and a checkpoint is where baton takes it up again, but only with %s's work committed. "+
			"Commit it, then checkpoint again to switch; or end your turn to go on with %s.\n", back, why, from, from)
	}
	fmt.Fprintf(io.Out, "baton: checkpoint recorded. End your turn now: baton compacts the context and you continue %s from your notes.\n", from)
	return 0
}

// returnReason says why a parked phase can go on again (state.Returning): its job has ended, or its
// verification did.
func returnReason(st state.State, id string) string {
	ps := st.Phases[id]
	if ps == nil || ps.Park == nil {
		return "it was set aside"
	}
	if j := st.Job(ps.Park.Job); j != nil && j.Finished() {
		return fmt.Sprintf("its job %s %s", j.ID, decide.Outcome(*j))
	}
	return "its verification ended"
}

func cmdPause(_ []string, io IO) int {
	return simpleTransition(io, "pause", "paused", state.Pause, fmt.Sprintf("baton: paused. baton will not compact, nudge or escalate until /baton run. "+
		"If the conversation grows by more than %s tokens meanwhile, the run compacts before it goes on.", valve.Tokens(state.ResumeGrowth)))
}

// cmdRun is the first step of /baton run: it decides what running a plan means in this session, so that
// the model does not have to. The session's own plan carries on (no file, or its file): a run paused or
// stopped for the human is resumed, and a running one is left alone. Any other plan is attached (baton
// attach), but never over a run underway without the human's say, and a finished plan is never run
// again by accident.
func cmdRun(args []string, io IO) int {
	p, err := parseArgs(args, nil, nil)
	if err != nil || len(p.pos) > 1 {
		return fail(io, "usage: baton run [plan file]%s", errSuffix(err))
	}
	var file, name string
	if len(p.pos) == 1 {
		name = p.pos[0]
		if file, err = filepath.Abs(name); err != nil {
			return fail(io, "%v", err)
		}
	}
	attach := func(why string) int {
		if file == "" {
			fmt.Fprintf(io.Out, "baton: %s, so attach a plan: find its file and attach it as /baton run describes.\n", why)
		} else {
			fmt.Fprintf(io.Out, "baton: %s, so attach %s: baton attach %s --suggest\n", why, name, name)
		}
		return 0
	}
	s, err := store(io)
	switch {
	case errors.Is(err, state.ErrNoPlan):
		if c, ok := carry(io, file); ok {
			return runCarried(io, c)
		}
		return attach("no plan is underway in this session")
	case err != nil:
		return fail(io, "%v", err)
	}
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	pl, perr := s.LoadPlan()
	if st.Mode == state.ModeIdle || perr != nil {
		if c, ok := carry(io, file); ok {
			return runCarried(io, c)
		}
		return attach("no plan is underway in this session")
	}
	same := file == "" || file == pl.File || file == pl.Origin && gone(file) // named by the path baton moved it from
	switch {
	case st.Mode == state.ModeComplete && !same:
		return attach(fmt.Sprintf("%q, this session's last plan, is complete", pl.Title))
	case st.Mode == state.ModeComplete:
		switch adopted, changed, err := plan.Recheck(pl, nil); {
		case changed && err == nil && len(adopted.Phases) > len(pl.Phases):
			return runAdded(io, s, adopted)
		case changed && err != nil && pl.SHA() != "":
			// Nothing done is checked again, so what fails is a phase the file adds.
			first := strings.SplitN(err.Error(), "\n", 2)[0]
			return fail(io, "%q is complete, and its file adds phases baton cannot follow: %s. Fix %s, then /baton run again.", pl.Title, first, filepath.Base(pl.File))
		}
		if file == "" {
			fmt.Fprintf(io.Out, "baton: %q is complete: every phase is done, so there is nothing to run. Ask the human which plan to run next.\n", pl.Title)
			return 0
		}
		fmt.Fprintf(io.Out, "baton: %q is complete: every phase is done, so there is nothing to run. To run it again from the start, /baton drop it first, then /baton run it.\n", pl.Title)
		return 0
	case !same:
		fmt.Fprintf(io.Out, "baton: %q is %s in this session, at %s. Running %s instead discards that run's progress: ask the human first, and only if they agree, attach %s with --replace.\n",
			pl.Title, st.Mode, phaseLabel(pl, st.Current), name, name)
		return 0
	}
	fresh := append(takeUp(io, s), askAgain(io, s)...)
	for _, line := range fresh {
		fmt.Fprintln(io.Out, line)
	}
	if !st.Resumable() {
		if len(fresh) == 0 {
			fmt.Fprintf(io.Out, "baton: %q is already running, at %s. Nothing to resume.\n", pl.Title, phaseLabel(pl, st.Current))
		}
		return 0
	}
	return resume(io, s, pl, st)
}

// askAgain puts each question of the run's phase agents that waits for the human back in the queue, to be
// asked again (state.AskAgain): nobody answered it with a way on (Wait for me, Pause baton, none), and its
// phase still waits on it. It returns a line for the human about each.
func askAgain(io IO, s *state.Store) []string {
	st, err := s.Load()
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range st.Relay {
		if r.Answered == nil || !leftHere(s, st, r) { // AskAgain asks it again only once its answer was applied
			continue
		}
		again := false
		s.Update(func(st *state.State) error {
			again = state.AskAgain(st, r, io.Now())
			return nil
		})
		if again {
			fields := map[string]any{"phase": r.Phase, "type": r.Kind}
			if r.Decision != "" {
				fields["decision"] = r.Decision
			}
			s.Event("relay_again", fields)
			out = append(out, decide.RelayAgain(r.Phase))
		}
	}
	return out
}

// takeUp asks for a fresh phase agent on each of the run's phases that waits for the human because of its
// agent: one they stopped from the agent panel (a held worker), or one whose last turn was refused or
// failed in a way baton does not retry (its sub-run escalated refused or api_error), which /baton run is
// one of the ways on from. The host starts each (a suspended worker: loop.Coordinator), in its worktree.
// It returns a line for the human about each.
func takeUp(io IO, s *state.Store) []string {
	p, err := project(io)
	if err != nil {
		return nil
	}
	st, err := s.Load()
	if err != nil {
		return nil
	}
	now := io.Now()
	var out []string
	for _, w := range st.LiveWorkers() {
		why := ""
		switch w.Status {
		case state.WorkerHeld:
			why = "you stopped the agent before it"
		case state.WorkerWorking, state.WorkerSuspended: // suspended: its agent ended with the lead's claude since
			sub, err := p.ExistingRun(w.Ref, instance(io))
			if err != nil {
				continue
			}
			var kind string
			sub.Update(func(sst *state.State) error {
				if e := sst.Run.Escalation; e != nil && (e.Kind == "refused" || e.Kind == "api_error") {
					kind, sst.Run.Escalation, sst.Run.LastError = e.Kind, nil, nil
				}
				return nil
			})
			switch kind {
			case "refused":
				why = "the model's safeguards declined the last turn of the agent before it"
			case "api_error":
				why = "the agent before it stopped on an API error baton does not retry"
			default:
				continue
			}
			sub.Event("escalation_cleared", map[string]any{"type": kind, "by": "run"})
		default:
			continue
		}
		taken := false
		s.Update(func(st *state.State) error {
			if e := st.Entry(w); e != nil && e.Live() {
				e.Status, e.Since, e.Why, e.Resume, taken = state.WorkerSuspended, now, "the human asked for a fresh agent: "+why, "", true
				if e.Stops >= 2 {
					e.Stops = 0 // the human asked for it after a second stop, as Restart it does then (loop/stopped.go)
				}
				e.TakenDown, e.Released = time.Time{}, time.Time{}
			}
			return nil
		})
		if taken {
			s.Event("worker_afresh", map[string]any{"phase": w.Phase, "why": why})
			out = append(out, decide.AgentAfresh(w.Phase, why))
		}
	}
	return out
}

// runAdded runs the phases the human added to a plan after it finished: they start after a compaction,
// as a phase does at a boundary, and the phases done stay done.
func runAdded(io IO, s *state.Store, pl plan.Plan) int {
	if err := s.WritePlan(pl); err != nil {
		return fail(io, "%v", err)
	}
	var added []string
	var next string
	if _, err := s.Update(func(st *state.State) error {
		added, _ = state.Extend(st, pl, io.Now())
		st.PlanDrift = ""
		next = state.Revise(st, pl, io.Now())
		return nil
	}); err != nil {
		return fail(io, "%v", err)
	}
	s.Event("plan_changed", map[string]any{"plan": pl.File, "adopted": true, "added": added})
	s.Event("revised", map[string]any{"plan": pl.File, "phases": len(pl.Phases), "next": next, "by": "run"})
	fmt.Fprintf(io.Out, "baton: %q was complete, and its file now adds %s. ", pl.Title, phaseLabels(pl, added))
	if hosted(io) {
		fmt.Fprintf(io.Out, "End your turn now: baton compacts this conversation, then starts %s with a fresh brief.\n", phaseLabel(pl, next))
		return 0
	}
	fmt.Fprintf(io.Out, "%s starts once baton has compacted this conversation.\n", phaseLabel(pl, next))
	fmt.Fprintln(io.Out, notHostedNote(io, "this session is not hosted by baton, so nothing will compact automatically"))
	return 0
}

// resume gives a paused or stopped run back to baton. A plan file that changed so that baton can no
// longer follow it is reported, and the run goes on with the plan as attached. A compaction the run kept while it was paused counts as the one
// it owed (state.Keep): the phase owed starts with no compaction of its own, its brief handed over with
// this command's result (hooks.Owe), or the checkpoint is over. Otherwise, if the conversation grew by
// more than state.ResumeGrowth while the run was paused, the phase goes on from a compacted context: the
// model records where it stands first, and the hooks hold it to that (CheckpointDue). A run that holds no
// phase takes up one that can start (state.Extend): one the plan file adds, or one free to start since the
// run was paused.
func resume(io IO, s *state.Store, pl plan.Plan, cur state.State) int {
	adopted, changed, err := plan.Recheck(pl, state.Remaining(cur, pl))
	if err != nil {
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		s.Event("plan_changed", map[string]any{"plan": pl.File, "adopted": false, "why": first, "kept": true, "by": "run"})
		fmt.Fprintf(io.Out, "baton: the plan file changed, and baton cannot follow it (%s): the run goes on with the plan as attached. Fix %s and baton follows the file again.\n",
			first, filepath.Base(pl.File))
		changed = false
	}
	if changed {
		if err := s.WritePlan(adopted); err != nil {
			return fail(io, "%v", err)
		}
		pl = adopted
	}
	// What the repository looks like as a phase starts on a compaction kept while paused (read before
	// taking the lock: git can be slow).
	var origin state.Origin
	if !cur.Covered.IsZero() && cur.BoundaryOwed {
		origin = state.OriginOf(s.Root)
	}
	var grew int
	var added []string
	var took string
	var kept state.After
	var keeps, covered bool
	var owed hooks.Owing
	isHosted := hosted(io) // read before the run's lock: it reads processes
	st, err := s.Update(func(st *state.State) error {
		if err := guard(isHosted, st, "not resumed", false); err != nil {
			return err
		}
		if changed {
			st.PlanDrift = ""
		}
		grew, covered = st.GrownWhilePaused(), !st.Covered.IsZero()
		kept, keeps = state.Keep(st, io.Now(), origin)
		if err := state.Resume(st); err != nil {
			return err
		}
		if keeps {
			owed = hooks.Owe(st, pl, kept, false, io.Now())
		}
		added, took = state.Extend(st, pl, io.Now())
		return nil
	})
	if err != nil {
		return refused(io, s, "run", err)
	}
	if keeps {
		s.Event("compaction_kept", map[string]any{"phase": owed.Brief.Phase, "reason": kept.Reason, "epoch": kept.Epoch, "grew": grew})
		owed.Log(s)
	} else if covered {
		s.Event("compaction_not_kept", map[string]any{"grew": grew})
	}
	if len(added) > 0 {
		s.Event("plan_changed", map[string]any{"plan": pl.File, "adopted": true, "added": added})
		if took == "" {
			fmt.Fprintf(io.Out, "baton: the plan file now adds %s, which run in turn.\n", phaseLabels(pl, added))
		} else {
			fmt.Fprintf(io.Out, "baton: the plan file now adds %s.\n", phaseLabels(pl, added))
		}
	}
	at := phaseLabel(pl, st.Current)
	switch {
	case took != "":
		verb := "starts"
		if ps := st.Phases[took]; ps != nil && ps.Status == state.PhaseParked {
			verb = "resumes"
		}
		s.Event("resumed", map[string]any{"next": took})
		fmt.Fprintf(io.Out, "baton: resumed %q. No phase was in hand, and %s can start: end your turn now, so baton compacts this conversation, then %s %s with a fresh brief.\n",
			pl.Title, at, took, verb)
		return 0
	case st.Current == "" && st.Mode == state.ModeRunning:
		s.Event("resumed", nil)
		fmt.Fprintf(io.Out, "baton: resumed %q. No phase can start yet: every phase left waits for a job or for a worker's work to land. End your turn now; baton wakes you when there is something to do.\n", pl.Title)
		return 0
	}
	if keeps {
		s.Event("resumed", map[string]any{"kept": true})
		fmt.Fprintf(io.Out, "baton: resumed %q at %s. %s\n", pl.Title, at, decide.ResumeKept(at, kept.Started, kept.Resumed))
		return 0
	}
	if !st.CheckpointDue {
		s.Event("resumed", nil)
		fmt.Fprintf(io.Out, "baton: resumed %q at %s. baton drives the plan again.\n", pl.Title, at)
		return 0
	}
	s.Event("resumed", map[string]any{"grew": grew, "checkpoint_due": true})
	fmt.Fprintf(io.Out, "baton: resumed %q at %s. The conversation grew by %s tokens while baton was paused. %s\n", pl.Title, at, valve.Tokens(grew), decide.CheckpointOnResume(st.Current))
	return 0
}

// phaseLabels names phases as the human knows them: "P3 (Polish) and P4 (Docs)".
func phaseLabels(pl plan.Plan, ids []string) string {
	var names []string
	for _, id := range ids {
		names = append(names, phaseLabel(pl, id))
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// phaseLabel is a phase as the human knows it: "P2 (The one-liners)".
func phaseLabel(pl plan.Plan, id string) string {
	if i := pl.Index(id); i >= 0 {
		return id + " (" + pl.Phases[i].Title + ")"
	}
	return id
}

func cmdDrop(args []string, io IO) int {
	if len(args) != 0 {
		return fail(io, "usage: baton drop")
	}
	s, err := store(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	pl, _ := s.LoadPlan()
	var stopped []state.Job
	var logs int
	var workers []string
	var rec *state.Record
	var left *resumeCancel
	if _, err := s.Update(func(st *state.State) error {
		stopped, logs = st.Unfinished(), len(st.Jobs)
		for _, w := range st.LiveWorkers() {
			workers = append(workers, w.Phase)
		}
		if st.Mode == state.ModeIdle {
			return state.Detach(st) // refused: nothing to drop
		}
		var err error
		if rec, err = s.Record(*st); err != nil { // written once the lock is let go
			return fmt.Errorf("could not keep the record of the plan: %w", err)
		}
		left = leaveResume(st, state.CancelDrop, io.Now())
		return state.Detach(st)
	}); err != nil {
		return refused(io, s, "drop", err)
	}
	resumeLeft(s, left)
	kept := keepRecord(io, s, rec)
	s.KeepReplans(kept) // the dropped plan's replans go with its record
	// Taken off the record, the jobs stop at their runners' next beat; told to, they stop now.
	stopRunners(stopped, io.Now())
	os.RemoveAll(jobsDir(s))
	lanesDown(io, s.Checkout, s.Project, instance(io))
	dropped := map[string]any{"plan": pl.File}
	if len(stopped) > 0 {
		dropped["stopped"] = jobIDs(stopped)
	}
	s.Event("dropped", dropped)
	fmt.Fprintf(io.Out, "baton: dropped %q. Its plan is set aside (its history stays in baton's event log), and baton does nothing in this session until the next plan: /baton plan or /baton run.\n", pl.Title)
	if kept != "" {
		fmt.Fprintf(io.Out, "baton: its record (its state, plan and notes) is kept in %s.\n", shown(s, kept))
	}
	switch {
	case len(stopped) > 0:
		fmt.Fprintf(io.Out, "baton: it stopped %s, and removed the logs of the run's jobs.\n", jobLabels(stopped))
	case logs > 0:
		fmt.Fprintln(io.Out, "baton: it removed the logs of the run's jobs.")
	}
	if len(workers) > 0 {
		fmt.Fprintln(io.Out, "baton: "+decide.DropWorkers(phaseLabels(pl, workers)))
	}
	return 0
}

func simpleTransition(io IO, command, kind string, fn func(*state.State) error, msg string) int {
	s, err := store(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	if _, err := s.Update(fn); err != nil {
		return refused(io, s, command, err)
	}
	s.Event(kind, nil)
	fmt.Fprintln(io.Out, msg)
	return 0
}

// words is what the model-facing text depends on in this project.
func words(io IO, s *state.Store) decide.Words {
	return decide.Words{Git: gitx.Usable(s.Root), Timeout: config.EscalationFromEnv(io.Env).Timeout, Worker: s.Worker() != ""}
}

func storeAndPlan(io IO) (*state.Store, plan.Plan, bool) {
	s, err := store(io)
	if err != nil {
		fail(io, "%v", err)
		return nil, plan.Plan{}, false
	}
	pl, err := s.LoadPlan()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail(io, "%v", state.ErrNoPlan)
		} else {
			fail(io, "%v", err)
		}
		return nil, plan.Plan{}, false
	}
	return s, pl, true
}
