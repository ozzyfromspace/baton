package decide

import (
	"strings"
	"testing"
)

func TestPlanMoved(t *testing.T) {
	got := PlanMoved("/home/h/.claude/plans/humming-whistling-hare.md", "/home/h/.claude/plans/the-next-campaign.md")
	for _, want := range []string{"moved the plan to /home/h/.claude/plans/the-next-campaign.md (it was humming-whistling-hare.md)", "after every compaction", "edit it there"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}
