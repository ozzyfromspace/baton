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
	// Plan is the tier the phase's plan named when it was raised: the raise stands while the plan names it
	// still (Raises). Zero in a raise recorded before v0.8.19, which stands whatever the plan names.
	Plan plan.Tier `json:"plan,omitzero"`
	// Why is what raised it, in words; Reds the red runs of the phase's own gate counted then.
	Why  string    `json:"why"`
	Reds int       `json:"reds,omitempty"`
	At   time.Time `json:"at"`
}

// Raises are the steps baton raised phase that stand in run st of plan p: the latest of them, back to the
// first since the plan's tier for it last changed. The plan sets each phase's tier and baton only raises
// it, so a tier the human writes into the plan after a raise is the phase's, and the raises before it no
// longer stand (nor count against its steps).
func Raises(st State, p plan.Plan, phase string) []TierRaise {
	ps := st.Phases[phase]
	if ps == nil {
		return nil
	}
	now := p.TierOf(phase)
	i := len(ps.Raises)
	for i > 0 && (ps.Raises[i-1].Plan.Zero() || ps.Raises[i-1].Plan == now) {
		i--
	}
	if i == len(ps.Raises) {
		return nil
	}
	return ps.Raises[i:]
}

// LastRaised is the latest step baton raised phase that stands in run st of plan p (Raises), or nil.
func LastRaised(st State, p plan.Plan, phase string) *TierRaise {
	if rs := Raises(st, p, phase); len(rs) > 0 {
		r := rs[len(rs)-1]
		return &r
	}
	return nil
}

// TierFor is the tier phase runs on in run st of plan p: its plan's, or the last step baton raised it to
// that stands (Raises), with its model kept among allowed (AllowedModel). A part the plan leaves to the
// session stays "".
func TierFor(st State, p plan.Plan, phase string, allowed []string) plan.Tier {
	t := p.TierOf(phase)
	if r := LastRaised(st, p, phase); r != nil {
		t = r.To
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

// Raised is the tier phase of plan p was last raised to, while that raise stands (Raises), or nil while it
// runs on its plan's.
func (st State) Raised(p plan.Plan, phase string) *plan.Tier {
	if r := LastRaised(st, p, phase); r != nil {
		return &r.To
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
		if j.Phase != phase || j.kind() != JobVerify || j.Status != JobFailed || !j.Ended.After(since) || len(j.EnvLost) > 0 ||
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

// TierChange is a phase whose tier a change of its plan's text changed: From what it ran on, To what it runs
// on now (TierFor), and where it stands, Held: HeldByLead (the lead holds it or is about to start it),
// HeldByAgent (a phase agent is at work on it), HeldStarted (started, and neither: its gate runs, say), or ""
// (not started). Moves says the loop that holds it runs on another tier, and moves onto To (the lead
// restarts, the agent is replaced).
type TierChange struct {
	Phase    string
	From, To plan.Tier
	Held     string
	Moves    bool
}

// Where a phase whose tier changed stands (TierChange.Held).
const (
	HeldByLead  = "lead"
	HeldByAgent = "agent"
	HeldStarted = "started"
)

// TierChanges lists the phases of run st not done whose tier changes as the run follows plan now in place
// of plan was (plan.Follow), in now's order: the plan sets each phase's tier, so a change the human writes
// into the plan file reaches a phase under way too, up or down.
func TierChanges(st State, was, now plan.Plan) []TierChange {
	var out []TierChange
	for _, ph := range now.Phases {
		ps := st.Phases[ph.ID]
		if ps != nil && ps.Status == PhaseDone || was.Index(ph.ID) < 0 {
			continue // done, or one the text adds, which starts on its tier
		}
		from, to := TierFor(st, was, ph.ID, st.Run.Models), TierFor(st, now, ph.ID, st.Run.Models)
		if from == to {
			continue
		}
		c := TierChange{Phase: ph.ID, From: from, To: to}
		w := slices.IndexFunc(st.Workers, func(w Worker) bool { return w.Phase == ph.ID && w.Status == WorkerWorking })
		switch {
		case st.Current == ph.ID && st.Mode == ModeRunning && (st.InHand() || st.BoundaryOwed):
			c.Held = HeldByLead
			c.Moves = !to.Zero() && !Fits(to, st.LeadRuns())
		case w >= 0:
			c.Held = HeldByAgent // replaced as the coordinator's retier would: not one spawned on no tier
			c.Moves = !to.Zero() && !st.Workers[w].Tier.Zero() && !Fits(to, st.Workers[w].Tier)
		case ps != nil && ps.Status != PhasePending:
			c.Held = HeldStarted
		}
		out = append(out, c)
	}
	return out
}

// Fields are c's fields as tier_changed logs them, by the plan.
func (c TierChange) Fields() map[string]any {
	f := map[string]any{"phase": c.Phase, "from": c.From.String(), "to": c.To.String(), "by": "plan"}
	if c.Held != "" {
		f["held"], f["moves"] = c.Held, c.Moves
	}
	return f
}
