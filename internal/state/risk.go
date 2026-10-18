package state

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// Risk is the exposure a run has built up since a full check last passed: each phase's points, scored from
// what baton knows of it (its files, its record, whether it ran a check, how it departed from the plan), add
// to Total, and a passing full check (a QA phase done, a --qa gate passed) starts it again. Past the plan's
// lines baton asks for an assessment of it, and a verdict of "full now" holds new phases until the full
// check is queued (Held).
//
// The ledger itself (Total to Owed) is the lead's run's. The record the phases' points are read from
// (Checks, DespiteRed) is kept where each phase is worked: the lead's run for its own phases, a phase
// agent's sub-run for its phase. Conflicts is the merge queue's, so the lead's run's.
type Risk struct {
	Total int `json:"total"`
	// Since is when the last full check passed, and Commit the commit it passed on; zero before the first.
	Since  time.Time   `json:"since,omitzero"`
	Commit string      `json:"commit,omitempty"`
	Phases []PhaseRisk `json:"phases,omitempty"` // the phases scored since, oldest first
	// Reviewed is when the exposure was last assessed since Since, and Verdict what the assessment said;
	// Assessment is its words, its verdict first, and Assessor the model that gave them.
	Reviewed   time.Time `json:"reviewed,omitzero"`
	Verdict    string    `json:"verdict,omitempty"`
	Assessment string    `json:"assessment,omitempty"`
	Assessor   string    `json:"assessor,omitempty"`
	// Asking is since when an assessment of the last line crossed is asked (zero: none): Owed, the score's
	// look-in, waits for its answer meanwhile, and is given in its place if none comes.
	Asking time.Time `json:"asking,omitzero"`
	// Held is since when new phases are held for a full check; zero while none are.
	Held time.Time `json:"held,omitzero"`
	// Crossed is the highest of the plan's lines the total crossed since Since (RiskReview, RiskFull; ""
	// for none): each line hands over once per crossing.
	Crossed string `json:"crossed,omitempty"`
	// Told is when the lead was told of the hold (zero: not yet). Owed is a look-in for the lead not given
	// yet: both wait for the lead to hold a phase, since with none in hand it could queue no check.
	Told time.Time `json:"told,omitzero"`
	Owed string    `json:"owed,omitempty"`
	// Scored lists every phase of the plan scored, each once, whatever reset the ledger since; Cleared the
	// passing full checks that reset it (a job's id, or a QA phase's), each once.
	Scored  []string `json:"scored,omitempty"`
	Cleared []string `json:"cleared,omitempty"`
	// Checks counts the check commands (lane.IsCheck) the model's shell ran in each phase, as PostToolUse
	// recorded them; DespiteRed lists the phases finished over a red verification (baton done
	// --despite-red); Conflicts counts each phase's landings that came back to it for conflicts.
	Checks     map[string]int `json:"checks,omitempty"`
	DespiteRed []string       `json:"despite_red,omitempty"`
	Conflicts  map[string]int `json:"conflicts,omitempty"`
}

// PhaseRisk is one phase's score.
type PhaseRisk struct {
	Phase     string       `json:"phase"`
	Points    int          `json:"points"`
	Factors   []RiskFactor `json:"factors,omitempty"`
	Deviation string       `json:"deviation,omitempty"` // the phase's own words (PhaseState.Deviation)
	// At is when the phase was done (a lead phase) or landed (an agent's): a full check queued after it
	// checks its work.
	At time.Time `json:"at,omitzero"`
}

// RiskFactor is one thing that added to a phase's score, and what it found ("412 lines changed").
type RiskFactor struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
	Detail string `json:"detail,omitempty"`
}

// The plan's lines a ledger crosses (Risk.Crossed): the review line asks for an assessment once per
// crossing, the hard (full) line again, whatever the last one said.
const (
	RiskReview = "review"
	RiskFull   = "full"
)

// VerdictFull is the assessment's verdict that holds new phases until the full check is queued (RiskVerdict).
const VerdictFull = "full"

// MessageRisk is the kind of baton's messages to the lead about the ledger: a look-in (the score and its
// factors) or the hold.
const MessageRisk = "risk"

// HoldRisk is the hold on a phase that waits for a worker while new phases are held for a full check.
const HoldRisk = "risk"

// ledger is st's Risk, made if st had none.
func (st *State) ledger() *Risk {
	if st.Risk == nil {
		st.Risk = &Risk{}
	}
	return st.Risk
}

// CountCheck records that the model's shell ran a check command in phase.
func CountCheck(st *State, phase string) {
	r := st.ledger()
	if r.Checks == nil {
		r.Checks = map[string]int{}
	}
	r.Checks[phase]++
}

// MarkDespiteRed records that phase was finished over a red verification.
func MarkDespiteRed(st *State, phase string) {
	if r := st.ledger(); !slices.Contains(r.DespiteRed, phase) {
		r.DespiteRed = append(r.DespiteRed, phase)
	}
}

// CountReturned records that a landing of phase came back to it for conflicts.
func CountReturned(st *State, phase string) {
	r := st.ledger()
	if r.Conflicts == nil {
		r.Conflicts = map[string]int{}
	}
	r.Conflicts[phase]++
}

// Checks is how many check commands the model's shell ran in phase (CountCheck).
func (st State) Checks(phase string) int {
	if st.Risk == nil {
		return 0
	}
	return st.Risk.Checks[phase]
}

// FinishedDespiteRed reports a phase finished over a red verification (MarkDespiteRed).
func (st State) FinishedDespiteRed(phase string) bool {
	return st.Risk != nil && slices.Contains(st.Risk.DespiteRed, phase)
}

// LandingsReturned is how many of phase's landings came back to it for conflicts (CountReturned).
func (st State) LandingsReturned(phase string) int {
	if st.Risk == nil {
		return 0
	}
	return st.Risk.Conflicts[phase]
}

// Unscored lists the phases of p done and not scored yet, in the order they were done.
func (st State) Unscored(p plan.Plan) []string {
	var out []string
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status == PhaseDone && (st.Risk == nil || !slices.Contains(st.Risk.Scored, ph.ID)) {
			out = append(out, ph.ID)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return st.Phases[a].DoneAt.Compare(st.Phases[b].DoneAt) })
	return out
}

// Scored reports a phase the ledger has scored.
func (st State) Scored(phase string) bool {
	return st.Risk != nil && slices.Contains(st.Risk.Scored, phase)
}

// AddRisk adds a phase's score to the ledger, once, and returns its total.
func AddRisk(st *State, pr PhaseRisk) int {
	r := st.ledger()
	if slices.Contains(r.Scored, pr.Phase) {
		return r.Total
	}
	r.Scored = append(r.Scored, pr.Phase)
	r.Phases = append(r.Phases, pr)
	r.Total += pr.Points
	return r.Total
}

// MarkScored records phase as scored with nothing added: a QA phase, whose full check resets the ledger.
func MarkScored(st *State, phase string) {
	if r := st.ledger(); !slices.Contains(r.Scored, phase) {
		r.Scored = append(r.Scored, phase)
	}
}

// ResetRisk starts the ledger again after a passing full check, by (a job's id, or a QA phase's), once: it
// passed at at, on commit, and checked the work of the phases done before covered, which leave the ledger;
// those done since stay. The crossings, the review and a hold go with it: the full check is what a hold
// waits for. It reports false when by reset it already.
func ResetRisk(st *State, by string, at time.Time, commit string, covered time.Time) bool {
	r := st.ledger()
	if slices.Contains(r.Cleared, by) {
		return false
	}
	r.Cleared = append(r.Cleared, by)
	kept := r.Phases[:0:0]
	total := 0
	for _, pr := range r.Phases {
		if pr.At.After(covered) {
			kept = append(kept, pr)
			total += pr.Points
		}
	}
	r.Phases, r.Total, r.Since, r.Commit = kept, total, at, commit
	r.Crossed, r.Reviewed, r.Verdict, r.Held, r.Told, r.Owed = "", time.Time{}, "", time.Time{}, time.Time{}, ""
	r.Assessment, r.Assessor, r.Asking = "", "", time.Time{}
	return true
}

// MarkCleared records a full check that reset the ledger already, with nothing reset: the ledger's record
// of it was lost (an older baton wrote the run's state without it).
func MarkCleared(st *State, by string) {
	if r := st.ledger(); !slices.Contains(r.Cleared, by) {
		r.Cleared = append(r.Cleared, by)
	}
}

// Cleared reports a full check (a job's id, a QA phase's) that reset the ledger already.
func (st State) Cleared(by string) bool {
	return st.Risk != nil && slices.Contains(st.Risk.Cleared, by)
}

// RiskVerdict records the assessment's verdict of the exposure: VerdictFull holds new phases until the full
// check is queued, or noted against, or escalation_timeout passes (the coordinator's riskHold). It reports
// whether the verdict started a hold.
func RiskVerdict(st *State, verdict string, now time.Time) bool {
	r := st.ledger()
	r.Reviewed, r.Verdict = now, verdict
	if verdict != VerdictFull || !r.Held.IsZero() {
		return false
	}
	r.Held, r.Told = now, time.Time{}
	return true
}

// ReleaseRisk lifts the hold on new phases. It reports false when none stood.
func ReleaseRisk(st *State) bool {
	r := st.Risk
	if r == nil || r.Held.IsZero() {
		return false
	}
	r.Held, r.Told = time.Time{}, time.Time{}
	return true
}

// RiskHeld reports new phases held for a full check.
func (st State) RiskHeld() bool { return st.Risk != nil && !st.Risk.Held.IsZero() }

// RiskTotal is the ledger's total since the last full check.
func (st State) RiskTotal() int {
	if st.Risk == nil {
		return 0
	}
	return st.Risk.Total
}
