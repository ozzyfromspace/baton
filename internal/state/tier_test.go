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
	if r := st.Raised("P1"); r == nil || *r != (plan.Tier{Model: "opus", Effort: "max"}) || st.Raised("P2") != nil {
		t.Errorf("raised: %v %v", r, st.Raised("P2"))
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
