package plan

import (
	"fmt"
	"slices"
	"strings"
)

// A phase's tier is the model and the effort it runs on, from the Model: and Effort: lines under its
// heading, beside its After:, Type: and Replan: lines:
//
//	Model: opus
//	Effort: xhigh
//
// The model is an alias of a model family, which Claude Code resolves to the family's newest model: the
// only form the Agent tool takes for a phase agent (docs/research/tiers.md). The effort is one of Claude
// Code's levels. A phase with neither line runs as phases always have: a phase agent on the lead's model
// and effort, the lead on its own.

// Models are the model families a phase may name, cheapest first: the order escalation climbs (Raise).
var Models = []string{"haiku", "sonnet", "opus", "fable"}

// Efforts are Claude Code's effort levels, least first: the order escalation climbs (Raise).
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Tier is the model and effort a phase runs on. Either may be "": the phase leaves it to the session.
type Tier struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Zero reports a tier that sets nothing.
func (t Tier) Zero() bool { return t.Model == "" && t.Effort == "" }

// String is the tier as baton writes it, "opus/high"; a part it leaves to the session reads "session".
func (t Tier) String() string {
	if t.Zero() {
		return "the session's"
	}
	m, e := t.Model, t.Effort
	if m == "" {
		m = "session"
	}
	if e == "" {
		e = "session"
	}
	return m + "/" + e
}

// Tier is the phase's tier, as its Model: and Effort: lines say.
func (ph Phase) Tier() Tier { return Tier{Model: ph.Model, Effort: ph.Effort} }

// TierOf is the tier of phase id in p, or the zero tier for a phase p does not have.
func (p Plan) TierOf(id string) Tier {
	if i := p.Index(id); i >= 0 {
		return p.Phases[i].Tier()
	}
	return Tier{}
}

// Tiered reports a plan any phase of which names a model or an effort.
func (p Plan) Tiered() bool {
	return slices.ContainsFunc(p.Phases, func(ph Phase) bool { return !ph.Tier().Zero() })
}

// Untiered lists the phases of p that name no model or no effort, in plan order.
func (p Plan) Untiered() []string {
	var out []string
	for _, ph := range p.Phases {
		if ph.Model == "" || ph.Effort == "" {
			out = append(out, ph.ID)
		}
	}
	return out
}

// Raise is the tier one step above t, climbing within allowed (the families the human allows, a subset of
// Models), or false when t is at the top. Its effort goes up first, one level at a time, as far as xhigh;
// from there its model goes up to the next allowed family, at the same effort; max comes last, on the
// highest family allowed. So sonnet/medium climbs to sonnet/high, sonnet/xhigh, opus/xhigh, opus/max: a
// harder phase gets a stronger model before the dearest effort of a weaker one (Sonnet 5.5's max costs
// 5.5 times its high per token, docs/research/tiers.md). A tier that leaves a part to the session is
// raised from base, the tier the session runs on; a part neither says is not raised.
func Raise(t, base Tier, allowed []string) (Tier, bool) {
	if t.Model == "" {
		t.Model = base.Model
	}
	if t.Effort == "" {
		t.Effort = base.Effort
	}
	e, top := slices.Index(Efforts, t.Effort), slices.Index(Efforts, "xhigh")
	if e >= 0 && e < top {
		t.Effort = Efforts[e+1]
		return t, true
	}
	if m := slices.Index(Models, t.Model); m >= 0 {
		for _, next := range Models[m+1:] {
			if slices.Contains(allowed, next) {
				t.Model = next
				return t, true
			}
		}
	}
	if e >= 0 && e < len(Efforts)-1 {
		t.Effort = Efforts[e+1]
		return t, true
	}
	return t, false
}

// Family is the model family a model names: an alias as it is ("opus"), or a full id or a display name
// that names one ("claude-opus-5-5", "Opus 5.5"). "" when it names none of Models.
func Family(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if slices.Contains(Models, m) {
		return m
	}
	for _, f := range Models {
		if m == "claude-"+f || strings.HasPrefix(m, "claude-"+f+"-") || strings.HasPrefix(m, f+" ") || strings.HasPrefix(m, f+"-") {
			return f
		}
	}
	return ""
}

// parseModelLine reads a `Model:` line (`**Model:**` too): ok says it is one. A full model id or a display
// name reads as its family (Family), since a phase agent can only be given a family.
func parseModelLine(line string) (model string, ok bool, err error) {
	label, rest, found := strings.Cut(line, ":")
	if !found || !strings.EqualFold(strings.Trim(label, "*_` \t"), "model") {
		return "", false, nil
	}
	v := strings.TrimSuffix(strings.Trim(rest, "*` \t"), ".")
	if f := Family(v); f != "" {
		return f, true, nil
	}
	return "", true, fmt.Errorf("Model: %q is not a model baton can give a phase: write one of %s, or leave the line out for the session's model",
		strings.TrimSpace(rest), strings.Join(Models, ", "))
}

// parseEffortLine reads an `Effort:` line (`**Effort:**` too): ok says it is one. "med" reads as medium,
// "ultracode" as xhigh, as Claude Code reads them.
func parseEffortLine(line string) (effort string, ok bool, err error) {
	label, rest, found := strings.Cut(line, ":")
	if !found || !strings.EqualFold(strings.Trim(label, "*_` \t"), "effort") {
		return "", false, nil
	}
	v := strings.ToLower(strings.TrimSuffix(strings.Trim(rest, "*` \t"), "."))
	switch v {
	case "med":
		v = "medium"
	case "ultracode", "x-high", "extra high":
		v = "xhigh"
	}
	if slices.Contains(Efforts, v) {
		return v, true, nil
	}
	return "", true, fmt.Errorf("Effort: %q is not an effort level: write one of %s, or leave the line out for the session's effort",
		strings.TrimSpace(rest), strings.Join(Efforts, ", "))
}
