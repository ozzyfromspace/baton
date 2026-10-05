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
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
)

// These commands are run by the model (through the Bash tool) and by the /baton skill. Their output is
// read by the model, so it is short, literal, and says what to do next.

func init() {
	register("attach", "attach a plan document: --suggest shows the phases, --suggested or --spec '<json>' attaches them", cmdAttach)
	register("status", "show the attached plan and where the run is (--json for machines)", cmdStatus)
	register("done", "mark the current phase finished: done <phase> [--notes TEXT]", cmdDone)
	register("blocked", "report that you cannot continue without the human: blocked <reason>", cmdBlocked)
	register("waiting", "declare a bounded wait: waiting <what> --until <duration, e.g. 20m>", cmdWaiting)
	register("checkpoint", "ask for a mid-phase compaction at this safe point [--notes TEXT]", cmdCheckpoint)
	register("pause", "stop baton from acting until resume (the human takes the wheel)", cmdPause)
	register("resume", "let baton drive again (also clears a block)", cmdResume)
}

func store(io IO) (*state.Store, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return state.Open(state.Locate(cwd, io.Env), io.Env("BATON_INSTANCE"), io.Now)
}

func fail(io IO, format string, a ...any) int {
	fmt.Fprintf(io.Err, "baton: "+format+"\n", a...)
	return 1
}

func hosted(io IO) bool { return io.Env("BATON_HOST") == "1" }

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
		out, _ := json.MarshalIndent(plan.Suggest(doc), "", "  ")
		fmt.Fprintln(io.Out, string(out))
		fmt.Fprintln(io.Err, "baton: this is a suggestion. Check every phase is present and in order, ids and titles are right, and each "+
			"anchor is a verbatim snippet that starts its phase. If it is right as printed: baton attach "+p.pos[0]+" --suggested. "+
			"Otherwise pass the corrected spec inline: baton attach "+p.pos[0]+" --spec '<json>'")
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
	pl, err := plan.Build(file, doc, spec)
	if err != nil {
		return fail(io, "the spec does not match the plan document:\n%v", err)
	}
	s, err := store(io)
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
	if err := s.SavePlan(pl); err != nil {
		return fail(io, "%v", err)
	}
	head := gitx.Head(filepath.Dir(s.Dir))
	if _, err := s.Update(func(st *state.State) error { *st = state.Reattach(*st, pl, io.Now(), head); return nil }); err != nil {
		return fail(io, "%v", err)
	}
	if err := state.ExcludeFromGit(s.Dir); err != nil {
		fmt.Fprintf(io.Err, "baton: warning: could not add .baton/ to .git/info/exclude: %v\n", err)
	}
	s.Event("attached", map[string]any{"plan": file, "phases": len(pl.Phases)})
	fmt.Fprintf(io.Out, "baton: attached %q — %d phases. Current phase: %s (%s).\n", pl.Title, len(pl.Phases), pl.Phases[0].ID, pl.Phases[0].Title)
	if !hosted(io) {
		fmt.Fprintln(io.Out, "baton: note — this session is not hosted by baton, so nothing will compact automatically. Run /baton elevate (or start claude with `baton`).")
	}
	return 0
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
	if err != nil {
		return fail(io, "%v", err)
	}
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	pl, perr := s.LoadPlan()
	if p.bools["json"] {
		out := map[string]any{"state": st, "baton_dir": s.Dir, "hosted": hosted(io)}
		if perr == nil {
			out["plan"] = pl
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(io.Out, string(b))
		return 0
	}
	if st.Mode == state.ModeIdle || perr != nil {
		fmt.Fprintln(io.Out, "baton: no plan attached here.")
		return 0
	}
	fmt.Fprintf(io.Out, "baton: %q — %s%s\nplan: %s\n", pl.Title, st.Mode, map[bool]string{true: " (hosted)", false: " (not hosted)"}[hosted(io)], pl.File)
	for _, ph := range pl.Phases {
		mark, extra := "·", ""
		if ps := st.Phases[ph.ID]; ps != nil {
			switch ps.Status {
			case state.PhaseDone:
				mark = "✓"
			case state.PhaseActive:
				mark, extra = "▶", fmt.Sprintf("  (active %s)", io.Now().Sub(ps.StartedAt).Round(time.Minute))
			}
		}
		if ph.ID == st.Current && st.BoundaryOwed {
			mark, extra = "→", "  (next, after compaction)"
		}
		fmt.Fprintf(io.Out, "  %s %-8s %s%s\n", mark, ph.ID, ph.Title, extra)
	}
	if st.Blocked != nil {
		fmt.Fprintf(io.Out, "blocked: %s\n", st.Blocked.Reason)
	}
	if st.Waiting != nil {
		fmt.Fprintf(io.Out, "waiting: %s (until %s)\n", st.Waiting.What, st.Waiting.Until.Local().Format("15:04"))
	}
	if st.CheckpointOwed {
		fmt.Fprintln(io.Out, "checkpoint: a mid-phase compaction is owed at the next stop")
	}
	if cu := st.Run.Context; cu != nil {
		fmt.Fprintln(io.Out, "context: "+contextLine(*cu, io))
	}
	return 0
}

// contextLine is the last context reading against the valves' thresholds. Outside the hosted session
// the settings come from baton's config, and the limit from the reading itself.
func contextLine(cu state.ContextUse, io IO) string {
	vs := valve.FromEnv(io.Env)
	if !hosted(io) {
		cfg := config.Load(batonRoot(io), io.Env)
		vs = valve.Settings{CheckpointPct: *cfg.CheckpointPct, WarnPct: *cfg.WarnPct}
	}
	if vs.Cap == 0 {
		vs.Cap = cu.Limit
	}
	l := vs.Limits(cu.WindowSize)
	if l.Window == 0 {
		return fmt.Sprintf("%.0f%% of the window", cu.UsedPct)
	}
	parts := []string{valve.Tokens(cu.Used()) + " of " + valve.Tokens(l.Window)}
	if l.Checkpoint > 0 {
		parts = append(parts, "checkpoint at "+valve.Tokens(l.Checkpoint))
	}
	if l.Warn > 0 {
		parts = append(parts, "asks you at "+valve.Tokens(l.Warn))
	}
	return strings.Join(append(parts, "Claude Code compacts at about "+valve.Tokens(l.AutoAt)), " · ")
}

func cmdDone(args []string, io IO) int {
	p, err := parseArgs(args, []string{"notes"}, []string{"force"})
	if err != nil || len(p.pos) != 1 {
		return fail(io, "usage: baton done <phase> [--notes TEXT] [--force]%s", errSuffix(err))
	}
	id := p.pos[0]
	s, pl, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	var next string
	var startHead string
	st, err := s.Update(func(st *state.State) error {
		if ps := st.Phases[id]; ps != nil {
			startHead = ps.StartHead
		}
		var err error
		next, err = state.Done(st, pl, id, io.Now(), p.bools["force"])
		return err
	})
	if err != nil {
		return fail(io, "%v", err)
	}
	s.AppendHandoff(id, p.vals["notes"])
	s.Event("phase_done", map[string]any{"phase": id, "next": next})
	if startHead != "" && startHead == gitx.Head(filepath.Dir(s.Dir)) {
		fmt.Fprintf(io.Out, "baton: warning — no commit since %s started. If this phase changed files, commit before ending your turn.\n", id)
	}
	if st.Mode == state.ModeComplete {
		fmt.Fprintf(io.Out, "baton: %s done. That was the last phase — the plan is complete. Summarize the results for the human and end your turn.\n", id)
		return 0
	}
	np := pl.Phases[pl.Index(next)]
	fmt.Fprintf(io.Out, "baton: %s done. Next phase: %s (%s). End your turn now: baton compacts the context, then starts %s with a fresh brief.\n", id, np.ID, np.Title, np.ID)
	if !hosted(io) {
		fmt.Fprintln(io.Out, "baton: note — this session is not hosted by baton, so no compaction will happen automatically.")
	}
	return 0
}

func cmdBlocked(args []string, io IO) int {
	reason := strings.TrimSpace(strings.Join(args, " "))
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	if _, err := s.Update(func(st *state.State) error { return state.SetBlocked(st, reason, io.Now()) }); err != nil {
		return fail(io, "%v", err)
	}
	s.Event("blocked", map[string]any{"reason": reason})
	fmt.Fprintln(io.Out, "baton: recorded. End your turn; baton will bring the human in.")
	return 0
}

func cmdWaiting(args []string, io IO) int {
	p, err := parseArgs(args, []string{"until"}, nil)
	if err != nil {
		return fail(io, "%v", err)
	}
	what := strings.TrimSpace(strings.Join(p.pos, " "))
	d, derr := time.ParseDuration(p.vals["until"])
	if what == "" || derr != nil {
		return fail(io, "usage: baton waiting <what> --until <duration, e.g. 20m or 1h30m>")
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	if _, err := s.Update(func(st *state.State) error { return state.SetWaiting(st, what, d, io.Now()) }); err != nil {
		return fail(io, "%v", err)
	}
	s.Event("waiting", map[string]any{"what": what, "until": io.Now().Add(d).UTC().Format(time.RFC3339)})
	fmt.Fprintf(io.Out, "baton: recorded a wait for %q until %s. You may end your turn; if nothing wakes you by then, baton will.\n", what, io.Now().Add(d).Local().Format("15:04"))
	return 0
}

func cmdCheckpoint(args []string, io IO) int {
	p, err := parseArgs(args, []string{"notes"}, nil)
	if err != nil || len(p.pos) != 0 {
		return fail(io, "usage: baton checkpoint [--notes TEXT]%s", errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	st, err := s.Update(func(st *state.State) error { return state.SetCheckpoint(st) })
	if err != nil {
		return fail(io, "%v", err)
	}
	s.AppendHandoff(st.Current+" (checkpoint)", p.vals["notes"])
	s.Event("checkpoint", map[string]any{"phase": st.Current})
	fmt.Fprintf(io.Out, "baton: checkpoint recorded. End your turn now: baton compacts the context and you continue %s from your notes.\n", st.Current)
	return 0
}

func cmdPause(_ []string, io IO) int {
	return simpleTransition(io, "paused", state.Pause, "baton: paused. baton will not compact, nudge or escalate until /baton resume.")
}

func cmdResume(_ []string, io IO) int {
	return simpleTransition(io, "resumed", state.Resume, "baton: resumed. baton drives the plan again.")
}

func simpleTransition(io IO, kind string, fn func(*state.State) error, msg string) int {
	s, err := store(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	if _, err := s.Update(fn); err != nil {
		return fail(io, "%v", err)
	}
	s.Event(kind, nil)
	fmt.Fprintln(io.Out, msg)
	return 0
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
