package decide

import (
	"strings"
	"testing"
	"time"
)

// A block's question has Look again third, where the default sits (serial mode types 3 for it), and says
// when baton picks it.
func TestBlockedOptions(t *testing.T) {
	var labels []string
	for _, o := range BlockedOptions {
		labels = append(labels, o.Label)
	}
	if got := strings.Join(labels, "|"); got != "Continue|Pause baton|Look again" {
		t.Errorf("options %s", got)
	}
	if d := BlockedOptions[2].Description; !strings.Contains(d, "if nobody answers in 30 minutes") || !strings.Contains(d, "proposes it first") {
		t.Errorf("Look again says %q", d)
	}
}

// A block lifted for the model to look again names the phase and what blocked it, for the human and for
// the model, says whether nobody answered or the human chose it, and tells the model what to do: the
// independent parts first, a reversible way around it proposed before it is taken (its wait named),
// nothing irreversible, and blocking again only if there truly is no way.
func TestLookingAgain(t *testing.T) {
	rule := []string{"Look again for a safe, independent way to continue that completes the assignment without dropping work",
		"first the parts that do not need what blocked you, then a reversible way around it", "Nothing irreversible",
		`baton propose "<what you will do>" --because "<what blocked you>" --undo "<how to reverse it>"`,
		"(your --wait, or 10 minutes)", "if nobody answers, you go ahead", `baton blocked "<why>" --tried "<what you tried>"`}
	for _, c := range []struct {
		name                 string
		agent, reason, phase string
		auto                 bool
		say, tell            []string
	}{
		{"the lead's, nobody answered", "", "the signing key is gone.", "P3", true,
			[]string{"baton: nobody answered the block on P3 in 30 minutes, so baton lifted it → Claude looks again for a safe way on, and proposes it — the signing key is gone"},
			[]string{"[baton] You were blocked on P3 (the signing key is gone), and nobody has answered for 30 minutes, so baton lifted the block for the human: the run does not wait on them any longer."}},
		{"the lead's, the human chose it", "", "the signing key is gone", "P3", false,
			[]string{"baton: the block on P3 is lifted, as you chose → Claude looks again"},
			[]string{"[baton] The human chose Look again on your block on P3 (the signing key is gone)."}},
		{"the lead's with no phase in hand", "", "x", "", true, []string{"the block on the plan"}, []string{"You were blocked on the plan (x)"}},
		{"an agent's, nobody answered", "P4", "need a key", "P4", true,
			[]string{"baton: nobody answered the block of the agent on P4 in 30 minutes, so baton lifted it → the agent looks again"},
			[]string{"You were blocked on P4 (need a key), and nobody has answered for 30 minutes"}},
		{"an agent's, the human chose it", "P4", "need a key", "P4", false,
			[]string{"baton: the block of the agent on P4 is lifted, as you chose → the agent looks again"},
			[]string{"The human chose Look again on your block on P4 (need a key)."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			say, tell := LookingAgain(c.agent, c.reason, c.phase, 30*time.Minute, 10*time.Minute, c.auto)
			for _, w := range c.say {
				if !strings.Contains(say, w) {
					t.Errorf("say %q lacks %q", say, w)
				}
			}
			for _, w := range append(c.tell, rule...) {
				if !strings.Contains(tell, w) {
					t.Errorf("tell %q lacks %q", tell, w)
				}
			}
		})
	}
}
