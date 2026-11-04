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
