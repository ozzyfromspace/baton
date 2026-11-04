package cli

import (
	"slices"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A tier restart's tier takes the place of the human's own --model and --effort on claude's command line,
// in either form, before any "--"; a part it leaves out keeps the human's.
func TestWithTier(t *testing.T) {
	for _, c := range []struct {
		name          string
		args          []string
		model, effort string
		want          []string
	}{
		{"none given", []string{"--permission-mode", "auto"}, "", "", []string{"--permission-mode", "auto"}},
		{"added", []string{"--permission-mode", "auto"}, "opus", "max", []string{"--permission-mode", "auto", "--model", "opus", "--effort", "max"}},
		{"in place of the human's", []string{"--model", "sonnet", "--effort=low", "--add-dir", "x"}, "opus", "high", []string{"--add-dir", "x", "--model", "opus", "--effort", "high"}},
		{"the effort alone", []string{"--model=sonnet", "--effort", "low"}, "", "xhigh", []string{"--model=sonnet", "--effort", "xhigh"}},
		{"before a prompt", []string{"--model", "sonnet", "--", "--model is a word here"}, "opus", "", []string{"--model", "opus", "--", "--model is a word here"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := withTier(c.args, c.model, c.effort); !slices.Equal(got, c.want) {
				t.Errorf("withTier(%q, %q, %q) = %q, want %q", c.args, c.model, c.effort, got, c.want)
			}
		})
	}
}

// The launch tier is the family of --model and the level of --effort, each "" when not given.
func TestLaunchTier(t *testing.T) {
	for _, c := range []struct {
		args []string
		want plan.Tier
	}{
		{[]string{"--model", "claude-opus-5-5", "--effort", "XHigh"}, plan.Tier{Model: "opus", Effort: "xhigh"}},
		{[]string{"--model=sonnet"}, plan.Tier{Model: "sonnet"}},
		{[]string{"--permission-mode", "auto"}, plan.Tier{}},
	} {
		if got := launchTier(c.args); got != c.want {
			t.Errorf("launchTier(%q) = %+v, want %+v", c.args, got, c.want)
		}
	}
}
