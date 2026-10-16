package state

import (
	"errors"
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// SchemaVersion is the state.json format version.
const SchemaVersion = 1

// Modes of a project's run.
const (
	ModeIdle     = "idle"     // no plan attached
	ModeRunning  = "running"  // baton drives the plan
	ModePaused   = "paused"   // the human took the wheel: baton observes but never acts
	ModeComplete = "complete" // every phase is done, and no job is left to run
)

// Phase statuses. A phase runs pending → active → done. Two more hold a phase that is not done while the
// run works on others: verifying (done was claimed while its verification job runs) and parked (it was
// set aside unfinished while its job runs, or its verification failed after done was claimed). A phase a
// worker session took up runs pending → working → done instead: everything in between happens in the
// worker's own sub-run, and the phase is done once its work has landed (fleet.go).
const (
	PhasePending   = "pending"
	PhaseActive    = "active"
	PhaseVerifying = "verifying"
	PhaseParked    = "parked"
	PhaseWorking   = "working"
	PhaseDone      = "done"
)

// PhaseState is the progress of one phase.
type PhaseState struct {
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at,omitzero"`
	DoneAt    time.Time `json:"done_at,omitzero"`
	// StartHead is the git HEAD when the phase started, used to warn about a phase with no commit.
	StartHead string `json:"start_head,omitempty"`
	// StartDirty is the uncommitted work already there when the phase started, so that blocked and done
	// refuse only the work this phase left uncommitted. nil when it is not known: a project without git,
	// or a phase that started before baton recorded it.
	StartDirty *Dirt `json:"start_dirty,omitempty"`
	// Edited lists the files this session's edit tools wrote during the phase (paths as git reports
	// them). When another session works in the same checkout, they are the only uncommitted work the
	// phase can be held to: the rest may be the other session's.
	Edited []string `json:"edited,omitempty"`
	// Park is the phase's latest park: why it was set aside and where it stood. The phase is parked
	// while its status says so; once it resumes, Park stays as what it resumed from (Resumed is set), so
	// a later brief of the phase still has its notes.
	Park *Park `json:"park,omitempty"`
	// Worker is the sub-run of the worker the phase was given to (a ref, see Project), from the moment it
	// is assigned; it stays once the phase has landed.
	Worker string `json:"worker,omitempty"`
	// On is the jobs the phase waits on while it is verifying or parked (WaitsOn): those still running
	// when its done was claimed, or it was set aside, a verify and an await at most. A job that had ended
	// by then was judged then, so it is not waited on again. Cleared once the phase is done or resumes.
	On []string `json:"on,omitempty"`
}

// Park is why a phase was set aside unfinished, and what it resumes from.
type Park struct {
	Reason  string    `json:"reason"` // ParkJob, ParkRed, ParkUnverified or ParkCheckpoint
	Since   time.Time `json:"since"`
	Notes   string    `json:"notes,omitempty"` // where the phase stood, in the model's words
	Job     string    `json:"job,omitempty"`   // the job it waits for
	Resumed time.Time `json:"resumed,omitzero"`
}

// Why a phase is parked.
const (
	ParkJob        = "job"        // set aside while its job runs, so the session could work on another phase
	ParkRed        = "red"        // its verification failed after done was claimed
	ParkUnverified = "unverified" // its verification ended without a verdict after done was claimed
	ParkCheckpoint = "checkpoint" // set aside at a checkpoint, so a phase whose job had ended could go on
)

// MaxEdited bounds the files a phase records as edited; past it, it stops recording.
const MaxEdited = 2000

// Touched records that this session's edit tools wrote path during the current phase.
func (st *State) Touched(path string) {
	ps := st.Phases[st.Current]
	if ps == nil || len(ps.Edited) >= MaxEdited {
		return
	}
	for _, p := range ps.Edited {
		if p == path {
			return
		}
	}
	ps.Edited = append(ps.Edited, path)
}

// Block is a reason the model cannot continue without a human, and what it tried first.
type Block struct {
	Reason string    `json:"reason"`
	Tried  string    `json:"tried,omitempty"`
	Since  time.Time `json:"since"`
	// Lapsed is when the lead's loop found that nobody had answered the block for its Timing.BlockLook
	// while its question was on screen: that question is then answered Look again (by baton's mod,
	// hooks.Bound, or where baton types, by the host), which lifts it (LiftBlock).
	Lapsed time.Time `json:"lapsed,omitzero"`
}

// State is the content of .baton/state.json.
type State struct {
	Version int                    `json:"version"`
	Owner   *Owner                 `json:"owner,omitempty"`
	Mode    string                 `json:"mode"`
	Current string                 `json:"current,omitempty"`
	Phases  map[string]*PhaseState `json:"phases"`
	Blocked *Block                 `json:"blocked,omitempty"`
	// Jobs are the commands baton runs for the run's phases while the session works on, oldest first.
	Jobs []Job `json:"jobs,omitempty"`
	// JobSeq numbers jobs. It outlives the jobs, and the plan they ran for, so a runner still winding
	// down can never take a later job for its own.
	JobSeq int `json:"job_seq,omitempty"`
	// BoundaryOwed: a phase finished and a compaction is owed before the next phase starts.
	BoundaryOwed bool `json:"boundary_owed,omitempty"`
	// CheckpointOwed: the model asked for a mid-phase compaction at a safe point.
	CheckpointOwed bool `json:"checkpoint_owed,omitempty"`
	// CheckpointAsked: the human chose "Checkpoint now"; the model may finish its step first, and the
	// next stop becomes a checkpoint whether or not the model ran `baton checkpoint`.
	CheckpointAsked bool `json:"checkpoint_asked,omitempty"`
	// CheckpointDue: the human resumed the run after the conversation grew while it was paused, so the
	// phase goes on from a compacted context. Before anything else, the model records where the phase
	// stands (`baton checkpoint`), and baton compacts.
	CheckpointDue bool `json:"checkpoint_due,omitempty"`
	// OwedAt is when BoundaryOwed or CheckpointOwed last became true (OweBoundary, OweCheckpoint): a
	// compaction before it covers nothing the run owes now.
	OwedAt time.Time `json:"owed_at,omitzero"`
	// PauseContext is the context's size in tokens when the run was paused, or, once the lead compacted
	// while it was paused (PauseCompacted), the smallest reading since (SawContext); 0: not known.
	PauseContext   int       `json:"pause_context,omitempty"`
	PauseCompacted time.Time `json:"pause_compacted,omitzero"`
	// Covered is when a compaction proven while the run was paused covered the one it owed (Compacted):
	// /baton run counts it rather than compacting again (Keep), unless the conversation grew by more than
	// ResumeGrowth since.
	Covered time.Time `json:"covered,omitzero"`
	// Decisions are the notes and proposals of this run, oldest first.
	Decisions []Decision `json:"decisions,omitempty"`
	// ReviewDue is the phase whose decisions without the human reached max_auto_decisions: baton stops
	// for the human to review them before anything else, and takes no further notes or proposals.
	// ReviewAt is when it became due.
	ReviewDue string    `json:"review_due,omitempty"`
	ReviewAt  time.Time `json:"review_at,omitzero"`
	// PlanQuestion is a plan the human approved while this run had one, until they say what to do with it.
	PlanQuestion *PlanQuestion `json:"plan_question,omitempty"`
	// PlanDrift is the hash of a plan document baton found changed and has already reported, so a change
	// is reported once.
	PlanDrift string `json:"plan_drift,omitempty"`
	// PlanLooked is set once baton has looked at moving the run's plan file out of Claude Code's way
	// (Aside): as it attached the plan, or, for a plan attached before baton did so, at the lead's next
	// phase boundary. Whether it moved is the plan's Origin.
	PlanLooked bool `json:"plan_looked,omitempty"`
	// Replan is the replan of the phases not started that is open (replan.go), and Replans the replans
	// baton followed since the human approved the plan, oldest first.
	Replan  *Replan     `json:"replanning,omitempty"`
	Replans []Replanned `json:"replans,omitempty"`
	// RulesVersion is the planning rules (plan.RulesVersion) the run's plan was made or last replanned
	// under: 0 for a plan attached before baton numbered them. ReplanCued lists the cues of the automatic
	// replans baton has looked at, each once ("qa:P9", "marked:P3"; replan.go).
	RulesVersion int      `json:"rules_version,omitempty"`
	ReplanCued   []string `json:"replan_cued,omitempty"`
	// RunBranch is the branch checked out when the plan was attached: workers branch off it, and their
	// work lands on it. Workers, Relay, Landed, Usage and Hold are the run's worker sessions and what they
	// need of the lead (fleet.go). Conflicts counts the workers' landings in a row that conflicted with the
	// run's branch since one last landed: while they keep conflicting, no new worker starts.
	RunBranch string    `json:"run_branch,omitempty"`
	Workers   []Worker  `json:"workers,omitempty"`
	Relay     []Relay   `json:"relay,omitempty"`
	Landed    []Landing `json:"landed,omitempty"`
	Usage     *Usage    `json:"usage,omitempty"`
	Hold      *Hold     `json:"hold,omitempty"`
	Conflicts int       `json:"conflicts,omitempty"`
	// Generation counts the plans attached to the run (attachKeeping). A worker belongs to the plan it was
	// assigned under (Worker.Generation): one of an earlier plan is retired, and never counts as the
	// worker on a phase of the plan attached now, which may use the same ids.
	Generation int `json:"generation,omitempty"`
	// IDPrefix numbers a worker sub-run's decisions and jobs under its phase (P3.d1, P3.j2), so that no id
	// is used twice across the run and its workers.
	IDPrefix string `json:"id_prefix,omitempty"`
	// Inbox holds what baton has for the session's model that no hook could tell it yet (Message).
	Inbox []Message `json:"inbox,omitempty"`
	// PlanText is what a worker's agent was given of the plan (a sub-run's only).
	PlanText *PlanText `json:"plan_text,omitempty"`
	// Run is what the hooks observe about the live session.
	Run Runtime `json:"run"`
}

// PlanQuestion is baton's question about a plan the human approved while the run had one. Same says it is
// in the run's own plan file, or in the one baton moved that from (plan.Plan.Origin): Claude Code writes
// every plan a session makes to the same file, so that is a revision of the plan.
type PlanQuestion struct {
	File     string    `json:"file"`
	Same     bool      `json:"same"`
	Question string    `json:"question"` // the question baton issued, exactly as issued
	Since    time.Time `json:"since"`
	// Held is when the human answered Wait for me: nothing changes until they write, or until the hold
	// lapses (loop's Timing.HoldLapse) and baton answers its default.
	Held time.Time `json:"held,omitzero"`
	// Origin is the file the plan was approved in, when baton moved it out of Claude Code's way as it was
	// approved (a revision of a plan moved before): a plan the answer runs keeps it as its Origin.
	Origin string `json:"origin,omitempty"`
}

// New is the state of a project with no plan attached.
func New() State {
	return State{Version: SchemaVersion, Mode: ModeIdle, Phases: map[string]*PhaseState{}}
}

// Attach starts a fresh run of p: the first phase becomes active.
func Attach(p plan.Plan, now time.Time, o Origin) State {
	return attachKeeping(New(), nil, p, now, o)
}

// Reattach is Attach for a project that may already have an owner: ownership survives a new plan, and so
// do the workers still on the roster, which the host retires as the replaced plan's.
func Reattach(prev State, p plan.Plan, now time.Time, o Origin) State {
	run := prev.Run
	run.Compaction, run.StopBlocks, run.Escalation = Compaction{Epoch: prev.Run.Compaction.Epoch}, 0, nil
	st := attachKeeping(prev, &run, p, now, o)
	st.JobSeq, st.IDPrefix = prev.JobSeq, prev.IDPrefix
	return st
}

// AttachAtBoundary is Reattach for a plan whose first phase starts after a compaction, as if a phase
// before it had just finished: the plan was approved at the end of a planning conversation, or attached
// in a conversation that had other things in it, and the first phase starts with a brief instead of all
// that.
func AttachAtBoundary(prev State, p plan.Plan, now time.Time) State {
	st := Reattach(prev, p, now, Origin{})
	st.Phases[st.Current] = &PhaseState{Status: PhasePending}
	st.OweBoundary(now)
	return st
}

// Revise moves the run onto a revision of its plan. Phases done, verifying, parked or working stay so
// (matched by id), and the jobs of phases the revision leaves out are cancelled; so are their workers, by
// the host, which finds them on the roster with their phase gone. The phase under way carries on if
// the revision keeps it and its dependencies are done; otherwise the run takes up the next phase after a
// compaction, as at a phase boundary. It returns the phase in hand, or "" when there is none: the
// revision has nothing left to do (ModeComplete), or nothing that can start until a job finishes.
func Revise(st *State, p plan.Plan, now time.Time) string {
	old, cur := st.Phases, st.Current
	st.Phases = map[string]*PhaseState{}
	for _, ph := range p.Phases {
		switch ps := old[ph.ID]; {
		case ps != nil && (ps.Status == PhaseDone || ps.Status == PhaseVerifying || ps.Status == PhaseParked || ps.Status == PhaseWorking):
			st.Phases[ph.ID] = ps
		default:
			st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
		}
	}
	CancelJobs(st, func(j Job) bool { return p.Index(j.Phase) < 0 }, "its phase is no longer in the plan", now)
	st.Mode, st.Blocked, st.CheckpointOwed, st.CheckpointAsked, st.CheckpointDue = ModeRunning, nil, false, false, false
	st.Run.CompleteNotified = false // a finished plan with phases added runs again, and finishes again
	st.Run.Progress()
	if ps := old[cur]; ps != nil && ps.Status == PhaseActive && !st.BoundaryOwed && p.Index(cur) >= 0 && ready(*st, p, cur) {
		st.Phases[cur] = ps
		return cur
	}
	return advance(st, p, now)
}

// Extend adds the phases of p the run does not know yet, pending, and returns their ids: phases the
// human added to the plan file while it ran (plan.Added). A running run that holds no phase (every
// phase left waited for a job) then takes up the next one that can start, and returns it as took ("" for
// none): it starts after a compaction, as at any phase boundary, rather than once a job ends.
func Extend(st *State, p plan.Plan, now time.Time) (added []string, took string) {
	for _, ph := range p.Phases {
		if st.Phases[ph.ID] == nil {
			st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
			added = append(added, ph.ID)
		}
	}
	if st.Mode != ModeRunning || st.Current != "" {
		return added, ""
	}
	if took = Next(*st, p); took != "" {
		st.Current = took
		st.OweBoundary(now)
		st.CheckpointOwed, st.CheckpointDue = false, false // the boundary compacts the conversation anyway
	}
	return added, took
}

// Remaining lists the phases of p not done yet, in order.
func Remaining(st State, p plan.Plan) []string {
	var ids []string
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ps == nil || ps.Status != PhaseDone {
			ids = append(ids, ph.ID)
		}
	}
	return ids
}

// Detach stops the run: its plan is set aside, and baton does nothing until the next plan is attached.
// The roster of its workers stays: the host ends the workers still on it, and a branch one of them left
// commits on is named until the next plan. So does the plan's generation, which the next plan counts on
// from: a worker still on the roster then is the dropped plan's.
func Detach(st *State) error {
	if st.Mode == ModeIdle {
		return errors.New("no plan is attached, so there is nothing to drop")
	}
	run := st.Run
	run.Compaction, run.StopBlocks, run.Escalation = Compaction{Epoch: st.Run.Compaction.Epoch}, 0, nil
	owner, seq, prefix, gen, workers, branch := st.Owner, st.JobSeq, st.IDPrefix, st.Generation, st.Workers, st.RunBranch
	*st = New()
	st.Owner, st.Run, st.JobSeq, st.IDPrefix, st.Generation, st.Workers, st.RunBranch = owner, run, seq, prefix, gen, workers, branch
	return nil
}

// attachKeeping is the state of a run of p attached over prev, with run as its runtime (nil: a new one).
// It keeps prev's owner and the workers prev still counts on (Worker.Live), and counts a generation more.
func attachKeeping(prev State, run *Runtime, p plan.Plan, now time.Time, o Origin) State {
	st := New()
	st.Owner, st.Generation, st.Workers = prev.Owner, prev.Generation+1, prev.LiveWorkers()
	if run != nil {
		st.Run = *run
	}
	st.Mode, st.RulesVersion = ModeRunning, plan.RulesVersion // made by the planning rules of now
	if prev.Mode == ModePaused {
		// The human has the wheel: the new plan waits for /baton run, as the one it replaces did.
		st.Mode, st.PauseContext, st.PauseCompacted = ModePaused, prev.PauseContext, prev.PauseCompacted
	}
	for _, ph := range p.Phases {
		st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
	}
	st.Current = Next(st, p)
	st.Phases[st.Current] = &PhaseState{Status: PhaseActive, StartedAt: now, StartHead: o.Head, StartDirty: o.Dirty}
	return st
}

// ErrNoPlan is returned by transitions that need an attached plan.
var ErrNoPlan = errors.New("no plan is attached (run /baton plan or /baton run first)")

// DoneOpts are what `baton done` may override.
type DoneOpts struct {
	Force      bool // finish a phase other than the one in hand
	DespiteRed bool // finish it although its latest verification is red
}

// RedError turns down done for a phase whose latest verification is red.
type RedError struct{ Job Job }

func (e RedError) Error() string {
	return fmt.Sprintf("phase %s's verification, job %s (`%s`), is red (%s)", e.Job.Phase, e.Job.ID, e.Job.Command, e.Job.Status)
}

// Done marks phase id finished. Unless o.Force is set, id must be the current phase. While any of the
// phase's jobs is queued or running, the phase is verifying on them (PhaseState.On) rather than done:
// Settle marks it done once they have all passed, and reopens it on the first that fails. A phase that
// stands on a red job (Standing) is not finished at all, unless o.DespiteRed. It returns the phase in hand afterwards: the next one, which starts after a compaction, or
// the one under way when id was finished out of order; "" when the plan is complete (ModeComplete) or
// nothing can start until a job finishes.
func Done(st *State, p plan.Plan, id string, now time.Time, o DoneOpts) (next string, err error) {
	if st.Mode == ModeIdle {
		return "", ErrNoPlan
	}
	i := p.Index(id)
	if i < 0 {
		return "", fmt.Errorf("unknown phase %q (phases: %s)", id, phaseList(p))
	}
	ps := st.Phases[id]
	if ps == nil {
		ps = &PhaseState{}
		st.Phases[id] = ps
	}
	if ps.Status == PhaseDone {
		return "", fmt.Errorf("phase %s is already done", id)
	}
	if ps.Status == PhaseWorking {
		return "", fmt.Errorf("phase %s is a worker's: baton marks it done once its work lands", id)
	}
	if id != st.Current && !o.Force {
		return "", fmt.Errorf("phase %s is not the current phase (%s); finish %s first, or pass --force", id, st.Current, st.Current)
	}
	if st.BoundaryOwed && !o.Force {
		return "", fmt.Errorf("phase %s has not started yet: end your turn now, so baton can compact the context before %s begins", id, id)
	}
	for _, j := range st.Standing(id) {
		if j.Red() && !o.DespiteRed {
			return "", RedError{j}
		}
	}
	if running := st.Running(id); len(running) > 0 {
		ps.Status, ps.On = PhaseVerifying, ids(running)
	} else {
		ps.Status, ps.DoneAt, ps.On = PhaseDone, now, nil
	}
	for i := range st.Jobs {
		if x := &st.Jobs[i]; x.Phase == id && x.Finished() && x.Delivered.IsZero() {
			x.Delivered = now // the phase is finished with its results in hand
		}
	}
	st.Blocked, st.CheckpointOwed, st.CheckpointDue = nil, false, false
	st.Run.Progress()
	st.Run.Escalation = nil
	if cs := st.Phases[st.Current]; id != st.Current && cs != nil && cs.Status == PhaseActive {
		return st.Current, nil // a phase finished out of order (--force); the one under way carries on
	}
	return advance(st, p, now), nil
}

// Start marks the current phase active; called when its brief is delivered after the boundary compaction.
// A parked phase resumes: what it is held to starts again from the repository as it is now (o), which
// other phases changed meanwhile.
func Start(st *State, now time.Time, o Origin) {
	if st.Current == "" {
		return
	}
	ps := st.Phases[st.Current]
	if ps == nil {
		ps = &PhaseState{}
		st.Phases[st.Current] = ps
	}
	switch ps.Status {
	case PhaseActive:
	case PhaseParked:
		ps.Status, ps.StartHead, ps.StartDirty, ps.Edited, ps.On = PhaseActive, o.Head, o.Dirty, nil, nil
		if ps.Park != nil {
			ps.Park.Resumed = now
		}
	default:
		ps.Status, ps.StartedAt, ps.StartHead, ps.StartDirty = PhaseActive, now, o.Head, o.Dirty
	}
	st.BoundaryOwed = false
	st.Run.Progress()
}

// InHand reports a phase under way: the plan runs, its current phase is active, and no boundary is owed
// before it (a phase that is done waits for the compaction, whose brief starts the next one).
func (st State) InHand() bool {
	ps := st.Phases[st.Current]
	return st.Mode == ModeRunning && !st.BoundaryOwed && ps != nil && ps.Status == PhaseActive
}

// BoundaryWaits reports a boundary that waits for the lead's turn under way to end, holding none of its
// calls: baton queued it itself (a compaction in flight that takes up a phase: settle and takeUp's
// boundary, with no phase in hand, or a switch), and the turn started before it was queued, or the human
// started it. A boundary the model's own `baton done` owes has no compaction queued until its stop, so
// that turn's calls are held; so are those of a turn baton or Claude Code started after the queueing.
func (st State) BoundaryWaits() bool {
	r, c := st.Run, st.Run.Compaction
	return st.BoundaryOwed && c.InFlight() && c.StartsPhase() && r.TurnOpen && (r.TurnStarted.Before(c.Queued) || r.TurnBy == "human")
}

// SetBlocked records that the model cannot continue without a human.
func SetBlocked(st *State, reason string, now time.Time) error {
	if st.Mode == ModeIdle {
		return ErrNoPlan
	}
	if reason == "" {
		return errors.New("say why you are blocked")
	}
	st.Blocked = &Block{Reason: reason, Since: now}
	st.Run.Progress()
	return nil
}

// LookDue reports a block whose question on screen is to be answered Look again: the lead's loop found
// nobody had answered it in time (Block.Lapsed), and the human has not written since.
func (st State) LookDue() bool {
	b := st.Blocked
	return b != nil && !b.Lapsed.IsZero() && b.Lapsed.After(st.Run.HumanAt)
}

// LiftBlock lifts the block, with the escalation that put it to the human, for the model to look again
// for a safe way on (decide.LookAgain): nobody answered it in time, or the human chose that. It reports
// the block's reason, and false when there was none.
func LiftBlock(st *State) (string, bool) {
	b := st.Blocked
	if b == nil {
		return "", false
	}
	st.Blocked = nil
	if e := st.Run.Escalation; e != nil && e.Kind == "blocked" {
		st.Run.Escalation = nil
	}
	st.Run.Progress()
	return b.Reason, true
}

// SetCheckpoint asks for a mid-phase compaction at the next stop.
func SetCheckpoint(st *State, now time.Time) error {
	if st.Mode != ModeRunning {
		return fmt.Errorf("checkpoints only apply while a plan is running (mode: %s)", st.Mode)
	}
	st.OweCheckpoint(now)
	st.CheckpointAsked, st.CheckpointDue, st.Blocked = false, false, nil
	st.Run.Progress()
	return nil
}

// OweBoundary owes a compaction before the phase in hand starts, from now (OwedAt).
func (st *State) OweBoundary(now time.Time) {
	if !st.BoundaryOwed {
		st.OwedAt = now
	}
	st.BoundaryOwed = true
}

// OweCheckpoint owes a compaction mid-phase, from now (OwedAt).
func (st *State) OweCheckpoint(now time.Time) {
	if !st.CheckpointOwed {
		st.OwedAt = now
	}
	st.CheckpointOwed = true
}

// ResumeGrowth is how far the context may grow while the run is paused before the phase goes on from a
// compacted context instead: the human talked about other things meanwhile, and the phase would carry
// all of it.
const ResumeGrowth = 20_000

// Pause hands the session to the human: baton keeps observing but takes no action. It notes how large
// the context is, so that Resume can tell how much the paused conversation added.
func Pause(st *State) error {
	if st.Mode != ModeRunning {
		return fmt.Errorf("nothing to pause (mode: %s)", st.Mode)
	}
	st.Mode, st.PauseContext, st.PauseCompacted, st.Covered = ModePaused, 0, time.Time{}, time.Time{}
	if cu := st.Run.Context; cu != nil {
		st.PauseContext = cu.Used()
	}
	return nil
}

// GrownWhilePaused is how many tokens the context grew by since the run was paused, or since the lead
// last compacted while it was (PauseCompacted), or 0 when that is not known.
func (st State) GrownWhilePaused() int {
	if st.Mode != ModePaused || st.PauseContext == 0 || st.Run.Context == nil {
		return 0
	}
	return st.Run.Context.Used() - st.PauseContext
}

// Resumable reports a run that Resume would change: one paused, or one stopped for the human (blocked,
// escalated, or waiting on a review).
func (st State) Resumable() bool {
	return st.Mode == ModePaused || st.Blocked != nil || st.Run.Escalation != nil || st.ReviewDue != ""
}

// Resume gives the session back to baton and clears any block. If the context grew by more than
// ResumeGrowth while the run was paused, a checkpoint is due before the phase goes on (CheckpointDue),
// unless a compaction is owed already, or no phase is in hand to go on.
func Resume(st *State) error {
	if !st.Resumable() {
		return fmt.Errorf("nothing to resume (mode: %s)", st.Mode)
	}
	if st.Mode == ModePaused {
		ps := st.Phases[st.Current]
		if st.GrownWhilePaused() > ResumeGrowth && !st.BoundaryOwed && !st.CheckpointOwed && ps != nil && ps.Status == PhaseActive {
			st.CheckpointDue = true
		}
		st.Mode, st.PauseContext, st.PauseCompacted, st.Covered = ModeRunning, 0, time.Time{}, time.Time{}
	}
	if st.ReviewDue != "" {
		Reviewed(st) // the human chose to let the run go on
	}
	st.Blocked, st.Run.Escalation = nil, nil
	st.Run.Progress()
	return nil
}

func phaseList(p plan.Plan) string {
	s := ""
	for i, ph := range p.Phases {
		if i > 0 {
			s += ", "
		}
		s += ph.ID
	}
	return s
}
