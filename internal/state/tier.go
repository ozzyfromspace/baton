package state

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A phase runs on its tier: the model and effort its plan's Model: and Effort: lines name (plan/tier.go),
// raised a step at a time when it goes badly (TierRaise), with its model among the families the human
// allows (config.Tiers). A phase agent is spawned on it (its effort is its type's, state.PhaseAgentAt, its
// model the spawn's), and keeps it for its life; the lead runs on its launch flags, which a restart in
// place changes (loop/tier.go). Each is checked against what its loop says it runs on (Runtime.Model,
// Runtime.Effort: every hook says the effort, the lead's status line both) and against what its replies
// were served (transcript.LastServed). docs/research/tiers.md has the evidence.

// TierRaise is a step baton raised a phase's tier, from the plan's or an earlier step's.
type TierRaise struct {
	From plan.Tier `json:"from"`
	To   plan.Tier `json:"to"`
	// Why is what raised it, in words; Reds the red runs of the phase's own gate counted then.
	Why  string    `json:"why"`
	Reds int       `json:"reds,omitempty"`
	At   time.Time `json:"at"`
}

// TierFor is the tier phase runs on in run st of plan p: its plan's, or the last step baton raised it to,
// with its model kept among allowed (AllowedModel). A part the plan leaves to the session stays "".
func TierFor(st State, p plan.Plan, phase string, allowed []string) plan.Tier {
	t := p.TierOf(phase)
	if ps := st.Phases[phase]; ps != nil && len(ps.Raises) > 0 {
		t = ps.Raises[len(ps.Raises)-1].To
	}
	t.Model = AllowedModel(t.Model, allowed)
	return t
}

// AllowedModel is model, a family, if allowed has it; else the highest family allowed below it, else the
// lowest allowed above it. "" stays "", and so does any model when allowed is empty.
func AllowedModel(model string, allowed []string) string {
	if model == "" || len(allowed) == 0 || slices.Contains(allowed, model) {
		return model
	}
	at := slices.Index(plan.Models, model)
	best := ""
	for i, f := range plan.Models {
		if slices.Contains(allowed, f) && (i < at || best == "") {
			best = f
		}
	}
	return best
}

// Served is the tier the loop of r runs on, as its hooks and its status line last said: its model's family
// and its effort, each "" while nothing has said.
func (r Runtime) Served() plan.Tier { return plan.Tier{Model: plan.Family(r.Model), Effort: r.Effort} }

// SawTier records what a hook's input or the status line says the loop runs on: model (a full id, or "")
// and effort (or ""). A part not said keeps what was recorded.
func SawTier(r *Runtime, model, effort string) {
	if model != "" {
		r.Model = model
	}
	if effort != "" {
		r.Effort = effort
	}
}

// Fits reports a served tier that is the one wanted: every part want names, served has. A part served
// does not know yet does not fit.
func Fits(want, served plan.Tier) bool {
	return (want.Model == "" || want.Model == served.Model) && (want.Effort == "" || want.Effort == served.Effort)
}

// Raised is the tier phase was last raised to, or nil while it runs on its plan's.
func (st State) Raised(phase string) *plan.Tier {
	if ps := st.Phases[phase]; ps != nil && len(ps.Raises) > 0 {
		t := ps.Raises[len(ps.Raises)-1].To
		return &t
	}
	return nil
}

// LeadRuns is the tier the lead runs on: its launch flags' parts (Runtime.Launched), else what its hooks
// and its status line said (Runtime.Served); a part neither says is "".
func (st State) LeadRuns() plan.Tier {
	t, served := st.Run.Launched, st.Run.Served()
	if t.Model == "" {
		t.Model = served.Model
	}
	if t.Effort == "" {
		t.Effort = served.Effort
	}
	return t
}

// LeadFits reports a phase of p whose tier the lead runs on, as far as is known: each part its tier names
// is the lead's, or the lead's is not known yet (a lead nothing has said of is taken to fit, so that the
// phases it takes do not turn on what it has not said yet).
func (st State) LeadFits(p plan.Plan, phase string) bool {
	want, runs := TierFor(st, p, phase, st.Run.Models), st.LeadRuns()
	return (want.Model == "" || runs.Model == "" || want.Model == runs.Model) && (want.Effort == "" || runs.Effort == "" || want.Effort == runs.Effort)
}

// RedRuns counts the red runs of phase's own gate in st that ended after since, and returns the last of
// them: its verify jobs that failed. A job that ran out of time is left out (a hang or the machine ends one
// as often as the code does), and so is a whole run whose environment took a stage (EnvLost), or one whose
// environment the host has not read yet (EnvUnread): what raises a phase's tier is its own code failing.
func RedRuns(st State, phase string, since time.Time) (int, *Job) {
	n := 0
	var last *Job
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if j.Phase != phase || j.Kind == JobAwait || j.Status != JobFailed || !j.Ended.After(since) || len(j.EnvLost) > 0 ||
			WholeRun(*j) && j.EnvRead.IsZero() {
			continue
		}
		n++
		if last == nil || j.Ended.After(last.Ended) {
			last = j
		}
	}
	return n, last
}

// LastRaise is when phase's tier was last raised, or the zero time.
func (st State) LastRaise(phase string) time.Time {
	if ps := st.Phases[phase]; ps != nil && len(ps.Raises) > 0 {
		return ps.Raises[len(ps.Raises)-1].At
	}
	return time.Time{}
}

// RaiseTier records r, a step up of phase's tier, in st: the phase runs on r.To from now on (TierFor).
func RaiseTier(st *State, phase string, r TierRaise) {
	ps := st.Phases[phase]
	if ps == nil {
		ps = &PhaseState{Status: PhasePending}
		st.Phases[phase] = ps
	}
	ps.Raises = append(ps.Raises, r)
}
