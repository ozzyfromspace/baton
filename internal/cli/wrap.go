package cli

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The human wraps a long run up (v0.8.17, state/wrap.go): `baton wrap`, run by the lead's model when the human
// typed /baton wrap, records the wrap in the lead's run, then, one run's lock at a time, over the lead's run and
// each live phase agent's sub-run: stops every confirmation run under way (its phase done on the rerun that
// passed, a decision with its undo) and every whole repeat under way, drops every confirmation owed, leaves a
// phase's first run of its gate alone, and tells each agent and the lead to finish on the evidence they have.
// While it lasts, verify refuses a whole repeat (askWrap, with no --why: the human chose it, and it waits on
// nothing) and a QA phase's done owes no confirmation (confirm.go). `baton wrap --off` lifts it. Only the
// human starts it or lifts it: a phase agent is refused, and so is a turn the human did not start.

func init() {
	register("wrap", "wrap the run up, only when the human typed /baton wrap: every phase still runs and lands, but no gate runs whole a second time "+
		"(confirmation runs skipped, whole reruns refused, only what failed reruns): wrap [--off]", cmdWrap)
}

func cmdWrap(args []string, io IO) int {
	p, err := parseArgs(args, nil, []string{"off"})
	if err != nil || len(p.pos) > 0 {
		return fail(io, "usage: baton wrap [--off], only when the human typed /baton wrap%s", errSuffix(err))
	}
	off := p.bools["off"]
	if worker(io) {
		phase := "its phase"
		if s, err := store(io); err == nil && s.Worker() != "" {
			phase = s.Worker()
			s.Event("wrap_refused", map[string]any{"why": "phase agent", "off": off})
		}
		return fail(io, "%s", decide.WorkerRefused("wrap", phase))
	}
	if !hosted(io) {
		return fail(io, "%s", decide.WrapNotHosted)
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	if !st.Run.TurnOpen || st.Run.TurnBy != "human" {
		by := ""
		if st.Run.TurnOpen {
			by = st.Run.TurnBy
		}
		s.Event("wrap_refused", map[string]any{"why": "not the human's turn", "turn_by": by, "off": off})
		return fail(io, "%s", decide.WrapTurnRefused(by))
	}
	if off {
		return wrapOff(io, s)
	}
	if st.Mode != state.ModeRunning && st.Mode != state.ModePaused {
		s.Event("wrap_refused", map[string]any{"why": "no run", "mode": st.Mode})
		return fail(io, "%s", decide.WrapNoRun(st.Mode))
	}
	return wrapUp(io, s)
}

// wrapUp records the wrap in the lead's run s and sweeps it and its live agents' sub-runs, one lock at a time,
// then says what it did. A run wrapping up already is swept again, and nobody is told again: an agent started
// since heard of it in its brief.
func wrapUp(io IO, s *state.Store) int {
	now := io.Now()
	var w decide.Wrapped
	var lead swept
	st, err := s.Update(func(st *state.State) error {
		if st.Mode != state.ModeRunning && st.Mode != state.ModePaused {
			return refusal{"no run", decide.WrapNoRun(st.Mode)}
		}
		if w.Again = st.Wrap != nil; !w.Again {
			st.Wrap = &state.Wrap{At: now, By: state.WrapByHuman}
			state.Tell(st, state.Message{Kind: state.MessageWrap, Text: decide.WrapUp, About: "wrap"}, now)
			w.Lead = true
		}
		w.Since = st.Wrap.At
		lead = sweep(st, now)
		return nil
	})
	if err != nil {
		var r refusal
		if errors.As(err, &r) {
			s.Event("wrap_refused", map[string]any{"why": r.why})
		}
		return fail(io, "%v", err)
	}
	lead.logged(s)
	w = lead.into(w)
	var told []string // the phases whose agents are at work, told
	p := s.ProjectOf()
	for _, e := range st.LiveWorkers() {
		if e.Generation < st.Generation || !e.Retiring.IsZero() {
			continue // a replaced plan's, or one on its way out: nothing of it lands under this plan
		}
		sub, err := p.ExistingRun(e.Ref, s.Instance)
		if err != nil {
			continue
		}
		var got swept
		if _, err := sub.Update(func(x *state.State) error {
			got = sweep(x, now)
			if !w.Again {
				// Given while its phase is in hand there: now, or once its phase reopens. A fresh agent's brief says
				// it instead (hooks' agentStart drops it), and a resumed one keeps its conversation, so it hears it.
				state.Tell(x, state.Message{Kind: state.MessageWrap, Text: decide.WrapUp, Phase: e.Phase, About: "wrap"}, now)
			}
			return nil
		}); err != nil {
			continue
		}
		got.logged(sub)
		w = got.into(w)
		if !w.Again && e.Status == state.WorkerWorking {
			told = append(told, e.Phase)
		}
	}
	w.Told = told
	line := decide.WrapLine(w)
	s.Update(func(st *state.State) error {
		st.Run.Announce = append(st.Run.Announce, line)
		return nil
	})
	s.Event("wrap_started", wrapEvent(w))
	fmt.Fprintln(io.Out, decide.WrapSaid(w))
	return 0
}

// wrapEvent is wrap_started's fields for w: the jobs stopped, the phases settled on their reruns, the phases
// whose agents were told, and the first runs kept.
func wrapEvent(w decide.Wrapped) map[string]any {
	stopped, settled, kept := []string{}, []string{}, []string{}
	for _, c := range w.Settled {
		if c.Job != "" {
			stopped = append(stopped, c.Job)
		}
		settled = append(settled, c.Phase)
	}
	for _, x := range w.Stopped {
		stopped = append(stopped, x.Job.ID)
	}
	for _, j := range w.Kept {
		kept = append(kept, j.ID)
	}
	told := w.Told
	if told == nil {
		told = []string{}
	}
	ev := map[string]any{"stopped": stopped, "settled": settled, "told": told, "lead": w.Lead, "kept": kept}
	if w.Again {
		ev["again"], ev["since"] = true, w.Since.UTC().Format(time.RFC3339)
	}
	return ev
}

// wrapOff lifts the wrap of the lead's run s: gates run whole again when asked, and confirmations are owed
// again. The message about it that no agent was given yet is dropped, one run's lock at a time; what the
// wrap stopped stays stopped.
func wrapOff(io IO, s *state.Store) int {
	var was *state.Wrap
	st, err := s.Update(func(st *state.State) error {
		if was = st.Wrap; was == nil {
			return nil
		}
		st.Wrap = nil
		state.DropMail(st, state.MessageWrap)
		st.Run.Announce = append(st.Run.Announce, decide.WrapEnded)
		return nil
	})
	if err != nil {
		return fail(io, "%v", err)
	}
	if was == nil {
		fmt.Fprintln(io.Out, decide.WrapNotOn)
		return 0
	}
	p := s.ProjectOf()
	for _, e := range st.LiveWorkers() {
		if sub, err := p.ExistingRun(e.Ref, s.Instance); err == nil {
			sub.Update(func(x *state.State) error { state.DropMail(x, state.MessageWrap); return nil })
		}
	}
	s.Event("wrap_ended", map[string]any{"since": was.At.UTC().Format(time.RFC3339), "seconds": int(io.Now().Sub(was.At).Seconds())})
	fmt.Fprintln(io.Out, decide.WrapEnded+".")
	return 0
}

// swept is what wrapping up did to one run (sweep).
type swept struct {
	settled []decide.WrapSettle
	stopped []decide.WrapStop
	kept    []state.Job
	stops   []state.Job      // every job it stopped, as it was
	notes   []state.Decision // the decisions it recorded
}

// sweep wraps up one run, st, under its lock: it stops each confirmation run under way and settles its phase
// on the rerun that passed (a decision), stops each whole repeat under way (state.WholeRepeat), and drops each
// confirmation owed (a decision). A phase's first run of its gate, a rerun of what failed, a wait on something
// outside and baton's own re-verification of a landing run on.
func sweep(st *state.State, now time.Time) swept {
	var out swept
	note := func(c decide.WrapSettle) decide.WrapSettle {
		what, undo := decide.WrapDecision(c)
		d := state.WrapNote(st, c.Phase, what, undo, now)
		c.Decision, out.notes = d.ID, append(out.notes, d)
		return c
	}
	for _, j := range slices.Clone(st.Jobs) {
		if j.Finished() {
			continue
		}
		ps := st.Phases[j.Phase]
		switch {
		case ps != nil && ps.Confirm != nil && ps.Confirm.Job == j.ID:
			stopped, rerun, ok := state.StopConfirm(st, j.Phase, decide.WrapReason, now)
			if !ok {
				continue
			}
			out.stops = append(out.stops, stopped)
			out.settled = append(out.settled, note(decide.WrapSettle{Phase: j.Phase, Job: stopped.ID, Rerun: rerun, Command: stopped.Command}))
		case j.Requeue || j.Kind == state.JobAwait:
		case st.WholeRepeat(j) != nil:
			whole := st.WholeRepeat(j).ID
			if stopped, ok := state.StopJob(st, j.ID, decide.WrapReason, now); ok {
				out.stops = append(out.stops, stopped)
				out.stopped = append(out.stopped, decide.WrapStop{Job: stopped, Whole: whole})
			}
		case j.Reruns == "" && len(j.Narrows) == 0:
			out.kept = append(out.kept, j)
		}
	}
	for _, phase := range slices.Sorted(maps.Keys(st.Phases)) {
		if c, ok := state.DropConfirm(st, phase); ok {
			out.settled = append(out.settled, note(decide.WrapSettle{Phase: phase, Rerun: c.After, Command: c.Command}))
		}
	}
	return out
}

// logged logs, in run s, the jobs the sweep stopped there and the decisions it recorded.
func (sw swept) logged(s *state.Store) {
	for _, j := range sw.stops {
		s.Event("job_stopped", map[string]any{"job": j.ID, "phase": j.Phase, "type": j.Kind, "command": j.Command, "was": j.Status,
			"why": "wrapping up", "by": "wrap"})
	}
	for _, d := range sw.notes {
		s.Event("note", map[string]any{"id": d.ID, "phase": d.Phase, "what": d.What, "undo": d.Undo, "by": "wrap"})
	}
}

// into adds what the sweep did to w.
func (sw swept) into(w decide.Wrapped) decide.Wrapped {
	w.Settled = append(w.Settled, sw.settled...)
	w.Stopped = append(w.Stopped, sw.stopped...)
	w.Kept = append(w.Kept, sw.kept...)
	return w
}

// askWrap turns down, while the lead's run is wrapping up (read before the lock: a phase agent reads the run
// it works for), a verify of command for phase that would run a gate whole a second time: a whole repeat
// (state.WholeRepeat), and a QA phase's --whole of its red gate. --why does not pass it. A gate's first run, a
// --failed rerun (narrowed or with whole stages), a QA phase's slim rerun and an await are left as they are.
func askWrap(s *state.Store, st state.State, pl plan.Plan, phase, command, kind string, failed, whole bool) error {
	if kind != state.JobVerify || failed || s.LeadWrap() == nil {
		return nil
	}
	if sl, err := askSlim(s, st, pl, phase, command, false, ""); err == nil && sl != nil && sl.rerun != nil {
		if whole {
			return refusal{"wrapping up", decide.WrapWholeRefused(phase, sl.gate)}
		}
		return nil // in a QA phase, its red gate's command reruns what failed (slim.go)
	}
	prev := st.WholeRepeat(state.Job{Phase: phase, Kind: kind, Command: command})
	if prev == nil {
		return nil
	}
	return refusal{"wrapping up", decide.WrapRefused(phase, wrapRepeat(s, st, phase, *prev))}
}

// wrapRepeat is what baton knows of a whole repeat of prev, the gate's last whole run in phase, for its
// refusal while the run wraps up: how long it ran, whether `baton verify --failed` reruns what failed in it,
// and the tests its log says failed.
func wrapRepeat(s *state.Store, st state.State, phase string, prev state.Job) decide.Repeat {
	r := decide.Repeat{Prev: prev, Took: ranFor(prev)}
	if rr, why := st.RerunOf(phase, s.StagesOf); why == "" && rr.Of.Command == prev.Command && prev.Status != state.JobPassed {
		r.Failed = true
	}
	r.Failures, r.MoreFailures = failuresOf(s, prev)
	return r
}

// wrapSkipped tells of a QA phase's done over reruns that owes no confirmation while the run wraps up
// (confirmAsk.wrapping): logged as qa_confirm_skipped, with why.
func (ca confirmAsk) wrapSkipped(io IO, s *state.Store, phase string) {
	s.Event("qa_confirm_skipped", map[string]any{"phase": phase, "of": ca.of, "gate": ca.gate.ID, "why": "wrapping up"})
	fmt.Fprintln(io.Out, "baton: "+decide.ConfirmWrapSkipped(phase, ca.of))
}
