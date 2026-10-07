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
	// Decisions are the notes and proposals of this run, oldest first.
	Decisions []Decision `json:"decisions,omitempty"`
	// ReviewDue is the phase whose decisions without the human reached max_auto_decisions: baton stops
	// for the human to review them before anything else, and takes no further notes or proposals.
	// ReviewAt is when it became due.
	ReviewDue string    `json:"review_due,omitempty"`
	ReviewAt  time.Time `json:"review_at,omitzero"`
	// Run is what the hooks observe about the live session.
	Run Runtime `json:"run"`
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
var ErrNoPlan = errors.New("no plan is attached (run /baton attach or /baton plan first)")

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
	st.Blocked, st.Waiting, st.CheckpointOwed = nil, nil, false
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
	st.CheckpointOwed, st.CheckpointAsked, st.Blocked = true, false, nil
	st.Run.Progress()
	return nil
}

// Pause hands the session to the human: baton keeps observing but takes no action.
func Pause(st *State) error {
	if st.Mode != ModeRunning {
		return fmt.Errorf("nothing to pause (mode: %s)", st.Mode)
	}
	st.Mode = ModePaused
	return nil
}

// Resume gives the session back to baton and clears any block.
func Resume(st *State) error {
	if st.Mode != ModePaused && st.Blocked == nil && st.Run.Escalation == nil && st.ReviewDue == "" {
		return fmt.Errorf("nothing to resume (mode: %s)", st.Mode)
	}
	if st.Mode == ModePaused {
		st.Mode = ModeRunning
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
