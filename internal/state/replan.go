package state

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A replan restructures the phases of the run's plan that have not started (package replan). Its files
// live in the run's replan/ folder:
//
//	replan/0.md      the plan's text as the human approved it (KeepApproved)
//	replan/<n>.md    the plan's text as replan n found it: copied back over the plan file, it undoes replan n
//	replan/draft.md  the draft the model edits while a replan is open
//
// A new plan, or a revision the human approves, starts the folder afresh, the old one kept with the
// replaced plan's record (Archive's folder).

// Who opens a replan (Replan.By): the human's /baton replan, or baton itself after an upgrade whose
// planning rules changed, at the start of a QA phase over a remainder that is mostly a chain, once a
// phase the plan marks `Replan: after` is done, and once a file in a second landing conflict is named by
// phases not started (ConflictCue).
const (
	ReplanHuman    = "human"
	ReplanUpgrade  = "upgrade"
	ReplanQA       = "qa"
	ReplanMarked   = "marked"
	ReplanConflict = "conflict"
)

// Replan is a replan of the run's plan that is open: the model edits the draft, until baton follows it
// (`baton replanned --notes`), the model finds nothing to change (`--none`), or it lapses. No new phase
// agent starts while it is open.
type Replan struct {
	By    string    `json:"by"`              // ReplanHuman, ReplanUpgrade, ReplanQA, ReplanMarked or ReplanConflict
	Words string    `json:"words,omitempty"` // what the human asked it to consider
	Since time.Time `json:"since"`
	N     int       `json:"n"` // replan/<n>.md holds the plan's text as it found it
	// Cue is the phases that cued a replan baton opened by itself: the QA phase that started, the phases
	// marked Replan: after that are done, or the phases not started that name a file that conflicted
	// twice. Then is the phase whose brief came after its own, which the lead begins once it is finished.
	// Files are the files in a second landing conflict it settles (ConflictCue).
	Cue   []string `json:"cue,omitempty"`
	Then  string   `json:"then,omitempty"`
	Files []string `json:"files,omitempty"`
}

// Cued reports that baton has looked at the cue of an automatic replan of kind by (ReplanQA,
// ReplanMarked, ReplanConflict) that phase (for ReplanConflict, a file) gives, which it does once
// (State.ReplanCued).
func (st State) Cued(by, phase string) bool { return slices.Contains(st.ReplanCued, by+":"+phase) }

// Cue records that baton looked at the cue of an automatic replan of kind by that phase gives.
func (st *State) Cue(by, phase string) {
	if !st.Cued(by, phase) {
		st.ReplanCued = append(st.ReplanCued, by+":"+phase)
	}
}

// Replanned is a replan baton followed: what it changed, against the plan before it and against the plan
// as the human approved it (replan/0.md), and the decision that records it with its undo.
type Replanned struct {
	N        int           `json:"n"`
	By       string        `json:"by"`
	Words    string        `json:"words,omitempty"`
	At       time.Time     `json:"at"`
	Notes    string        `json:"notes,omitempty"`
	Changed  []plan.Change `json:"changed,omitempty"`
	Approved []plan.Change `json:"approved,omitempty"` // the plan after it, against the approved one
	Decision string        `json:"decision,omitempty"`
	// Undone is when the plan file was found copied back to the text this replan, or one before it,
	// started from (package replan's Restore).
	Undone time.Time `json:"undone,omitzero"`
}

// OpenReplan opens r on the run, which must be running or paused with no replan open.
func OpenReplan(st *State, r Replan) error {
	switch {
	case st.Mode != ModeRunning && st.Mode != ModePaused:
		return fmt.Errorf("no plan is running here to replan (mode: %s)", st.Mode)
	case st.Replan != nil:
		return fmt.Errorf("a replan is open already (by %s, since %s)", st.Replan.By, st.Replan.Since.Local().Format("15:04"))
	}
	st.Replan = &r
	return nil
}

// Frozen is the set of the phases of p a replan may not change: every phase that started or is done (any
// status but pending), one given to a worker, and the phase in hand, even before its brief.
func Frozen(st State, p plan.Plan) map[string]bool {
	out := map[string]bool{}
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ph.ID == st.Current || ps != nil && (ps.Status != PhasePending || ps.Worker != "") {
			out[ph.ID] = true
		}
	}
	return out
}

// Unstarted lists the phases of p a replan may restructure, in order: those not Frozen.
func Unstarted(st State, p plan.Plan) []string {
	frozen := Frozen(st, p)
	var ids []string
	for _, ph := range p.Phases {
		if !frozen[ph.ID] {
			ids = append(ids, ph.ID)
		}
	}
	return ids
}

// AdoptReplan moves the run onto p, its plan replanned: every phase p keeps stays as it stands, a phase p
// adds joins pending, and a phase it drops, which had not started, leaves the run, with any job of its
// cancelled. A running run that holds no phase takes up the next one p lets start, as took (it starts
// after a compaction, as at any boundary), or is complete when p leaves it nothing to do. It refuses a p
// that drops a phase that has started (since the replan was checked).
func AdoptReplan(st *State, p plan.Plan, now time.Time) (took string, err error) {
	keep := make(map[string]bool, len(p.Phases))
	for _, ph := range p.Phases {
		keep[ph.ID] = true
	}
	frozen := Frozen(*st, plan.Plan{Phases: phasesOf(st)})
	for id := range st.Phases {
		if !keep[id] && frozen[id] {
			return "", fmt.Errorf("%s has started since the draft was checked, and the draft drops it", id)
		}
	}
	for id := range st.Phases {
		if !keep[id] {
			delete(st.Phases, id)
		}
	}
	for _, ph := range p.Phases {
		if st.Phases[ph.ID] == nil {
			st.Phases[ph.ID] = &PhaseState{Status: PhasePending}
		}
	}
	CancelJobs(st, func(j Job) bool { return p.Index(j.Phase) < 0 }, "its phase is no longer in the plan", now)
	st.PlanDrift = ""
	st.Run.Progress()
	if st.Mode == ModeRunning && st.Current == "" {
		took = advance(st, p, now)
		st.CheckpointOwed, st.CheckpointDue = false, false
	}
	return took, nil
}

// phasesOf is the phases st has on record, as a plan of ids alone.
func phasesOf(st *State) []plan.Phase {
	out := make([]plan.Phase, 0, len(st.Phases))
	for id := range st.Phases {
		out = append(out, plan.Phase{ID: id})
	}
	return out
}

// RecordReplan records r, a replan baton followed, with its decision: a note of the run's own (no phase's,
// so no phase's review counts it), shown in the session already (Announced), with how to undo it. The plan
// is replanned under the planning rules of the baton that followed it (RulesVersion). It returns the
// decision.
func RecordReplan(st *State, r Replanned, what, undo string) Decision {
	d := Decision{ID: fmt.Sprintf("%sd%d", st.IDPrefix, len(st.Decisions)+1), Kind: Note, What: what, Undo: undo, At: r.At, Announced: true}
	st.Decisions = append(st.Decisions, d)
	r.Decision = d.ID
	st.Replans = append(st.Replans, r)
	st.RulesVersion = plan.RulesVersion
	return d
}

// ReplanDir is the run's replan/ folder.
func (s *Store) ReplanDir() string { return s.path("replan") }

// ReplanText is replan/<n>.md: the plan's text as replan n found it (0: as the human approved it).
func (s *Store) ReplanText(n int) string { return filepath.Join(s.ReplanDir(), strconv.Itoa(n)+".md") }

// DraftPath is the draft the model edits while a replan is open.
func (s *Store) DraftPath() string { return filepath.Join(s.ReplanDir(), "draft.md") }

// NextReplan is the number the next replan takes: one past the highest kept (1 when none is).
func (s *Store) NextReplan() int {
	next := 1
	for _, n := range s.keptTexts() {
		next = max(next, n+1)
	}
	return next
}

// keptTexts are the numbers of the texts the replan folder keeps (replan/<n>.md).
func (s *Store) keptTexts() []int {
	entries, _ := os.ReadDir(s.ReplanDir())
	var out []int
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if n, err := strconv.Atoi(name); ok && err == nil && n >= 0 && !e.IsDir() {
			out = append(out, n)
		}
	}
	return out
}

// Restores reports the replan whose kept text doc is (replan/<n>.md): one copied back over the plan file
// undoes the replans from n on. The latest such text, when several are the same.
func (s *Store) Restores(doc []byte) (int, bool) {
	best, found := -1, false
	for _, n := range s.keptTexts() {
		if b, err := os.ReadFile(s.ReplanText(n)); err == nil && bytes.Equal(b, doc) && n > best {
			best, found = n, true
		}
	}
	return best, found
}

// KeepApproved starts the run's replan folder afresh for a plan the human approved, whose text is doc:
// the folder of the plan it replaces goes into kept, that plan's record (Archive; "" for none: it is
// removed), and doc becomes replan/0.md. It takes no lock.
func (s *Store) KeepApproved(doc []byte, kept string) error {
	if err := s.KeepReplans(kept); err != nil {
		return err
	}
	return s.WriteReplanText(0, doc, false)
}

// KeepReplans moves the run's replan folder into kept, the record of the plan it belongs to (Archive), as
// that plan is replaced or dropped; with no record ("") it is removed. It takes no lock.
func (s *Store) KeepReplans(kept string) error {
	dir := s.ReplanDir()
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if kept != "" && os.Rename(dir, filepath.Join(kept, "replan")) == nil {
		return nil
	}
	return os.RemoveAll(dir)
}

// WriteReplanText writes doc as replan/<n>.md, unless one is there already and over is false.
func (s *Store) WriteReplanText(n int, doc []byte, over bool) error {
	if err := os.MkdirAll(s.ReplanDir(), 0o755); err != nil {
		return err
	}
	path := s.ReplanText(n)
	if _, err := os.Stat(path); err == nil && !over {
		return nil
	}
	return writeAtomic(path, doc)
}

// WriteDraft writes doc as the draft the model edits.
func (s *Store) WriteDraft(doc []byte) error {
	if err := os.MkdirAll(s.ReplanDir(), 0o755); err != nil {
		return err
	}
	return writeAtomic(s.DraftPath(), doc)
}

// DropReplan removes what an open replan n left that nothing needs once it is closed with the plan
// unchanged: the draft, and the text it kept to undo it.
func (s *Store) DropReplan(n int) {
	os.Remove(s.DraftPath())
	if n > 0 {
		os.Remove(s.ReplanText(n))
	}
}

// ReplacePlanFile writes doc over the plan file at path, atomically, keeping its mode.
func ReplacePlanFile(path string, doc []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := writeAtomic(path, doc); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
