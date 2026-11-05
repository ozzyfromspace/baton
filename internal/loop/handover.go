package loop

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// In a run with phase agents, a phase whose tier the lead does not run on goes to a phase agent on that tier
// (state/handover.go), rather than waiting for a restart of the lead that the agents at work keep from
// coming, since a restart ends every agent of the lead's claude (leadtier.go):
//
//   - the phase in hand, raised mid-phase while agents are at work, is set aside for one at the lead's next
//     idle moment, its work committed on the run's branch (handOver, from retierIdle), and the lead takes up
//     its next phase after a compaction, as at a switch. With work uncommitted, or no agent at work, the
//     restart wait goes on as before;
//   - a boundary owed into a parked phase an agent takes up again is turned to the lead's next phase before
//     its compaction is typed (redirect, from switching);
//   - the agent that takes up a parked phase starts from the run's branch, which holds the lead's commits,
//     and its brief opens with where the lead left it (handNote, handed: decide.HandedBrief).

// handWait is the lead's part in setting its phase aside for a phase agent (handOver): when it is next
// looked at (each look reads git), the phase and why it was last held, logged once, and the phase whose
// uncommitted work the human was last told holds it.
type handWait struct {
	next time.Time
	held string
	said string
}

// handRecheck is how soon a phase held from a phase agent by the lead's checkout is looked at again.
const handRecheck = 30 * time.Second

// handUncommitted is why the checkout holds a phase from a phase agent when its work is not all committed.
const handUncommitted = "uncommitted work"

// handOver sets the phase the lead holds aside for a phase agent on its tier (state.HandOver), at the lead's
// idle moment, when the restart on that tier that retierIdle waits for cannot come because phase agents are
// at work: they would end with it. It reports whether it did; until it can, the restart wait goes on as
// before (tierGivenUp). The lead must be idle (retierIdle checked its turn and its draft; no dialog may
// hold it), the phase one an agent may take with nothing else of it under way (state.Handable), and the
// checkout must hold nothing the agent would not start from (handCheckout).
func (l *Loop) handOver(v host.View, st state.State) bool {
	agents := len(st.PhaseAgents())
	if !l.Workers || agents == 0 || l.dialogHolds(st) || v.Now.Before(l.hand.next) {
		return false
	}
	pl, ok := l.tierPlan(v.Now)
	if !ok {
		return false
	}
	if why := state.Handable(st, pl); why != "" {
		l.handHeld(st, pl, why, 0)
		return false
	}
	l.hand.next = v.Now.Add(handRecheck)
	if why, files := l.handCheckout(st); why != "" {
		l.handHeld(st, pl, why, files)
		return false
	}
	var h state.Handover
	var handed bool
	var epoch int
	if _, err := l.Store.Update(func(st *state.State) error {
		if st.Run.TurnOpen {
			return nil // the lead woke meanwhile
		}
		if h, handed = state.HandOver(st, pl, v.Now); handed {
			epoch = st.Run.Compaction.Epoch
			st.Run.Announce = append(st.Run.Announce, decide.PhaseHanded(h.Phase, h.Tier, h.Lead, agents, h.Next))
		}
		return nil
	}); err != nil || !handed {
		return false
	}
	raised := ""
	if r := state.LastRaised(st, pl, h.Phase); r != nil {
		raised = r.Why
	}
	fields := map[string]any{"phase": h.Phase, "tier": h.Tier.String(), "lead": h.Lead.String(), "why": decide.HandedWhy(h.Tier, h.Lead, raised),
		"agents": agents, "from": "in hand"}
	if h.Job != "" {
		fields["job"] = h.Job
	}
	if h.Next != "" {
		fields["next"] = h.Next
	}
	l.Store.Event("phase_handed", fields)
	if h.Next != "" {
		l.Store.Event("compact_queued", map[string]any{"reason": "boundary", "epoch": epoch, "next": h.Next, "by": "phase_handed"})
	}
	l.logf("loop: %s runs on %s, which the lead (%s) cannot restart on while %d phase agents are at work: set aside for an agent", h.Phase, h.Tier, h.Lead, agents)
	return true
}

// handCheckout says what in the lead's checkout keeps its phase from a phase agent, which starts from the
// run's branch alone, or "" when nothing does: a tracked file changed and not committed, work of the
// phase's own not committed (untracked files too, as a switch requires: state.Store.PhaseWork), or a
// checkout off the run's branch, once the run's branch has followed it where it can (FollowBranch). files
// counts what is uncommitted.
func (l *Loop) handCheckout(st state.State) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	root := l.Store.Root
	tracked, err := gitx.Uncommitted(ctx, root)
	if err != nil {
		return "git could not say what is uncommitted: " + err.Error(), 0
	}
	tracked = slices.DeleteFunc(tracked, func(p string) bool { return strings.HasPrefix(p, state.DirName+"/") })
	if len(tracked) > 0 {
		return handUncommitted, len(tracked)
	}
	switch w := l.Store.PhaseWork(st.Current); {
	case w.Err != nil:
		return "git could not say what is uncommitted: " + w.Err.Error(), 0
	case w.NoStart:
		return "baton cannot tell its work from what was there as it started", 0
	case w.TooMany:
		return handUncommitted, len(w.Paths)
	case len(w.Fresh) > 0:
		return handUncommitted, len(w.Fresh)
	}
	run := st.RunBranch
	if seen, followed := FollowBranch(ctx, l.Store); followed {
		run = seen.On
	}
	if on, err := gitx.IsAncestor(ctx, root, "HEAD", "refs/heads/"+run); err != nil || !on {
		return "the checkout is not on the run's branch", 0
	}
	return "", 0
}

// handHeld logs, once per phase and why, that the phase the lead holds is not set aside for a phase agent
// though the lead does not run on its tier (handover_held), and tells the human, once a phase, when its
// uncommitted work is why: they, or the lead's model, may commit it.
func (l *Loop) handHeld(st state.State, pl plan.Plan, why string, files int) {
	if key := st.Current + " " + why; l.hand.held != key {
		l.hand.held = key
		fields := map[string]any{"phase": st.Current, "why": why}
		if files > 0 {
			fields["files"] = files
		}
		l.Store.Event("handover_held", fields)
	}
	if why != handUncommitted || l.hand.said == st.Current {
		return
	}
	l.hand.said = st.Current
	line := decide.HandoverHeld(st.Current, state.TierFor(st, pl, st.Current, l.Tiers.Models), st.LeadRuns(), files)
	l.update(func(st *state.State) { st.Run.Announce = append(st.Run.Announce, line) })
}

// redirect turns a boundary the lead owes into a parked phase a phase agent takes up again
// (state.AgentTakes) to the lead's next phase, before its compaction is typed (state.Redirect): the run
// was carried over from a baton before v0.8.20, which chose it, or the lead's tier or the run's agents
// changed since. It returns the run's state as it left it, and whether it turned the boundary.
func (l *Loop) redirect(v host.View, st state.State) (state.State, bool) {
	ps := st.Phases[st.Current]
	if !l.Workers || !st.BoundaryOwed || ps == nil || ps.Status != state.PhaseParked || st.Run.Compaction.Status != state.CompactQueued {
		return st, false
	}
	pl, ok := l.tierPlan(v.Now)
	if !ok || !st.AgentTakes(pl, st.Current) {
		return st, false
	}
	tier, lead := state.TierFor(st, pl, st.Current, l.Tiers.Models), st.LeadRuns()
	var from, to string
	var turned bool
	nst, err := l.Store.Update(func(st *state.State) error {
		if from, to, turned = state.Redirect(st, pl); turned {
			st.Run.Announce = append(st.Run.Announce, decide.BoundaryRedirected(from, tier, to))
		}
		return nil
	})
	if err != nil || !turned {
		return st, false
	}
	l.Store.Event("boundary_redirected", map[string]any{"phase": from, "tier": tier.String(), "lead": lead.String(), "next": to})
	l.logf("loop: %s goes to a phase agent on %s, not back to the lead: its boundary takes up %q instead", from, tier, to)
	return nst, true
}

// What the brief of the agent given a phase the lead started quotes of its last gate's log.
const (
	handedTailLines = 40
	handedTailBytes = 4 << 10
	handedFailures  = 20
	handedClip      = 200
)

// maxHandedCommits bounds the lead's commits the brief of the agent given its phase lists.
const maxHandedCommits = 15

// handNote is where the lead left parked phase of run st (in s), which a phase agent takes up from base,
// the run's branch's tip (state.AgentTakes), as that agent's brief opens with it (decide.HandedBrief): read
// before the phase is assigned, which keeps its park as its Handed, with git outside the run's lock. why
// says why no agent may take it now ("" when one may): the lead's checkout is off the run's branch, so the
// branch may not hold the phase's work.
func (c *Coordinator) handNote(s *state.Store, st state.State, pl plan.Plan, phase, base string) (decide.Handover, string) {
	ps := st.Phases[phase]
	h := decide.Handover{Phase: phase, Tier: state.TierFor(st, pl, phase, c.Lead.Tiers.Models), Lead: st.LeadRuns(), Since: ps.StartHead}
	if r := state.LastRaised(st, pl, phase); r != nil {
		h.Raised = r.Why
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if on, err := gitx.IsAncestor(ctx, s.Checkout, "HEAD", base); err != nil || !on {
		return h, "the lead's checkout is not on the run's branch, which would hold " + phase + "'s work"
	}
	var until time.Time
	job := ""
	if pk := ps.Park; pk != nil {
		until, h.Notes, job = pk.Since, pk.Notes, pk.Job
	}
	h.Gate = handedGate(s, st, phase, job)
	if ps.StartHead != "" {
		h.Commits, h.More = leadCommits(ctx, s.Checkout, st, ps.StartHead, base, until)
	}
	return h, ""
}

// leadCommits lists the lead's commits for a phase from from (where it last started) to to (the run's
// branch's tip), committed by until (when it was set aside; zero: whenever): those no landing brought onto
// the run's branch, merges and the plan's record aside, at most maxHandedCommits as "<short> <subject>",
// oldest first, and how many more.
func leadCommits(ctx context.Context, root string, st state.State, from, to string, until time.Time) ([]string, int) {
	hashes, err := gitx.CommitsUntil(ctx, root, from, to, until)
	if err != nil {
		return nil, 0
	}
	others := append(st.LandedCommits(), st.RecordCommits()...)
	hashes = slices.DeleteFunc(hashes, func(h string) bool { return slices.Contains(others, h) })
	more := 0
	if len(hashes) > maxHandedCommits {
		hashes, more = hashes[:maxHandedCommits], len(hashes)-maxHandedCommits
	}
	commits, err := gitx.Described(ctx, root, hashes)
	if err != nil {
		return nil, 0
	}
	var out []string
	for _, cm := range commits {
		out = append(out, cm.Short()+" "+cm.Subject)
	}
	return out, more
}

// handedGate is the last gate of phase in run st (in s), as the brief of the agent given it quotes it: the
// verify job named job (its park's), once ended, else the gate the phase stood on, if it is red; nil for
// none.
func handedGate(s *state.Store, st state.State, phase, job string) *decide.HandedGate {
	j := st.Job(job)
	if j == nil || j.Kind == state.JobAwait || !j.Finished() {
		j = st.RedGate(phase)
	}
	if j == nil {
		return nil
	}
	path := j.Log
	if path == "" {
		path = s.JobLog(j.ID)
	}
	e, _ := lane.ReadExcerpt(path, handedTailLines, handedTailBytes, handedFailures, handedClip)
	return &decide.HandedGate{Job: *j, Path: path, Failures: e.Failures, More: e.More, Tail: e.Tail}
}

// handed records where the lead left phase h.Phase, which a phase agent takes up, in sub, the agent's
// sub-run: its first agent's brief opens with it (decide.HandedBrief). It logs phase_handed in s, the lead's
// run, for a phase the lead set aside for another reason than to give it an agent (handOver logged that one
// as it set it aside); pk is its park as the lead left it.
func (c *Coordinator) handed(s, sub *state.Store, h decide.Handover, pk *state.Park) {
	note := decide.HandedBrief(h)
	if _, err := sub.Update(func(st *state.State) error {
		if ps := st.Phases[h.Phase]; ps != nil {
			ps.HandedNote = note
		}
		return nil
	}); err != nil {
		c.Lead.logf("loop: %s: where the lead left it, for its agent's brief: %v", h.Phase, err)
	}
	if pk != nil && pk.Reason == state.ParkHanded {
		return
	}
	fields := map[string]any{"phase": h.Phase, "tier": h.Tier.String(), "lead": h.Lead.String(), "why": decide.HandedWhy(h.Tier, h.Lead, h.Raised), "from": "parked"}
	if pk != nil {
		fields["park"] = pk.Reason
		if pk.Job != "" {
			fields["job"] = pk.Job
		}
	}
	s.Event("phase_handed", fields)
}
