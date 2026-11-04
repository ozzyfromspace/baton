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
