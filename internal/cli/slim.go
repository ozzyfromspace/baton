package cli

import (
	"fmt"
	"slices"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Slim runs in a QA phase (v0.8.15, decide/slim.go): once a QA phase's full run found every red, `baton verify`
// of its red gate's command is queued as that gate's --failed rerun (askRerun), saying so first, until all of
// it passes; the stages that passed are carried, as with --failed. `--whole --why "…"` runs it whole, the
// human told (gate_whole); --whole with no --why is refused, saying why. A red gate that recorded no stages
// keeps today's path: the repeat question offers the narrowed rerun. One more full run follows at the
// phase's done only if the fixes could have broken what passed (confirm.go).

// slimRun is a QA phase's verify of its red gate's command, as askSlim found it before the run's lock.
type slimRun struct {
	gate  state.Job // the red gate
	rerun *rerunAsk // what it reruns; nil when it runs whole (--whole)
	why   string    // --whole's reason
}

// askSlim is what verify of command does for phase when phase is a QA phase whose red gate runs command and
// recorded its stages: rerun what failed in it (a --failed rerun), or, with whole, run it whole for why.
// nil for any other verify. --whole with no why is refused.
func askSlim(s *state.Store, st state.State, pl plan.Plan, phase, command string, whole bool, why string) (*slimRun, error) {
	if !slices.ContainsFunc(pl.Phases, func(ph plan.Phase) bool { return ph.ID == phase && ph.QA() }) {
		return nil, nil
	}
	g := st.RedGate(phase)
	if g == nil || g.Command != command {
		return nil, nil
	}
	a, err := askRerun(s, st, phase, "", nil)
	if err != nil || a.narrow != nil {
		return nil, nil // no stages on record (a narrowed rerun needs a command of its own), or nothing to rerun
	}
	sl := &slimRun{gate: *g}
	switch {
	case whole && why == "":
		return nil, refusal{"whole with no why", decide.SlimWholeRefused(phase, *g)}
	case whole:
		sl.why = why
	default:
		sl.rerun = &a
	}
	return sl, nil
}

// say tells the model, before anything else is asked of it, that its verify reruns what failed.
func (sl *slimRun) say(io IO, phase string) {
	if sl != nil && sl.rerun != nil {
		fmt.Fprintln(io.Out, "baton: "+decide.SlimRun(phase, sl.gate))
	}
}

// said is what the human is told of a whole run queued over it (--whole), in place of what baton's
// questions about the gate would tell them (said); said itself otherwise.
func (sl *slimRun) said(phase string, said gateSaid) gateSaid {
	if sl == nil || sl.rerun != nil {
		return said
	}
	return gateSaid{line: decide.SlimWholeSaid(phase, sl.gate, sl.why)} // logged as gate_whole (logged)
}

// event marks job_queued's ev as a slim run's.
func (sl *slimRun) event(ev map[string]any) {
	if sl != nil && sl.rerun != nil {
		ev["slim"] = true
	}
}

// logged logs a whole run queued as job over the slim run (gate_whole).
func (sl *slimRun) logged(s *state.Store, job state.Job) {
	if sl == nil || sl.rerun != nil {
		return
	}
	s.Event("gate_whole", map[string]any{"job": job.ID, "phase": job.Phase, "command": job.Command, "commit": job.Commit,
		"red_gate": sl.gate.ID, "why": sl.why})
}
