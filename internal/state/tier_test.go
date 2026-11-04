package state

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A phase runs on its plan's tier, or the last step baton raised it to, with its model kept among the
// families the human allows: a phase naming Fable runs on Opus unless Fable is allowed.
func TestTierFor(t *testing.T) {
	p := plan.Plan{Phases: []plan.Phase{
		{ID: "P1", Model: "opus", Effort: "high"},
		{ID: "P2", Model: "fable", Effort: "max"},
		{ID: "P3", Effort: "low"},
		{ID: "P4"},
	}}
	std := []string{"haiku", "sonnet", "opus"}
	st := New()
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	st.Phases["P1"] = &PhaseState{Status: PhaseActive, Raises: []TierRaise{
		{From: plan.Tier{Model: "opus", Effort: "high"}, To: plan.Tier{Model: "opus", Effort: "xhigh"}, Why: "3 red runs", At: at},
		{From: plan.Tier{Model: "opus", Effort: "xhigh"}, To: plan.Tier{Model: "opus", Effort: "max"}, Why: "3 more", At: at},
	}}
	for _, c := range []struct {
		phase   string
		allowed []string
		want    plan.Tier
	}{
		{"P1", std, plan.Tier{Model: "opus", Effort: "max"}},
		{"P2", std, plan.Tier{Model: "opus", Effort: "max"}},
		{"P2", append(std, "fable"), plan.Tier{Model: "fable", Effort: "max"}},
		{"P3", std, plan.Tier{Effort: "low"}},
		{"P4", std, plan.Tier{}},
		{"P9", std, plan.Tier{}},
		{"P2", nil, plan.Tier{Model: "fable", Effort: "max"}},
	} {
		if got := TierFor(st, p, c.phase, c.allowed); got != c.want {
			t.Errorf("%s with %v: %s, want %s", c.phase, c.allowed, got, c.want)
		}
	}
	if r := st.Raised(p, "P1"); r == nil || *r != (plan.Tier{Model: "opus", Effort: "max"}) || st.Raised(p, "P2") != nil {
		t.Errorf("raised: %v %v", r, st.Raised(p, "P2"))
	}
}

// A model the human does not allow becomes the highest allowed below it, or the lowest above.
func TestAllowedModel(t *testing.T) {
	for _, c := range []struct {
		model   string
		allowed []string
		want    string
	}{
		{"fable", []string{"haiku", "sonnet", "opus"}, "opus"},
		{"opus", []string{"haiku", "sonnet"}, "sonnet"},
		{"haiku", []string{"opus"}, "opus"},
		{"sonnet", []string{"haiku", "opus"}, "haiku"},
		{"opus", []string{"opus"}, "opus"},
		{"", []string{"opus"}, ""},
	} {
		if got := AllowedModel(c.model, c.allowed); got != c.want {
			t.Errorf("AllowedModel(%q, %v) = %q, want %q", c.model, c.allowed, got, c.want)
		}
	}
}

// A served tier fits the one wanted when it has every part that one names; a part not known yet does not.
func TestFits(t *testing.T) {
	r := Runtime{}
	SawTier(&r, "claude-opus-5-5", "")
	SawTier(&r, "", "xhigh")
	if got := r.Served(); got != (plan.Tier{Model: "opus", Effort: "xhigh"}) {
		t.Fatalf("served %s", got)
	}
	for _, c := range []struct {
		want, served plan.Tier
		fits         bool
	}{
		{plan.Tier{Model: "opus", Effort: "xhigh"}, plan.Tier{Model: "opus", Effort: "xhigh"}, true},
		{plan.Tier{Effort: "xhigh"}, plan.Tier{Model: "sonnet", Effort: "xhigh"}, true},
		{plan.Tier{Model: "opus", Effort: "max"}, plan.Tier{Model: "opus", Effort: "xhigh"}, false},
		{plan.Tier{Model: "opus"}, plan.Tier{Effort: "high"}, false},
		{plan.Tier{}, plan.Tier{}, true},
	} {
		if got := Fits(c.want, c.served); got != c.fits {
			t.Errorf("Fits(%s, %s) = %v", c.want, c.served, got)
		}
	}
}

// In a run with phase agents, the lead passes over a phase whose tier it does not run on, for an agent to
// take; a phase on its tier, one an agent failed on, and every phase of a run with no agents, are the
// lead's.
func TestTheLeadPassesOverAPhaseOnAnotherTier(t *testing.T) {
	p := plan.Plan{Phases: []plan.Phase{
		{ID: "P1", Model: "opus", Effort: "max", After: []string{}},
		{ID: "P2", Model: "opus", Effort: "high", After: []string{}},
	}}
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	agents := func() State {
		st := New()
		st.Mode, st.RunBranch = ModeRunning, "baton/run"
		st.Run.Fleet = "inst"
		st.Run.Launched = plan.Tier{Model: "opus", Effort: "high"}
		return st
	}
	if st := agents(); Next(st, p) != "P2" || NextWorker(st, p) != "P1" {
		t.Errorf("with agents: the lead takes %q, an agent %q", Next(st, p), NextWorker(st, p))
	}
	st := agents()
	st.Workers = []Worker{{Phase: "P1", Status: WorkerFailed, Assigned: at}}
	if Next(st, p) != "P1" {
		t.Errorf("a phase an agent failed on went to %q", Next(st, p))
	}
	st = agents()
	st.Run.Fleet = ""
	if Next(st, p) != "P1" {
		t.Errorf("with no agents the lead takes %q", Next(st, p))
	}
	st = agents()
	st.Run.Launched = plan.Tier{}
	if Next(st, p) != "P1" {
		t.Errorf("a lead nothing has said of takes %q", Next(st, p))
	}
	st = agents()
	st.Run.Launched, st.Run.Model, st.Run.Effort = plan.Tier{}, "claude-opus-5-5", "max"
	if Next(st, p) != "P1" || !st.LeadFits(p, "P1") || st.LeadFits(p, "P2") {
		t.Errorf("a lead served opus/max takes %q", Next(st, p))
	}
}

// RedRuns counts a phase's own gate failing since a time: never an await, a run that timed out, a whole run
// its environment took a stage of, or one whose environment has not been read yet.
func TestRedRuns(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	red := func(id string, min int) Job {
		return Job{ID: id, Phase: "P1", Kind: JobVerify, Command: "go test ./...", Status: JobFailed, Ended: at.Add(time.Duration(min) * time.Minute), EnvRead: at.Add(time.Duration(min) * time.Minute)}
	}
	st := New()
	st.Jobs = []Job{
		red("j1", 1), red("j2", 2),
		{ID: "j3", Phase: "P1", Kind: JobVerify, Status: JobTimeout, Ended: at.Add(3 * time.Minute)},
		{ID: "j4", Phase: "P1", Kind: JobAwait, Status: JobFailed, Ended: at.Add(4 * time.Minute)},
		{ID: "j5", Phase: "P1", Kind: JobVerify, Status: JobFailed, Ended: at.Add(5 * time.Minute)}, // its environment not read yet
		func() Job { j := red("j6", 6); j.EnvLost = []EnvLoss{{Stage: "build", Kind: "oom"}}; return j }(),
		{ID: "j7", Phase: "P1", Kind: JobVerify, Status: JobFailed, Reruns: "j2", Ended: at.Add(7 * time.Minute)}, // a rerun: no environment to read
		red("j8", 8),
		{ID: "j9", Phase: "P2", Kind: JobVerify, Status: JobFailed, Ended: at.Add(9 * time.Minute), EnvRead: at},
		{ID: "j10", Phase: "P1", Kind: JobVerify, Status: JobPassed, Ended: at.Add(10 * time.Minute)},
	}
	n, last := RedRuns(st, "P1", time.Time{})
	if n != 4 || last == nil || last.ID != "j8" {
		t.Fatalf("since ever: %d, last %+v", n, last)
	}
	if n, last := RedRuns(st, "P1", at.Add(2*time.Minute)); n != 2 || last.ID != "j8" {
		t.Errorf("since the second: %d, last %+v", n, last)
	}
	RaiseTier(&st, "P1", TierRaise{From: plan.Tier{Model: "opus", Effort: "high"}, To: plan.Tier{Model: "opus", Effort: "xhigh"}, At: at.Add(8 * time.Minute)})
	if got := st.LastRaise("P1"); !got.Equal(at.Add(8 * time.Minute)) {
		t.Errorf("last raise %v", got)
	}
	if n, _ := RedRuns(st, "P1", st.LastRaise("P1")); n != 0 {
		t.Errorf("after the raise: %d", n)
	}
}

// The plan sets each phase's tier and baton only raises it: a raise stands while the plan names the tier it
// was raised from, and once the human writes another tier into the plan, that tier is the phase's, and its
// steps count again. A raise recorded before v0.8.19 names no plan tier, and stands.
func TestARaiseStandsUntilThePlanChangesTheTier(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	high, xhigh, max := plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "xhigh"}, plan.Tier{Model: "opus", Effort: "max"}
	on := func(t plan.Tier) plan.Plan {
		return plan.Plan{Phases: []plan.Phase{{ID: "P1", Model: t.Model, Effort: t.Effort}}}
	}
	st := New()
	st.Phases["P1"] = &PhaseState{Status: PhaseActive}
	RaiseTier(&st, "P1", TierRaise{From: high, To: xhigh, Plan: high, Why: "3 red runs", At: at})
	if got := TierFor(st, on(high), "P1", nil); got != xhigh || len(Raises(st, on(high), "P1")) != 1 {
		t.Errorf("raised under the same plan tier: %s", got)
	}
	if got := TierFor(st, on(max), "P1", nil); got != max || Raises(st, on(max), "P1") != nil || st.Raised(on(max), "P1") != nil {
		t.Errorf("after the human wrote max into the plan: %s", got)
	}
	medium := plan.Tier{Model: "opus", Effort: "medium"}
	if got := TierFor(st, on(medium), "P1", nil); got != medium {
		t.Errorf("after the human lowered it in the plan: %s", got)
	}
	RaiseTier(&st, "P1", TierRaise{From: medium, To: high, Plan: medium, At: at.Add(time.Minute)})
	if rs := Raises(st, on(medium), "P1"); len(rs) != 1 || TierFor(st, on(medium), "P1", nil) != high {
		t.Errorf("a raise under the new plan tier: %+v", rs)
	}
	old := New()
	old.Phases["P1"] = &PhaseState{Status: PhaseActive, Raises: []TierRaise{{From: high, To: xhigh, At: at}}}
	if got := TierFor(old, on(max), "P1", nil); got != xhigh {
		t.Errorf("a raise from before v0.8.19: %s", got)
	}
}

// A change of the plan's text that changes phases' tiers lists each phase not done whose tier changed,
// with where it stands: the lead's (and whether the lead moves), a phase agent's (and whether it is
// replaced: never one spawned on no tier), started and neither, or not started.
func TestTierChanges(t *testing.T) {
	high, xhigh := plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "xhigh"}
	mk := func(t plan.Tier) plan.Plan {
		var p plan.Plan
		for _, id := range []string{"P0", "P1", "P2", "P3", "P4", "P5", "P6"} {
			p.Phases = append(p.Phases, plan.Phase{ID: id, Model: t.Model, Effort: t.Effort, After: []string{}})
		}
		return p
	}
	was, now := mk(high), mk(xhigh)
	now.Phases[6].Effort = "high" // unchanged
	st := New()
	st.Mode, st.Current = ModeRunning, "P1"
	st.Run.Launched = high
	st.Phases["P0"] = &PhaseState{Status: PhaseDone}
	st.Phases["P1"] = &PhaseState{Status: PhaseActive}
	st.Phases["P2"] = &PhaseState{Status: PhaseWorking}
	st.Phases["P3"] = &PhaseState{Status: PhaseWorking}
	st.Phases["P4"] = &PhaseState{Status: PhaseVerifying}
	st.Workers = []Worker{{Phase: "P2", Status: WorkerWorking, Tier: high}, {Phase: "P3", Status: WorkerWorking}}
	got := TierChanges(st, was, now)
	want := []TierChange{
		{Phase: "P1", From: high, To: xhigh, Held: HeldByLead, Moves: true},
		{Phase: "P2", From: high, To: xhigh, Held: HeldByAgent, Moves: true},
		{Phase: "P3", From: high, To: xhigh, Held: HeldByAgent},
		{Phase: "P4", From: high, To: xhigh, Held: HeldStarted},
		{Phase: "P5", From: high, To: xhigh},
	}
	if len(got) != len(want) {
		t.Fatalf("changes %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	st.Run.Launched = xhigh
	if c := TierChanges(st, was, now)[0]; c.Phase != "P1" || c.Moves {
		t.Errorf("a lead on the new tier already moves: %+v", c)
	}
	if c := TierChanges(st, was, was); c != nil {
		t.Errorf("no change: %+v", c)
	}
}
