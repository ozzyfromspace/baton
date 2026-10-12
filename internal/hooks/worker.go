package hooks

import (
	"os"
	"time"

	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A worker is a session of its own on one phase of a run, in a git worktree of its own (state.NewWorker
// links it to its sub-run before it starts). Its hooks are the lead's, with these differences: it starts
// its phase from a brief of its own, it never touches the project's checkout and never plans, its work
// lands on the run's branch once it is done, and its questions are queued for the lead session to put
// to the human (state.Store.Relay) instead of asked in a session nobody watches.

// workerStart starts a worker's phase as its session starts: the phase becomes active from the state of
// the worktree now, and the worker gets its brief, which the prompt it was launched with tells it to
// follow. A session started anew on a phase already under way gets the brief again.
func (h *handlers) workerStart(c Context, s *state.Store, pl plan.Plan, ann string) (Result, error) {
	origin := state.OriginOf(s.Root) // read before taking the lock: git can be slow
	started := false
	_, st, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if ps := st.Phases[st.Current]; st.Mode == state.ModeRunning && (ps == nil || ps.Status == state.PhasePending) {
			state.Start(st, c.Now, origin)
			started = true
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	if started {
		s.Event("phase_started", map[string]any{"phase": st.Current})
	}
	text := h.workerBrief(c, s, pl, st, false)
	return Result{Output: map[string]any{
		"systemMessage": lines(ann, "baton: worker on "+phaseTitle(pl, st.Current)+" — it starts now, with its brief"),
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	}}, nil
}

// workerBrief is the brief a worker starts its phase from, in its sub-run s, and logs it.
func (h *handlers) workerBrief(c Context, s *state.Store, pl plan.Plan, st state.State, again bool) string {
	doc, _ := os.ReadFile(pl.File)
	handoff, _ := os.ReadFile(s.HandoffPath())
	in := brief.Input{Kind: brief.Worker, Plan: pl, Doc: doc, State: st, Handoff: string(handoff), Words: words(c, s),
		Tree: s.Root, Branch: gitx.Branch(s.Root), Needs: builtOn(s, st.Current), Decisions: s.Decisions(st), Again: again}
	text := brief.Write(in)
	s.Event("brief", map[string]any{"type": brief.Worker, "phase": st.Current, "chars": len(text)})
	return text
}

// builtOn is every phase that phase waits for in the run's plan, directly or not: a worker's brief quotes
// their notes. A worker's own plan holds its phase alone (plan.Only), with nothing to wait for.
func builtOn(s *state.Store, phase string) []string {
	up, err := s.ParentRun()
	if err != nil {
		return nil
	}
	pl, err := up.LoadPlan()
	if err != nil {
		return nil
	}
	return pl.Ancestors(phase)
}

// workerPlan is what becomes of a plan approved in a worker: nothing. Only the lead session attaches
// plans, and the worker's phase is planned already.
func workerPlan(phase, file string) valveAction {
	return valveAction{event: "plan_not_attached", fields: map[string]any{"plan": file, "why": "worker"},
		say:  "baton: plan approved in the worker on " + phase + " — not attached: only the lead session attaches plans",
		tell: decide.WorkerPlanRefused(phase)}
}

// relayed is escalate in a worker. Nobody watches its session, so no question is put there: the human is
// pushed (the notice), the lead session asks them (r, which the stop queues), and the stop goes through
// while the phase waits for their answer. A repeated escalation for the same reason pushes nothing new.
func relayed(st *state.State, d *decision, kind, reason string, r state.Relay, now time.Time) {
	if e := st.Run.Escalation; e == nil || e.Reason != reason {
		st.Run.Escalation = &state.Escalation{Kind: kind, Reason: reason, Since: now, Asked: true}
		notice(st, kind, reason, now)
		d.emit("escalated", map[string]any{"type": kind, "reason": reason, "relayed": true})
	}
	d.relay = &r
	d.say("baton: waiting on the human — " + reason + "; the lead session asks them")
}
