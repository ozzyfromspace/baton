package replan

import (
	"maps"
	"slices"
	"time"

	briefs "github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton replans by itself, never on a timer and never on the model's initiative: at the lead's phase
// boundary, where the lead starts the next phase from a fresh context, it opens a replan (Auto) whose
// brief comes before the phase's, and the lead replans, runs `baton replanned`, then begins the phase.
// Four cues open one (Due), each once:
//
//   - the run's plan was made under older planning rules than this baton's (state.State.RulesVersion
//     below plan.RulesVersion: a run carried across an upgrade), once per run per rules version, at its
//     first boundary (ReplanUpgrade);
//   - a QA phase is under way, and the phases not started are mostly one chain (plan.Shape.MostlyChain),
//     at most once per QA phase (ReplanQA);
//   - a phase marked `Replan: after` is done (plan.Phase.Marked): what it found should reshape the phases
//     after it, once per marked phase (ReplanMarked);
//   - a file conflicted in a second landing of the run, and phases not started name it in their sections
//     (state.ConflictCue, recorded by the merge queue): they would serialize on it too, once per file
//     (ReplanConflict).
//
// The first two change the structure alone, and are kept only where the shape improves (Structural,
// Improved); a marked one, or a conflict's, may rework the steps of the phases not started, and split
// them, but never drop or merge work (LimitsOf). The setting auto_replan turns them off; the human's
// /baton replan works either way.

// Cue is what the lead's phase boundary owes the automatic replans: the replan it opens, if any, and the
// cues it settles, each looked at once whether a replan opens or not.
type Cue struct {
	By string // the replan to open: state.ReplanMarked, ReplanConflict, ReplanQA or ReplanUpgrade; "" for none
	// Phases are the phases that cue it: the marked phases done, the phases not started that name a file
	// that conflicted twice, or the QA phases under way.
	Phases []string
	Rules  bool     // the run's plan was made under older planning rules
	QA     []string // the QA phases under way that baton has not looked at
	Marked []string // the phases marked Replan: after that are done, and have cued no replan
	// Conflict is the files in a second landing conflict that have cued no replan (state.ConflictCue);
	// Files, those of them that phases not started still name, and Named those phases, in the plan's order.
	Conflict []string
	Files    []string
	Named    []string
	Why      string // why none opens, with something to settle
}

// Any reports a cue to settle.
func (c Cue) Any() bool { return c.Rules || len(c.QA) > 0 || len(c.Marked) > 0 || len(c.Conflict) > 0 }

// settle records in st that each of c's cues was looked at.
func (c Cue) settle(st *state.State) {
	if c.Rules {
		st.RulesVersion = plan.RulesVersion
	}
	for _, id := range c.QA {
		st.Cue(state.ReplanQA, id)
	}
	for _, id := range c.Marked {
		st.Cue(state.ReplanMarked, id)
	}
	for _, p := range c.Conflict {
		st.Cue(state.ReplanConflict, p)
	}
}

// fields are c's, for an event.
func (c Cue) fields() map[string]any {
	f := map[string]any{}
	if c.By != "" {
		f["by"] = c.By
	}
	if c.Rules {
		f["rules"] = plan.RulesVersion
	}
	if len(c.QA) > 0 {
		f["qa"] = c.QA
	}
	if len(c.Marked) > 0 {
		f["marked"] = c.Marked
	}
	if len(c.Conflict) > 0 {
		f["conflict"] = c.Conflict
	}
	if c.Why != "" {
		f["why"] = c.Why
	}
	return f
}

// Due is what the lead's phase boundary owes the automatic replans on run st of plan pl (Cue): nothing
// unless the plan runs with no replan open. A marked phase done opens the widest replan, then a file in a
// second landing conflict, then a QA phase over a chain, then newer planning rules; one replan settles
// every cue the boundary has. Each opens only where it can change something: a phase is left to replan,
// a phase not started still names the file that conflicted, and for the structure alone, a chain to
// shorten (newer rules) or most of the phases left in one chain (a QA phase).
func Due(st state.State, pl plan.Plan) (c Cue) {
	if st.Mode != state.ModeRunning || st.Replan != nil || len(pl.Phases) == 0 {
		return c
	}
	frozen := state.Frozen(st, pl)
	c.Rules = st.RulesVersion < plan.RulesVersion
	for _, ph := range pl.Phases {
		ps := st.Phases[ph.ID]
		done := ps != nil && ps.Status == state.PhaseDone
		if ph.QA() && frozen[ph.ID] && !done && !st.Cued(state.ReplanQA, ph.ID) {
			c.QA = append(c.QA, ph.ID)
		}
		if ph.Marked() && done && !st.Cued(state.ReplanMarked, ph.ID) {
			c.Marked = append(c.Marked, ph.ID)
		}
	}
	c.conflicts(st, pl, frozen)
	if !c.Any() {
		return c
	}
	rest := pl.Part(unstarted(pl, frozen)).Shape()
	switch {
	case rest.Phases == 0:
		c.Why = "every phase has started"
	case len(c.Marked) > 0:
		c.By, c.Phases = state.ReplanMarked, c.Marked
	case len(c.Files) > 0:
		c.By, c.Phases = state.ReplanConflict, c.Named
	case len(c.QA) > 0 && rest.MostlyChain():
		c.By, c.Phases = state.ReplanQA, c.QA
	case c.Rules && len(rest.Chain) > 1:
		c.By = state.ReplanUpgrade
	case len(c.QA) > 0:
		c.Why = "the phases not started are not mostly one chain"
	case c.Rules:
		c.Why = "the phases not started have no chain to shorten"
	default:
		c.Why = "the phases that named the files that conflicted have started"
	}
	return c
}

// Auto opens the replan the lead's run on s owes at the phase boundary where it begins phase then (Due),
// and gives it with its brief, which comes before the phase's: ok is false when none opens. The cues are
// settled first, each once, whether a replan opens or not (a cue looked at with nothing to open logs
// replan_skipped). The caller checks the setting (auto_replan) and that the brief is the lead's, at a
// phase boundary.
func Auto(s *state.Store, then string, now time.Time, max time.Duration) (o Opened, ok bool, err error) {
	pl, err := s.LoadPlan()
	if err != nil {
		return o, false, nil // no plan: nothing to replan
	}
	if st, err := s.Load(); err != nil || !Due(st, pl).Any() {
		return o, false, err
	}
	var c Cue
	st, err := s.Update(func(st *state.State) error {
		c = Due(*st, pl)
		c.settle(st)
		return nil
	})
	if err != nil || !c.Any() {
		return o, false, err
	}
	if c.By == "" {
		s.Event("replan_skipped", c.fields())
		return o, false, nil
	}
	o, err = open(s, st, pl, state.Replan{By: c.By, Cue: c.Phases, Files: c.Files, Then: then, Since: now}, max)
	if err != nil {
		f := c.fields()
		f["why"] = firstLine(err)
		s.Event("replan_skipped", f)
		return o, false, nil
	}
	return o, true, nil
}

// Owed reports a replan of baton's own due at the lead's phase boundary under way on run st of plan pl
// (Due), its brief not given yet: the boundary is owed, or its brief is. New phase agents wait for it as
// they do while it is open, since one would take a phase it restructures first, for at most max from when
// the boundary was owed or its brief was: past that, they go on.
func Owed(st state.State, pl plan.Plan, now time.Time, max time.Duration) bool {
	var since time.Time
	switch b := st.Run.Briefing; {
	case st.BoundaryOwed:
		since = st.OwedAt
	case b != nil && AtBoundary(b.Kind):
		since = b.At
	default:
		return false
	}
	return now.Before(since.Add(max)) && Due(st, pl).By != ""
}

// AtBoundary reports a brief (briefs.Boundary, …) that starts or resumes a phase after a compaction at a
// phase boundary: where baton opens its own replans.
func AtBoundary(kind string) bool {
	return kind == briefs.Boundary || kind == briefs.Switch || kind == briefs.Resume
}

// conflicts finds the files of st's ConflictCue that have cued no replan (Conflict), those of them that
// phases of pl not started (none of frozen) still name (Files), and those phases (Named), in the plan's
// order.
func (c *Cue) conflicts(st state.State, pl plan.Plan, frozen map[string]bool) {
	named := map[string]bool{}
	for _, p := range slices.Sorted(maps.Keys(st.ConflictCue)) {
		if st.Cued(state.ReplanConflict, p) {
			continue
		}
		c.Conflict = append(c.Conflict, p)
		still := false
		for _, id := range st.ConflictCue[p] {
			if pl.Index(id) >= 0 && !frozen[id] {
				named[id], still = true, true
			}
		}
		if still {
			c.Files = append(c.Files, p)
		}
	}
	for _, ph := range pl.Phases {
		if named[ph.ID] {
			c.Named = append(c.Named, ph.ID)
		}
	}
}
