package state

import (
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The session waits on the phase in hand's job only while nothing else can go on. Once the job has run
// long and another phase can start (or a parked one can resume), the host switches the session: the phase
// is set aside, parked with the notes its job was queued with, and the other is taken up after a
// compaction, as at a phase boundary. The phase comes back once its job ends (Next). A checkpoint is a
// switch point too: a phase whose job finished meanwhile is taken up again there.

// Switched is a switch the host made: the phase it set aside while its job runs (From, and the park it had
// before, Prev) and the phase it took up instead (To). Until its compaction is typed, it can be called
// off (Unswitch).
type Switched struct {
	From string `json:"from"`
	To   string `json:"to"`
	Job  string `json:"job"`
	Prev *Park  `json:"prev,omitempty"`
}

// CurrentJob is the job the phase in hand waits on: its latest job, while it is queued or running. nil
// when there is none.
func (st State) CurrentJob() *Job {
	if st.Current == "" {
		return nil
	}
	if j := st.Latest(st.Current); j != nil && !j.Finished() {
		return j
	}
	return nil
}

// SwitchAfter is how long the phase in hand waits on job j before baton switches the session to another
// phase: after (switch_after), or no time at all when the same command's earlier runs took at least that
// long (their median), since the wait is known to be long.
func SwitchAfter(st State, j Job, after time.Duration) time.Duration {
	var took []time.Duration
	for _, o := range st.Jobs {
		if o.ID != j.ID && o.Command == j.Command && (o.Status == JobPassed || o.Red()) && !o.Started.IsZero() && o.Ended.After(o.Started) {
			took = append(took, o.Ended.Sub(o.Started))
		}
	}
	if len(took) == 0 {
		return after
	}
	slices.Sort(took)
	median := took[len(took)/2]
	if len(took)%2 == 0 {
		median = (took[len(took)/2-1] + median) / 2
	}
	if median >= after {
		return 0
	}
	return after
}

// Switchable reports whether the session can be switched off the phase in hand: the phase is under way
// and waits on its job, and nothing else is owed or asked of anyone. No compaction is due or under way
// (and the session went on after baton's last one), no result is due, the human is not being asked
// anything, and none of the session's own background work runs: its results belong to this phase. Nor
// does a gate run in the session's working tree (HereGate): the next phase would change what it reads.
// Whether another phase can go on instead is Next's to say.
func Switchable(st State) bool {
	ps, comp := st.Phases[st.Current], st.Run.Compaction
	switch {
	case st.Mode != ModeRunning || ps == nil || ps.Status != PhaseActive || st.CurrentJob() == nil:
		return false
	case st.BoundaryOwed || st.CheckpointOwed || st.CheckpointAsked || st.CheckpointDue || len(st.Due()) > 0:
		return false
	case comp.InFlight() || comp.Status == CompactFailed || comp.Status == CompactDone && comp.ByBaton && !st.Run.TurnStarted.After(comp.Finished):
		return false
	case st.Blocked != nil || st.Run.Escalation != nil || st.ReviewDue != "" || st.Replan != nil || st.PendingProposal() != nil:
		return false
	case st.HereGate() != nil:
		return false
	}
	return len(st.Run.BusyBackground()) == 0
}

// Switch sets the phase in hand aside while its job runs, and takes up the next phase (Next) after a
// compaction it queues, as at a phase boundary. The phase is parked with the notes its job was queued
// with, and resumes from them once the job has ended. It reports false, changing nothing, when the session
// cannot be switched (Switchable) or no other phase can go on.
func Switch(st *State, p plan.Plan, now time.Time) (Switched, bool) {
	if !Switchable(*st) {
		return Switched{}, false
	}
	to := Next(*st, p)
	if to == "" {
		return Switched{}, false
	}
	j, ps := *st.CurrentJob(), st.Phases[st.Current]
	sw := Switched{From: st.Current, To: to, Job: j.ID, Prev: ps.Park}
	ps.Status, ps.Park = PhaseParked, &Park{Reason: ParkJob, Since: now, Notes: j.Notes, Job: j.ID}
	st.Current = to
	st.OweBoundary(now)
	st.Run.Progress()
	st.Run.Compaction.Queue("switch", now)
	st.Run.Compaction.Switch = &sw
	return sw, true
}

// Unswitch calls off a switch whose job finished before its compaction was typed: the phase set aside is
// in hand again, as it was, and is given the job's result there. It reports whether it did. A switch that
// went further (its compaction typed, or the run moved on meanwhile) stands, and the phase comes back at
// the next switch point.
func Unswitch(st *State) (Switched, bool) {
	comp := &st.Run.Compaction
	sw := comp.Switch
	if sw == nil || comp.Status != CompactQueued || comp.Tries > 0 || comp.Rounds > 0 {
		return Switched{}, false
	}
	if j := st.Job(sw.Job); j != nil && !j.Finished() {
		return Switched{}, false
	}
	ps := st.Phases[sw.From]
	if st.Current != sw.To || !st.BoundaryOwed || ps == nil || ps.Status != PhaseParked || ps.Park == nil || ps.Park.Job != sw.Job {
		return Switched{}, false
	}
	ps.Status, ps.Park = PhaseActive, sw.Prev
	st.Current, st.BoundaryOwed = sw.From, false
	comp.Status, comp.Switch = CompactCancelled, nil
	return *sw, true
}

// Returning is the parked phase with something new to resume from, parked longest: its job has ended,
// or its verification did. A checkpoint goes back to it. "" for none.
func Returning(st State, p plan.Plan) string {
	return oldestParked(st, p, func(pk *Park) bool {
		return pk != nil && (pk.Job != "" || pk.Reason != ParkCheckpoint) && st.resolved(pk)
	})
}

// oldestParked is the phase parked longest whose park ok accepts, or "" for none.
func oldestParked(st State, p plan.Plan, ok func(*Park) bool) string {
	id, since := "", time.Time{}
	for _, ph := range p.Phases {
		ps := st.Phases[ph.ID]
		if ps == nil || ps.Status != PhaseParked || !ok(ps.Park) {
			continue
		}
		var at time.Time
		if ps.Park != nil {
			at = ps.Park.Since
		}
		if id == "" || at.Before(since) {
			id, since = ph.ID, at
		}
	}
	return id
}

// SwitchAtCheckpoint makes a checkpoint of the phase in hand a switch point: when a parked phase has
// something new to resume from (Returning), the phase in hand is set aside with the checkpoint's notes,
// and the other is taken up again after the compaction this queues. The phase set aside waits on its own
// job if one runs, and comes back at a later switch point. It returns the phase taken up, or "" when the
// checkpoint stays a checkpoint: no notes to resume from, no phase to go back to, or a gate that runs in
// the session's working tree (HereGate).
func SwitchAtCheckpoint(st *State, p plan.Plan, notes string, now time.Time) string {
	ps := st.Phases[st.Current]
	if strings.TrimSpace(notes) == "" || st.Mode != ModeRunning || ps == nil || ps.Status != PhaseActive || st.BoundaryOwed || st.Run.Compaction.InFlight() || st.HereGate() != nil {
		return ""
	}
	to := Returning(*st, p)
	if to == "" {
		return ""
	}
	pk := Park{Reason: ParkCheckpoint, Since: now, Notes: strings.TrimSpace(notes)}
	if j := st.CurrentJob(); j != nil {
		pk.Job = j.ID
	}
	ps.Status, ps.Park = PhaseParked, &pk
	st.Current = to
	st.OweBoundary(now)
	st.CheckpointOwed, st.CheckpointAsked, st.CheckpointDue = false, false, false
	st.Run.Progress()
	st.Run.Compaction.Queue("switch", now)
	return to
}
