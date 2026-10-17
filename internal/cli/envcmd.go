package cli

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// `baton env "<what the environment needs>"` records a fact about the gates' environment that a phase
// found (a port to leave free, a service to start first) as the run's (State.Facts), with the phase that
// found it: every later brief, the lead's and every phase agent's, lists the run's facts, and each phase
// agent at work now is told of it once, by the mail path its results take. The facts are kept in the run's
// state, not a worker's sub-run, so an agent's fact goes to the run it works for.

func init() {
	register("env", "record what this run's gates need of their environment, for every later brief and the phase agents at work: env \"<what the environment needs>\"", cmdEnv)
}

func cmdEnv(args []string, io IO) int {
	p, err := parseArgs(args, nil, nil)
	text := decide.Sanitize(strings.Join(p.pos, " "))
	if err != nil || text == "" {
		return fail(io, "%s%s", decide.EnvUsage, errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	if n := utf8.RuneCountInString(text); n > state.MaxFactLen {
		return refused(io, s, "env", refusal{"too long", decide.FactTooLong(n)})
	}
	run, phase := s, s.Worker()
	if phase != "" {
		if run, err = s.ParentRun(); err != nil {
			return fail(io, "%v", err)
		}
	}

	var fact state.Fact
	var n int
	var fresh bool
	var tell []state.Worker // the phase agents at work, but the one that found it
	if _, err := run.Update(func(st *state.State) error {
		if phase == "" {
			phase = st.Current
		}
		if f, ok := state.KnownFact(*st, text); ok {
			fact = f
			return nil
		}
		if len(st.Facts) >= state.MaxFacts {
			return refusal{"too many", decide.FactsFull()}
		}
		fact, fresh = state.AddFact(st, text, phase, io.Now())
		n = len(st.Facts)
		tell = atWork(*st, s.Worker())
		return nil
	}); err != nil {
		return refused(io, s, "env", err)
	}
	if !fresh {
		fmt.Fprintln(io.Out, decide.FactKnown(fact))
		return 0
	}

	// Each sub-run's lock is taken on its own, after the run's: no code holds two at once.
	told := tellFact(run, tell, fact, io)
	run.Update(func(st *state.State) error {
		st.Run.Announce = append(st.Run.Announce, decide.FactRecorded(fact, told))
		return nil
	})
	fields := map[string]any{"text": fact.Text, "phase": fact.Phase, "n": n, "told": told}
	if s.Worker() != "" {
		fields["by"] = "agent"
	} else {
		fields["by"] = "lead"
	}
	run.Event("env_fact", fields)
	fmt.Fprintln(io.Out, decide.FactSaid(fact, n, told))
	return 0
}

// atWork lists the workers of run st whose agent may be at work on its phase, or take it up again with
// its conversation, but the one on phase except: starting, working, suspended or held, of the plan attached
// now, and not retiring. One landing has finished its phase. A fresh agent's brief lists the facts, so it
// drops the message (hooks' agentStart).
func atWork(st state.State, except string) []state.Worker {
	var out []state.Worker
	for _, w := range st.LiveWorkers() {
		if w.Phase == except || w.Status == state.WorkerLanding || w.Generation < st.Generation || !w.Retiring.IsZero() {
			continue
		}
		out = append(out, w)
	}
	return out
}

// tellFact leaves fact f for the agent of each of ws, in its sub-run, while its phase is in hand there and
// not done, and returns the phases told.
func tellFact(run *state.Store, ws []state.Worker, f state.Fact, io IO) []string {
	var told []string
	p := run.ProjectOf()
	for _, w := range ws {
		sub, err := p.ExistingRun(w.Ref, "")
		if err != nil {
			continue
		}
		var ok bool
		sub.Update(func(st *state.State) error {
			ps := st.Phases[w.Phase]
			if ok = st.Current == w.Phase && (ps == nil || ps.Status != state.PhaseDone); ok {
				state.Tell(st, state.Message{Kind: state.MessageFact, Text: decide.FactTold(f), Phase: w.Phase}, io.Now())
			}
			return nil
		})
		if ok {
			told = append(told, w.Phase)
		}
	}
	return told
}
