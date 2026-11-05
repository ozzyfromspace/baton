package state

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The lead changes its tier only by a restart in place, which ends every phase agent of its claude
// (loop/leadtier.go). In a run with phase agents, a phase not started whose tier the lead does not run on is
// an agent's (forAgent); since v0.8.20, so is a phase the lead started whose tier it no longer runs on,
// raised as it went badly, say, rather than one that waits for a restart the agents at work keep from ever
// coming. (In a 74-phase campaign, a raised phase waited ten minutes for a restart that never came, then
// went on on the tier it had failed on.)
//
//   - A parked phase a phase agent may take (AgentTakes) is passed over by the lead as it takes up its next
//     phase (Next), and taken up again by an agent (NextWorker, Assign) once none of its jobs runs, from the
//     work the lead committed on the run's branch. baton never sets a phase aside with its work
//     uncommitted: a switch and a checkpoint refuse to, and a phase whose done kept files of its own
//     uncommitted (KeptDirty) stays the lead's, as does one whose gate runs in the lead's checkout.
//   - The phase in hand, raised mid-phase while agents are at work, is set aside for one at the lead's next
//     idle moment, its work committed (HandOver, from loop/handover.go), and the lead takes up its next
//     phase after a compaction, as at a switch.
//   - A boundary owed into a parked phase an agent takes is turned to the lead's next phase before its
//     compaction is typed (Redirect): a run carried over from an earlier baton may have chosen it.
//
// The lead's part stays on the phase (Assign): its raises (TierFor), its start, what it edited, and its
// park (Handed), which it goes back to if its agent fails on it (Unassign): the lead's again then
// (failedOn), and the lead waits for a restart on its tier as before. The lead's gates of the phase no
// longer stand for it once an agent has it (handedFrom): the agent's own, in its sub-run, do, and count its
// further raises (RedRuns since its last raise).

// Handed is the lead's part of a phase it started that baton gave a phase agent (Assign): when, and the park
// it was given from, which it is parked on again if the agent fails on it (Unassign).
type Handed struct {
	At   time.Time `json:"at"`
	Park *Park     `json:"park,omitempty"`
}

// agentMay reports phase ph of p, which the lead started, as one a phase agent may take from it, as forAgent
// says of a phase not started: the run has agents, none of the plan attached now failed on it, the lead does
// not run on its tier (LeadFits), it waits for every phase before it only as a QA phase (an integration or
// a release stays the lead's: plan.Plan.AfterAll), and every phase it waits for is done. Never while the run
// winds down near the end of the weekly allowance (WoundDown): no new agent starts until the weekly reset,
// and work under way, the lead's started phases among it, goes on.
func (st State) agentMay(p plan.Plan, ph plan.Phase) bool {
	return st.HasAgents() && !st.WoundDown() && !st.failedOn(ph.ID) && !st.LeadFits(p, ph.ID) && (ph.QA() || !p.AfterAll(ph.ID)) && ready(st, p, ph.ID)
}

// AgentTakes reports parked phase id of p as one a phase agent takes up again, which the lead passes over
// (Next): one an agent may take (agentMay), with nothing of it in the lead's checkout alone: no gate of its
// running there (Job.Here), and no file of its own its last done left uncommitted (KeptDirty). NextWorker
// gives it an agent once none of its jobs runs.
func (st State) AgentTakes(p plan.Plan, id string) bool {
	ps, i := st.Phases[id], p.Index(id)
	if ps == nil || ps.Status != PhaseParked || i < 0 || ps.KeptDirty > 0 || st.hereOf(id) {
		return false
	}
	return st.agentMay(p, p.Phases[i])
}

// hereOf reports a gate of phase id not finished yet that runs in the lead's working tree (Job.Here): what
// it reads is there alone.
func (st State) hereOf(id string) bool {
	return slices.ContainsFunc(st.Jobs, func(j Job) bool { return j.Phase == id && j.Here && !j.Finished() })
}

// handTo gives parked phase id to the worker of the sub-run ref (Assign): it is working, with its history
// (its raises, which TierFor reads, its start, what the lead edited), and its park kept as Handed. The
// results of its jobs in this run go with its agent's brief (loop/handover.go), not to the lead, and those
// jobs stand for it no more (handedFrom).
func handTo(st *State, id, ref string, now time.Time) {
	ps := st.Phases[id]
	ps.Handed = &Handed{At: now, Park: ps.Park}
	ps.Status, ps.Worker, ps.Park, ps.On = PhaseWorking, ref, nil, nil
	for i := range st.Jobs {
		if j := &st.Jobs[i]; j.Phase == id && j.Finished() && j.Delivered.IsZero() {
			j.Delivered = now
		}
	}
}

// takenBack is the state of a phase taken back from its worker, which cannot go on (Unassign,
// ReleaseWorker), from ps, its state with the worker: pending again, with the raises baton made its tier;
// or, for one the lead started (Handed), parked again as it was handed, with its history, for the lead to
// take up again, since an agent failed on it (failedOn).
func takenBack(ps *PhaseState) *PhaseState {
	if ps.Handed == nil {
		return &PhaseState{Status: PhasePending, Raises: ps.Raises}
	}
	back := *ps
	pk := Park{Reason: ParkHanded, Since: ps.Handed.At}
	if ps.Handed.Park != nil {
		pk = *ps.Handed.Park
	}
	back.Status, back.Worker, back.Park, back.On, back.Handed = PhaseParked, "", &pk, nil, nil
	return &back
}

// handedFrom reports job j as one of the lead's on a phase baton has since given a phase agent (Handed):
// queued by then, it stands for the phase no more (Standing), so the lead's red gate neither holds the
// phase nor reopens it, nor reads as red once its agent's work landed. The agent's gates, in its sub-run,
// are the phase's.
func (st State) handedFrom(j Job) bool {
	ps := st.Phases[j.Phase]
	return ps != nil && ps.Handed != nil && !j.Queued.After(ps.Handed.At)
}

// Handover is a phase in hand the host set aside for a phase agent (HandOver): the phase, the tier its agent
// is to run on and the lead's, the last gate whose notes its park keeps ("" for none), and the phase the
// lead takes up after a compaction ("" for none).
type Handover struct {
	Phase      string
	Tier, Lead plan.Tier
	Job        string
	Next       string
}

// Handable says why the phase in hand of run st, of plan p, cannot be set aside for a phase agent now
// (HandOver), or "" when it can: a phase agent may take it (agentMay) and will once it is parked
// (AgentTakes), and nothing of it is under way or owed but what its agent would do (no job of it runs, no
// gate runs in the lead's checkout, no compaction or checkpoint is owed, no replan is open), nor is the
// human asked anything. That the lead could not restart on its tier, and that its work is committed (git),
// are the caller's to have found (loop/handover.go).
func Handable(st State, p plan.Plan) string {
	i, comp := p.Index(st.Current), st.Run.Compaction
	switch {
	case !st.InHand() || i < 0:
		return "no phase is in hand"
	case !st.HasAgents():
		return "the run has no phase agents"
	case st.WoundDown():
		return "the run winds down until the weekly reset, and starts no agent"
	case st.failedOn(st.Current):
		return "a phase agent failed on it"
	case !p.Phases[i].QA() && p.AfterAll(st.Current):
		return "it waits for every phase before it, so it is the lead's"
	case st.LeadFits(p, st.Current):
		return "the lead runs on its tier"
	case st.Phases[st.Current].KeptDirty > 0:
		return "its last done left files of its own uncommitted"
	case len(st.Running(st.Current)) > 0:
		return "its jobs run"
	case st.HereGate() != nil:
		return "a gate runs in the lead's checkout"
	case st.CheckpointOwed || st.CheckpointAsked || st.CheckpointDue:
		return "a checkpoint is owed"
	case comp.InFlight() || comp.Status == CompactFailed:
		return "a compaction is under way"
	case st.Replan != nil:
		return "a replan is open"
	case st.Blocked != nil || st.Run.Escalation != nil || st.ReviewDue != "" || st.PlanQuestion != nil || st.PendingProposal() != nil:
		return "the human is asked something"
	}
	return ""
}

// HandOver sets the phase in hand aside for a phase agent on its tier (Handable): it is parked (ParkHanded)
// with the notes of its last gate, for an agent to take up (AgentTakes), and the lead takes up its next
// phase (Next, which passes over it) after a compaction this queues, as a switch does, or holds none. It
// reports false, changing nothing, when it cannot.
func HandOver(st *State, p plan.Plan, now time.Time) (Handover, bool) {
	if Handable(*st, p) != "" {
		return Handover{}, false
	}
	id, ps := st.Current, st.Phases[st.Current]
	h := Handover{Phase: id, Tier: TierFor(*st, p, id, st.Run.Models), Lead: st.LeadRuns()}
	pk := Park{Reason: ParkHanded, Since: now}
	if j := st.LatestOf(id, JobVerify); j != nil {
		pk.Notes, pk.Job, h.Job = j.Notes, j.ID, j.ID
	}
	ps.Status, ps.Park, ps.On = PhaseParked, &pk, nil
	st.Current = ""
	h.Next = takeUp(st, p, now)
	st.Run.Progress()
	return h, true
}

// Redirect turns a boundary owed into a parked phase a phase agent takes up again (AgentTakes) to the
// lead's next phase (Next, which passes over it), before its compaction is typed: a run carried over from a
// baton before v0.8.20 chose it, or the lead's tier or the run's agents changed since. The compaction
// queued takes that phase up instead, or is called off when the lead has none. It returns the phase passed
// over and the one taken up ("" for none), and false, changing nothing, when there is none to pass over.
func Redirect(st *State, p plan.Plan) (from, to string, ok bool) {
	comp, ps := &st.Run.Compaction, st.Phases[st.Current]
	if st.Mode != ModeRunning || !st.BoundaryOwed || ps == nil || ps.Status != PhaseParked || !st.AgentTakes(p, st.Current) ||
		comp.Status != CompactQueued || !comp.StartsPhase() || comp.Switch != nil || comp.Tries > 0 || comp.Rounds > 0 {
		return "", "", false
	}
	from = st.Current
	if to = Next(*st, p); to != "" {
		st.Current = to // the boundary stays owed, from when it was
	} else {
		st.Current, st.BoundaryOwed, comp.Status = "", false, CompactCancelled
	}
	st.Run.Progress()
	return from, to, true
}
