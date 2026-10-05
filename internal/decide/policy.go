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
