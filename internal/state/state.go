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
}

// Block is a reason the model cannot continue without a human.
type Block struct {
	Reason string    `json:"reason"`
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
}

// New is the state of a project with no plan attached.
func New() State {
	return State{Version: SchemaVersion, Mode: ModeIdle, Phases: map[string]*PhaseState{}}
}

// Attach starts a fresh run of p: the first phase becomes active.
func Attach(p plan.Plan, now time.Time, head string) State {
	return attachKeeping(nil, p, now, head)
}

// Reattach is Attach for a project that may already have an owner: ownership survives a new plan.
func Reattach(prev State, p plan.Plan, now time.Time, head string) State {
	return attachKeeping(prev.Owner, p, now, head)
}

func attachKeeping(owner *Owner, p plan.Plan, now time.Time, head string) State {
	st := New()
	st.Owner = owner
	st.Mode = ModeRunning
	for _, ph := range p.Phases {
		st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
	}
	st.Current = p.Phases[0].ID
	st.Phases[st.Current] = &PhaseState{Status: PhaseActive, StartedAt: now, StartHead: head}
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
	ps.Status, ps.DoneAt = PhaseDone, now
	st.Blocked, st.Waiting, st.CheckpointOwed = nil, nil, false
	next = nextPending(st, p)
	if next == "" {
		st.Mode, st.Current, st.BoundaryOwed = ModeComplete, "", false
		return "", nil
	}
	st.Current, st.BoundaryOwed = next, true
	return next, nil
}

// Start marks the current phase active; called when its brief is delivered after the boundary compaction.
func Start(st *State, now time.Time, head string) {
	if st.Current == "" {
		return
	}
	ps := st.Phases[st.Current]
	if ps == nil {
		ps = &PhaseState{}
		st.Phases[st.Current] = ps
	}
	if ps.Status != PhaseActive {
		ps.Status, ps.StartedAt, ps.StartHead = PhaseActive, now, head
	}
	st.BoundaryOwed = false
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
	return nil
}

// SetWaiting records a bounded wait.
func SetWaiting(st *State, what string, d time.Duration, now time.Time) error {
	if st.Mode == ModeIdle {
		return ErrNoPlan
	}
	if what == "" || d <= 0 {
		return errors.New("say what you are waiting for and for how long (e.g. --until 20m)")
	}
	st.Waiting, st.Blocked = &Wait{What: what, Since: now, Until: now.Add(d)}, nil
	return nil
}

// SetCheckpoint asks for a mid-phase compaction at the next stop.
func SetCheckpoint(st *State) error {
	if st.Mode != ModeRunning {
		return fmt.Errorf("checkpoints only apply while a plan is running (mode: %s)", st.Mode)
	}
	st.CheckpointOwed = true
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
	if st.Mode != ModePaused && st.Blocked == nil {
		return fmt.Errorf("nothing to resume (mode: %s)", st.Mode)
	}
	if st.Mode == ModePaused {
		st.Mode = ModeRunning
	}
	st.Blocked = nil
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
