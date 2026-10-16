//go:build !windows

package e2e

import (
	"strings"
	"testing"
	"time"
)

// chainPlan is a chain, as a plan with no After: lines is: each phase follows the one before it. P1 and
// P2 plainly need nothing from each other: each writes a file of its own.
const chainPlan = `# Chain plan

## P0 — Say hello
Run echo hello, then report P0 done.

## P1 — Write alpha
Write the single word alpha into a new file named alpha.txt, commit it, and report P1 done.

## P2 — Write beta
Write the single word beta into a new file named beta.txt, commit it, and report P2 done.
`

// The human's /baton replan: the lead's model restructures the phases not started, as the human's words
// say, in the draft baton gives it, and hands it back; baton checks the draft and follows it. The plan
// file then has P2 after P1 no more, so P1 and P2 could run at once.
func TestAReplanShortensTheChain(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, chainPlan)
	s := Start(t, dir, "--model", "haiku")
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	s.Type("/baton replan P1 and P2 need nothing from each other: each writes a file of its own, so both can follow P0 directly")
	waitSequence(t, s, 6*time.Minute,
		is("replan_opened", "by", "human"),
		is("plan_replanned", "by", "human"),
	)
	var ev map[string]any
	for _, e := range s.Events() {
		if e["kind"] == "plan_replanned" {
			ev = e
		}
	}
	before, _ := ev["before"].(map[string]any)
	after, _ := ev["after"].(map[string]any)
	// The shapes are of the phases not started, P1 and P2: P0 is the phase in hand.
	if before["longest_chain"] != 2.0 || after["longest_chain"] != 1.0 || after["width"] != 2.0 {
		t.Errorf("the shape before %v, after %v", before, after)
	}
	_, p2, _ := strings.Cut(readFile(dir, "plan.md"), "## P2")
	if head, _, _ := strings.Cut(p2, "\n\n"); strings.Contains(head, "P1") {
		t.Errorf("P2's head after the replan: %q", head)
	}
	none(t, s, "replan_refused", "replan_lapsed")
}
