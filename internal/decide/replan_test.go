package decide

import (
	"fmt"
	"testing"
)

func TestPlanChangeWords(t *testing.T) {
	const told = "[baton] The human changed the plan while you work on P3, and baton follows the new text. Below is %s as the plan reads now: " +
		"follow it from here on. The commits you made stay as they are: where one no longer fits, change it with a new commit, and say so in your notes."
	rules, section := "## Standing rules\n\n- Never push.\n", "## P3 — Gates\n\nAfter: P1\n\nRun make e2e too."
	for _, c := range []struct {
		name, got, want string
	}{
		{"its section changed", PlanChanged("P3", "", section), fmt.Sprintf(told, "P3's section") + "\n\n" + section},
		{"the rules changed", PlanChanged("P3", rules, ""), fmt.Sprintf(told, "the standing rules") + "\n\n## Standing rules\n\n- Never push."},
		{"both changed", PlanChanged("P3", rules, section),
			fmt.Sprintf(told, "the standing rules and P3's section") + "\n\n## Standing rules\n\n- Never push.\n\n" + section},
		{"the human told of its section", PlanTold("P3", false, true), "baton: the plan changed → the agent on P3 is told P3's section as it reads now"},
		{"the human told of both", PlanTold("P3", true, true), "baton: the plan changed → the agent on P3 is told the standing rules and P3's section as it reads now"},
		{"the human told of the rules", PlanTold("P3", true, false), "baton: the plan changed → the agent on P3 is told the standing rules as it reads now"},
		{"an idle agent woken for it", AgentMail([]string{"plan"}),
			"[baton] The human changed the plan: baton's message has the new text for you. End this turn, and baton gives it to you then (or with your next tool call, if you make one first)."},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}
