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
	ModeComplete = "complete" // every phase is done
)

// Phase statuses.
const (
	PhasePending = "pending"
	PhaseActive  = "active"
	PhaseDone    = "done"
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
}

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
}

// Wait is a declared, bounded wait for something outside the model (a build, a deploy).
type Wait struct {
	What  string    `json:"what"`
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

// State is the content of .baton/state.json.
type State struct {
	Version int                    `json:"version"`
	Owner   *Owner                 `json:"owner,omitempty"`
	Mode    string                 `json:"mode"`
	Current string                 `json:"current,omitempty"`
	Phases  map[string]*PhaseState `json:"phases"`
	Blocked *Block                 `json:"blocked,omitempty"`
	Waiting *Wait                  `json:"waiting,omitempty"`
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
	// PauseContext is the context's size in tokens when the run was paused (0: not known).
	PauseContext int `json:"pause_context,omitempty"`
	// Decisions are the notes and proposals of this run, oldest first.
	Decisions []Decision `json:"decisions,omitempty"`
	// ReviewDue is the phase whose decisions without the human reached max_auto_decisions: baton stops
	// for the human to review them before anything else, and takes no further notes or proposals.
	// ReviewAt is when it became due.
	ReviewDue string    `json:"review_due,omitempty"`
	ReviewAt  time.Time `json:"review_at,omitzero"`
	// Replan is a plan the human approved while this run had one, until they say what to do with it.
	Replan *Replan `json:"replan,omitempty"`
	// PlanDrift is the hash of a plan document baton found changed and has already reported, so a change
	// is reported once.
	PlanDrift string `json:"plan_drift,omitempty"`
	// Run is what the hooks observe about the live session.
	Run Runtime `json:"run"`
}

// Replan is a plan the human approved while the run had one. Same says it is in the run's own plan file:
// Claude Code writes every plan a session makes to the same file, so that is a revision of the plan.
type Replan struct {
	File     string    `json:"file"`
	Same     bool      `json:"same"`
	Question string    `json:"question"` // the question baton issued, exactly as issued
	Since    time.Time `json:"since"`
}

// New is the state of a project with no plan attached.
func New() State {
	return State{Version: SchemaVersion, Mode: ModeIdle, Phases: map[string]*PhaseState{}}
}

// Attach starts a fresh run of p: the first phase becomes active.
func Attach(p plan.Plan, now time.Time, o Origin) State {
	return attachKeeping(nil, nil, p, now, o)
}

// Reattach is Attach for a project that may already have an owner: ownership survives a new plan.
func Reattach(prev State, p plan.Plan, now time.Time, o Origin) State {
	run := prev.Run
	run.Compaction, run.StopBlocks, run.Escalation = Compaction{Epoch: prev.Run.Compaction.Epoch}, 0, nil
	return attachKeeping(prev.Owner, &run, p, now, o)
}

// AttachAtBoundary is Reattach for a plan whose first phase starts after a compaction, as if a phase
// before it had just finished: the plan was approved at the end of a planning conversation, or attached
// in a conversation that had other things in it, and the first phase starts with a brief instead of all
// that.
func AttachAtBoundary(prev State, p plan.Plan, now time.Time) State {
	st := Reattach(prev, p, now, Origin{})
	st.Phases[st.Current] = &PhaseState{Status: PhasePending}
	st.BoundaryOwed = true
	return st
}

// Revise moves the run onto a revision of its plan. Phases already done stay done (matched by id); the
// run goes on from the first phase of the revision that is not done. If that is the phase under way, it
// carries on; otherwise it starts after a compaction, as at a phase boundary. It returns that phase, or
// "" when the revision has nothing left to do.
func Revise(st *State, p plan.Plan) string {
	old := st.Phases
	st.Phases = map[string]*PhaseState{}
	for _, ph := range p.Phases {
		if ps := old[ph.ID]; ps != nil && ps.Status == PhaseDone {
			st.Phases[ph.ID] = ps
		} else {
			st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
		}
	}
	st.Mode, st.Blocked, st.Waiting, st.CheckpointOwed, st.CheckpointAsked, st.CheckpointDue = ModeRunning, nil, nil, false, false, false
	st.Run.CompleteNotified = false // a finished plan with phases added runs again, and finishes again
	st.Run.Progress()
	next := nextPending(st, p)
	if next == "" {
		st.Mode, st.Current, st.BoundaryOwed = ModeComplete, "", false
		return ""
	}
	if ps := old[next]; ps != nil && ps.Status == PhaseActive && next == st.Current && !st.BoundaryOwed {
		st.Phases[next] = ps
		return next
	}
	st.Current, st.BoundaryOwed = next, true
	return next
}

// Extend adds the phases of p the run does not know yet, pending, and returns their ids: phases the
// human added to the plan file while it ran (plan.Added).
func Extend(st *State, p plan.Plan) []string {
	var added []string
	for _, ph := range p.Phases {
		if st.Phases[ph.ID] == nil {
			st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
			added = append(added, ph.ID)
		}
	}
	return added
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
func Detach(st *State) error {
	if st.Mode == ModeIdle {
		return errors.New("no plan is attached, so there is nothing to drop")
	}
	run := st.Run
	run.Compaction, run.StopBlocks, run.Escalation = Compaction{Epoch: st.Run.Compaction.Epoch, Rewoken: st.Run.Compaction.Rewoken}, 0, nil
	owner := st.Owner
	*st = New()
	st.Owner, st.Run = owner, run
	return nil
}

func attachKeeping(owner *Owner, prev *Runtime, p plan.Plan, now time.Time, o Origin) State {
	st := New()
	st.Owner = owner
	if prev != nil {
		st.Run = *prev
	}
	st.Mode = ModeRunning
	for _, ph := range p.Phases {
		st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
	}
	st.Current = p.Phases[0].ID
	st.Phases[st.Current] = &PhaseState{Status: PhaseActive, StartedAt: now, StartHead: o.Head, StartDirty: o.Dirty}
	return st
}

// ErrNoPlan is returned by transitions that need an attached plan.
var ErrNoPlan = errors.New("no plan is attached (run /baton plan or /baton run first)")

// Done marks phase id finished. Unless force is set, id must be the current phase. It returns the id of
// the next phase ("" when the plan is complete).
func Done(st *State, p plan.Plan, id string, now time.Time, force bool) (next string, err error) {
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
	if id != st.Current && !force {
		return "", fmt.Errorf("phase %s is not the current phase (%s); finish %s first, or pass --force", id, st.Current, st.Current)
	}
	if st.BoundaryOwed && !force {
		return "", fmt.Errorf("phase %s has not started yet: end your turn now, so baton can compact the context before %s begins", id, id)
	}
	ps.Status, ps.DoneAt = PhaseDone, now
	st.Blocked, st.Waiting, st.CheckpointOwed, st.CheckpointDue = nil, nil, false, false
	st.Run.Progress()
	st.Run.Escalation = nil
	next = nextPending(st, p)
	if next == "" {
		st.Mode, st.Current, st.BoundaryOwed = ModeComplete, "", false
		return "", nil
	}
	if ns := st.Phases[next]; ns != nil && ns.Status == PhaseActive {
		st.Current = next // a phase finished out of order (--force); the one already underway carries on
		return next, nil
	}
	st.Current, st.BoundaryOwed = next, true
	return next, nil
}

// Start marks the current phase active; called when its brief is delivered after the boundary compaction.
func Start(st *State, now time.Time, o Origin) {
	if st.Current == "" {
		return
	}
	ps := st.Phases[st.Current]
	if ps == nil {
		ps = &PhaseState{}
		st.Phases[st.Current] = ps
	}
	if ps.Status != PhaseActive {
		ps.Status, ps.StartedAt, ps.StartHead, ps.StartDirty = PhaseActive, now, o.Head, o.Dirty
	}
	st.BoundaryOwed = false
	st.Run.Progress()
}

// SetBlocked records that the model cannot continue without a human.
func SetBlocked(st *State, reason string, now time.Time) error {
	if st.Mode == ModeIdle {
		return ErrNoPlan
	}
	if reason == "" {
		return errors.New("say why you are blocked")
	}
	st.Blocked, st.Waiting = &Block{Reason: reason, Since: now}, nil
	st.Run.Progress()
	return nil
}

// MaxWait bounds a declared wait. A longer one would let a run sit idle for hours on the model's say-so;
// anything that slow needs the human to know.
const MaxWait = 2 * time.Hour

// SetWaiting records a bounded wait.
func SetWaiting(st *State, what string, d time.Duration, now time.Time) error {
	if st.Mode == ModeIdle {
		return ErrNoPlan
	}
	if what == "" || d <= 0 {
		return errors.New("say what you are waiting for and for how long (e.g. --until 20m)")
	}
	if d > MaxWait {
		return fmt.Errorf("a wait can be at most %s; for anything longer, run `baton blocked \"<what you are waiting for>\" --tried \"<what you tried>\"` so the human knows", MaxWait)
	}
	st.Waiting, st.Blocked = &Wait{What: what, Since: now, Until: now.Add(d)}, nil
	return nil
}

// SetCheckpoint asks for a mid-phase compaction at the next stop.
func SetCheckpoint(st *State) error {
	if st.Mode != ModeRunning {
		return fmt.Errorf("checkpoints only apply while a plan is running (mode: %s)", st.Mode)
	}
	st.CheckpointOwed, st.CheckpointAsked, st.CheckpointDue, st.Blocked = true, false, false, nil
	st.Run.Progress()
	return nil
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
	st.Mode, st.PauseContext = ModePaused, 0
	if cu := st.Run.Context; cu != nil {
		st.PauseContext = cu.Used()
	}
	return nil
}

// GrownWhilePaused is how many tokens the context grew by since the run was paused, or 0 when that is
// not known.
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
// unless a compaction is owed already.
func Resume(st *State) error {
	if !st.Resumable() {
		return fmt.Errorf("nothing to resume (mode: %s)", st.Mode)
	}
	if st.Mode == ModePaused {
		if st.GrownWhilePaused() > ResumeGrowth && !st.BoundaryOwed && !st.CheckpointOwed {
			st.CheckpointDue = true
		}
		st.Mode, st.PauseContext = ModeRunning, 0
	}
	if st.ReviewDue != "" {
		Reviewed(st) // the human chose to let the run go on
	}
	st.Blocked, st.Run.Escalation = nil, nil
	st.Run.Progress()
	return nil
}

func nextPending(st *State, p plan.Plan) string {
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ps == nil || ps.Status != PhaseDone {
			return ph.ID
		}
	}
	return ""
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
