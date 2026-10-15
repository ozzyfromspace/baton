package decide

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Outward reports an action that looks outward-facing or irreversible, and the words that make it look
// so. Such a proposal waits for the human like any other, but at its deadline baton answers Not now for
// them, not Go ahead: the model does it only if it is the safe kind (NotNowRule), which baton's word list
// cannot tell, and leaves it for them otherwise. A false match costs the model that judgment, never an
// act nobody approved.
func Outward(action string) (term string, outward bool) {
	if m := outwardWords.FindString(strings.ToLower(action)); m != "" {
		return m, true
	}
	return "", false
}

// outwardWords are the verbs of acts that reach outside the machine or cannot be taken back.
var outwardWords = regexp.MustCompile(`\b(` + strings.Join([]string{
	`force[- ]push(es|ed|ing)?`, `push(es|ed|ing)?`, `publish(es|ed|ing)?`, `deploy(s|ed|ing|ment)?`, `releas(e|es|ed|ing)`,
	`merg(e|es|ed|ing)`, `delet(e|es|ed|ing|ion)`, `drop(s|ped|ping)?`, `rm`, `reset\s+--hard`, `git\s+clean`,
	`destroy(s|ed|ing)?`, `wip(e|es|ed|ing)`, `purg(e|es|ed|ing)`, `revok(e|es|ed|ing)`,
	`sen(d|ds|ding|t)`, `e-?mail(s|ed|ing)?`, `upload(s|ed|ing)?`,
	`pa(y|ys|id|ying|yment|yments)`, `charg(e|es|ed|ing)`, `refund(s|ed|ing)?`,
	`terraform\s+apply`, `kubectl`, `prod(uction)?`,
}, "|") + `)\b|--force\b`)

// OutwardReason says why a proposal is answered Not now at its deadline, for its question.
func OutwardReason(term string) string {
	return fmt.Sprintf("'%s' looks outward-facing or irreversible", term)
}

// The model sizes each proposal's wait by how small and reversible its action is: from ProposalWaitMin,
// for a trivial one, to ProposalWaitMax, for one the human would most want to see. A short wait keeps the
// run moving; none waits forever.
const (
	ProposalWaitMin = 5 * time.Minute
	ProposalWaitMax = 20 * time.Minute
)

// ProposalWait is the wait of a proposal whose model asked for given (--wait; "" for none): given, from
// ProposalWaitMin to ProposalWaitMax; otherwise fallback (escalation_timeout), and why the model's was not
// used ("" when it named none). Nothing is refused for it: the proposal goes on with the wait baton chose.
func ProposalWait(given string, fallback time.Duration) (wait time.Duration, why string) {
	if strings.TrimSpace(given) == "" {
		return fallback, ""
	}
	d, err := time.ParseDuration(strings.TrimSpace(given))
	switch {
	case err != nil:
		return fallback, fmt.Sprintf("--wait %q is not a duration such as 5m", given)
	case d < ProposalWaitMin || d > ProposalWaitMax:
		return fallback, fmt.Sprintf("--wait %s is outside %s to %s", given, Spell(ProposalWaitMin), Spell(ProposalWaitMax))
	}
	return d, ""
}

// WaitAdvice tells the model how to size a proposal's wait. baton keeps to the bounds; the size is the
// model's call, so it is told what a wait costs.
func WaitAdvice() string {
	return fmt.Sprintf("Give the shortest wait that fits, with --wait from %s to %s: every minute a proposal waits is implementation time the run loses. "+
		"%s for most; longer only for an action the human would most want to catch before it happens.", Spell(ProposalWaitMin), Spell(ProposalWaitMax), Spell(ProposalWaitMin))
}

// WaitUsed says which wait a proposal got (wait), and why it is not the one the model asked for (why).
func WaitUsed(wait time.Duration, why string) string {
	if why == "" {
		return "It waits " + Spell(wait) + "."
	}
	return fmt.Sprintf("It waits %s, baton's default, since %s. Next time: %s", Spell(wait), why, WaitAdvice())
}

// CapReached reports that a phase has made as many decisions without the human as max allows (0: no
// limit), so baton stops for them to review those decisions before anything else.
func CapReached(unattended, max int) bool { return max > 0 && unattended >= max }

// Spell says a duration the way people do: "5 minutes", "1 minute", "45s".
func Spell(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "1 minute"
	case d > time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", d/time.Minute)
	default:
		return d.String()
	}
}

// UncommittedRefusal is why blocked or done turns down work the phase left uncommitted, and the way past
// it, in the wording proven in the S2 replay: commit, degraded if need be, and never lose work to get a
// clean tree. It is only ever said in a git repository. files lists the paths, one per line.
func UncommittedRefusal(command, phase, files string) string {
	advice := "Commit it first, degraded if need be (e.g. --no-gpg-sign if signing fails), and if you degraded it, " +
		"run baton note \"<what you did>\" --undo \"<how to repair it>\" so the human can. " +
		"Never discard, stash, reset or unstage work to get a clean tree."
	if command == "done" {
		return fmt.Sprintf("not done — %s leaves uncommitted work that was not there when it started:\n%s\n%s If it truly cannot be committed, say why: baton done %s --keep-dirty \"<why>\"",
			phase, files, advice, phase)
	}
	return fmt.Sprintf("not recorded — %s leaves uncommitted work that was not there when it started, and a blocked run can sit for hours:\n%s\n%s "+
		"If it truly cannot be committed, say why: baton blocked \"<why>\" --tried \"<what you tried>\" --keep-dirty \"<why it stays uncommitted>\"", phase, files, advice)
}
