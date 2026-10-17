package state

import "time"

// Risk is the exposure a run has built up since a full check last passed: each phase's points, scored from
// what baton knows of it (its files, its record, whether it ran a check, how it departed from the plan), add
// to Total, and a passing full check (a QA phase done, a --qa gate passed) starts it again. Past the plan's
// lines baton asks for an assessment of it, and a verdict of "full now" holds new phases until the full
// check is queued (Held).
type Risk struct {
	Total int `json:"total"`
	// Since is when the last full check passed, and Commit the commit it passed on; zero before the first.
	Since  time.Time   `json:"since,omitzero"`
	Commit string      `json:"commit,omitempty"`
	Phases []PhaseRisk `json:"phases,omitempty"` // the phases scored since, oldest first
	// Reviewed is when the exposure was last assessed since Since, and Verdict what the assessment said.
	Reviewed time.Time `json:"reviewed,omitzero"`
	Verdict  string    `json:"verdict,omitempty"`
	// Held is since when new phases are held for a full check; zero while none are.
	Held time.Time `json:"held,omitzero"`
}

// PhaseRisk is one phase's score.
type PhaseRisk struct {
	Phase     string       `json:"phase"`
	Points    int          `json:"points"`
	Factors   []RiskFactor `json:"factors,omitempty"`
	Deviation string       `json:"deviation,omitempty"` // the phase's own words (PhaseState.Deviation)
}

// RiskFactor is one thing that added to a phase's score, and what it found ("412 lines changed").
type RiskFactor struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
	Detail string `json:"detail,omitempty"`
}
