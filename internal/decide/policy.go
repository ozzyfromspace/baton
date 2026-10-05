package decide

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Outward reports an action that looks outward-facing or irreversible, and the words that make it look
// so. Such a proposal gets no timer: it waits for the human like a hard block. The model chose the action,
// and baton only withholds the timer. A false match costs a wait, never an act nobody approved.
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

// UntimedReason says why a proposal has no timer, for its question.
func UntimedReason(term string) string {
	return fmt.Sprintf("'%s' looks outward-facing or irreversible", term)
}

// CapReached reports that a phase has made as many decisions without the human as max allows (0: no
// limit), so baton stops for them to review those decisions before anything else.
func CapReached(unattended, max int) bool { return max > 0 && unattended >= max }

// Playbook is what a refusal says instead of only "no": how to keep the run moving, and the lightest
// way to reach the human that fits (the wording held up in the S2 replay, docs/research/escalation.md).
// Without git there is no "uncommitted work?" line: baton never asks for a commit there.
func Playbook(git bool, timeout time.Duration) string {
	var b strings.Builder
	b.WriteString("A run must not stop for something it can work around. Before hard-blocking:\n")
	if git {
		b.WriteString("  · uncommitted work?   commit it, degraded if need be (e.g. --no-gpg-sign); never discard, stash, reset or unstage work\n")
	}
	b.WriteString("  · a step failed?      is there a worse-but-working version? do that\n" +
		"  · a rule or convention of the plan can't be followed right now (a tool missing, a service down)?\n" +
		"                        that is what baton propose is for: the human gets a deadline to veto your way around it\n" +
		"Then pick the lightest status that fits:\n" +
		"  · baton note \"<what you did>\" --undo \"<how to reverse it>\"\n" +
		"        you already took a reversible path, and the human should know. The run continues.\n" +
		"  · baton propose \"<what you will do to keep the plan moving>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"\n")
	fmt.Fprintf(&b, "        a human might choose differently: baton puts it to them with a deadline (%s), and no answer means you do it.\n", Spell(timeout))
	b.WriteString("        A proposal is an action that makes progress. Waiting, doing nothing or stopping is not a proposal.\n" +
		"  · baton blocked \"<why>\" --tried \"<what you tried>\"\n" +
		"        only if every way forward is irreversible, or needs a secret only the human holds. The run stops until they answer.")
	return b.String()
}

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
