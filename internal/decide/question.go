// Package decide is baton's policy for a run that must not stop for something it can work around
// (docs/design.md): which way of reaching the human fits, the questions baton puts to them, and the
// words the model reads about all of it. It lives in one place so that the CLI, the hooks and the brief
// say the same thing, and so that no text meant for a project without git mentions committing.
package decide

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Option is one answer a question of baton's offers.
type Option struct{ Label, Description string }

// Question is a question baton tells the model to put to the human, word for word. It counts as baton's
// own only in exactly that shape: one question, the same text (whitespace aside), the same labels in the
// same order, single-select.
type Question struct {
	Text    string
	Options []Option
}

// Call spells out the AskUserQuestion call that puts q to the human.
func (q Question) Call() string {
	return fmt.Sprintf("call the AskUserQuestion tool with exactly one question, %q (header \"baton\"), and exactly %d options, in this order: %s; not multi-select",
		q.Text, len(q.Options), optionList(q.Options))
}

// optionList spells out options for the model: "A" (description: "…"), "B" (description: "…").
func optionList(opts []Option) string {
	var parts []string
	for _, o := range opts {
		parts = append(parts, fmt.Sprintf("%q (description: %q)", o.Label, o.Description))
	}
	return strings.Join(parts, ", ")
}

// EscalationOptions are the answers to an escalation. The default, Continue, is third, as in every
// question of baton's that has one: where baton's mod does not run, the host answers it by typing 3 at its
// deadline, which in a permission prompt is "No" (docs/research/escalation.md). Wait for me holds the run
// for the human, and lapses like any hold (HoldLapse).
var EscalationOptions = []Option{
	{"Pause baton", "I'll take it from here"},
	{"Wait for me", "Hold the run until I write in the session; with no word for " + Spell(HoldLapse) + ", it carries on"},
	{"Continue", "I've handled it; carry on with the plan"},
}

// ReviewOptions are the answers to a review of the decisions a phase made without the human.
var ReviewOptions = []Option{
	{"Continue", "I've seen them; let the run go on"},
	{"Pause baton", "I'll take it from here"},
}

// MinWait is the least time one of baton's questions is on screen before baton answers it for the human:
// after a sleep, or once a dialog in front of it goes, they get that long again.
const MinWait = time.Minute

// WarnOptions are the context question's answers. The default, Keep going, is third on purpose: baton
// types 3 when nobody answers, and in a permission prompt 3 is "No" (docs/research/escalation.md).
var WarnOptions = []Option{
	{"Checkpoint now", "Finish the current step, save notes and compact; this phase continues from the notes"},
	{"Pause baton", "I'm taking over; baton stops driving the plan"},
	{"Keep going", "Carry on; Claude Code compacts on its own when the context is full"},
}

// ProposalOptions are the answers to a proposal. Go ahead is third for the same reason as Keep going.
var ProposalOptions = []Option{
	{"Wait for me", "Don't do it yet; I'll answer in the session, or it goes ahead after " + Spell(HoldLapse) + " with no word"},
	{"Pause baton", "I'm taking over; baton stops driving the plan"},
	{"Go ahead", "Do what you proposed"},
}

// OutwardOptions are the answers to an outward proposal (Outward). Not now, what baton answers when
// nobody does, is third for the same reason as Keep going; Go ahead, which only the human gives it, last.
var OutwardOptions = []Option{
	{"Wait for me", "Don't do it yet; I'll answer in the session, or it is answered Not now after " + Spell(HoldLapse) + " with no word"},
	{"Pause baton", "I'm taking over; baton stops driving the plan"},
	{state.NotNow, notNowOption},
	{"Go ahead", "Do what you proposed"},
}

// notNowOption is what Not now does, as its option says it.
const notNowOption = "Do it only if it is safely reversible and stays in the project; otherwise leave it for me"

// OptionsOf are the answers to proposal p.
func OptionsOf(p state.Decision) []Option {
	if p.Outward != "" {
		return OutwardOptions
	}
	return ProposalOptions
}

// NoPhase turns down a proposal or a block of the lead's (verb: "not recorded") while it holds no phase:
// every phase left is a phase agent's, or waits on one or on a job, and what the plan says of a phase
// agent's phase is that agent's to do, not the lead's.
func NoPhase(verb string) string {
	return verb + " — no phase is in hand here: every phase left is a phase agent's, or waits for one or for a job, and nothing of a phase agent's is yours to do. End your turn; baton wakes you when there is something to do."
}

// AskFirst says to put proposal p to the human before anything else: until it is, nothing moves.
func AskFirst(p state.Decision) string {
	return fmt.Sprintf("put your proposal %s to the human first: %s", p.ID, Question{p.Question, OptionsOf(p)}.Call())
}

// ContextWarning asks the model to put the context question to the human; call is its exact call.
// stopped says the model ended its turn without having asked it.
func ContextWarning(call string, stopped bool) string {
	lead := "[baton] Context warning. Before anything else, "
	if stopped {
		lead = "[baton] You stopped, but put baton's context question to the human first: "
	}
	return lead + call + ". baton acts on the answer itself; then follow what it tells you."
}

// ReviewQuestion is the question that stops the run for the human to review the decisions phase made
// without them.
func ReviewQuestion(phase string, n int) Question {
	return Question{
		Text:    fmt.Sprintf("baton: %s has made %d decisions without you, as many as it may before you review them; /baton status lists each with its undo. Let the run go on?", phase, n),
		Options: ReviewOptions,
	}
}

// Deadline is when baton answers a proposal made at now: timeout later, rounded up to the minute, since
// the question names it as HH:MM and baton must never answer before the time it gave.
func Deadline(now time.Time, timeout time.Duration) time.Time {
	t := now.Add(timeout)
	if r := t.Truncate(time.Minute); r.Before(t) {
		return r.Add(time.Minute)
	}
	return t
}

// ProposalQuestion is the question that puts a timed proposal to the human. because and action must be
// sanitized already.
func ProposalQuestion(because, action string, deadline time.Time) Question {
	return Question{
		Text:    fmt.Sprintf("baton: %s. Unless you answer by %s, I will %s.", clause(because), deadline.Local().Format("15:04"), lowerFirst(clause(action))),
		Options: ProposalOptions,
	}
}

// WentAhead is what baton says to the human (say) and tells the model (tell) when nobody answered the lead's
// proposal id, to do what (undone with undo), by deadline, so baton chose Go ahead for them. last says it
// was the last decision phase may make without the human before they review them (max_auto_decisions,
// which the human set); otherwise the model carries on with phase.
func WentAhead(id, what, undo, phase string, deadline time.Time, last bool) (say, tell string) {
	at := deadline.Local().Format("15:04")
	say = fmt.Sprintf("baton: nobody answered by %s, so baton went ahead with %s — %s", at, id, what)
	tell = fmt.Sprintf("[baton] Nobody answered your proposal %s by %s, so baton chose Go ahead for the human. Do what you proposed now: %s. "+
		"If they later ask, it is undone with: %s.", id, at, what, undo)
	if last {
		return say, tell + " That was the last decision baton lets " + phase + " make without the human: once it is done, end your turn, and baton stops for them to review the decisions."
	}
	return say, tell + " Then carry on with " + Named(phase) + "."
}

// Continued is what baton says to the human (say) and tells the model (tell) when nobody answered its
// question about reason by deadline, so baton chose Continue for them, and phase goes on.
func Continued(reason, phase string, deadline time.Time) (say, tell string) {
	at := deadline.Local().Format("15:04")
	say = fmt.Sprintf("baton: nobody answered by %s, so baton chose Continue — %s", at, reason)
	tell = fmt.Sprintf("[baton] Nobody answered baton's question (%s) by %s, so baton chose Continue for the human: the plan goes on. Carry on with %s.", reason, at, Named(phase))
	return say, tell
}

// ProposalDismissed tells the human that the question of the lead's proposal p went away unanswered:
// baton still answers it at its deadline (Go ahead, or Not now for an outward one) unless they write in
// the session first.
func ProposalDismissed(p state.Decision) string {
	then := "baton goes ahead with it"
	if p.Outward != "" {
		then = "baton answers Not now for you (Claude does it only if it is safely reversible and stays in the project)"
	}
	return fmt.Sprintf("baton: the question of %s went away unanswered → %s at %s, unless you write in the session first",
		p.ID, then, p.Deadline.Local().Format("15:04"))
}

// OutwardQuestion is the question that puts an outward proposal to the human: why it looks so, and what
// baton answers at deadline if they do not (Not now). because and action must be sanitized already.
func OutwardQuestion(because, action, why string, deadline time.Time) Question {
	return Question{Text: outwardText("baton", because, action, why, deadline), Options: OutwardOptions}
}

// outwardText is the question of who ("baton", or "baton: the agent on P3") about an outward proposal.
func outwardText(who, because, action, why string, deadline time.Time) string {
	return fmt.Sprintf("%s: %s. I propose to %s, but %s: unless you answer by %s, I will do it only if it is safely reversible and stays in the project, "+
		"and leave it for you otherwise.", who, clause(because), lowerFirst(clause(action)), why, deadline.Local().Format("15:04"))
}

// AnsweredNotNow is what baton says to the human (say) and tells the model (tell) when nobody answered
// the lead's outward proposal id, to do what, by deadline, so baton answered Not now for them; the model
// then carries on with phase. A zero deadline says the human chose Not now themselves.
func AnsweredNotNow(id, what, phase string, deadline time.Time) (say, tell string) {
	return notNow("", id, what, Named(phase), deadline)
}

// notNow words Not now on proposal id (to do what) of the agent on agent, or of the lead's (""): baton's
// answer at deadline, or the human's own (a zero deadline). The model then carries on with phase.
func notNow(agent, id, what, phase string, deadline time.Time) (say, tell string) {
	whose, who := id, "Claude"
	if agent != "" {
		whose, who = "the proposal "+id+" of the agent on "+agent, "the agent"
	}
	then := fmt.Sprintf("%s does it only if it is safely reversible and stays in the project; otherwise it is left for you — %s", who, clause(what))
	if deadline.IsZero() {
		say = fmt.Sprintf("baton: you chose Not now on %s → %s", whose, then)
		tell = fmt.Sprintf("[baton] The human chose Not now on your proposal %s (%s). %s Then carry on with %s.", id, clause(what), NotNowRule(), phase)
		return say, tell
	}
	at := deadline.Local().Format("15:04")
	say = fmt.Sprintf("baton: nobody answered by %s, so baton answered Not now for you on %s → %s", at, whose, then)
	tell = fmt.Sprintf("[baton] Nobody answered your proposal %s (%s) by %s, so baton answered Not now for the human: the run does not wait on them. %s Then carry on with %s.",
		id, clause(what), at, NotNowRule(), phase)
	return say, tell
}

// clause trims what would double up with the punctuation around it.
func clause(s string) string { return strings.TrimRight(strings.TrimSpace(s), " .") }

// lowerFirst lowers a capital that only starts a sentence ("Commit P4" reads "commit P4"), and leaves
// one that is part of a word ("PR", "GPG") alone.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	next, _ := utf8.DecodeRuneInString(s[n:])
	if unicode.IsUpper(r) && !unicode.IsUpper(next) {
		return string(unicode.ToLower(r)) + s[n:]
	}
	return s
}

// Sanitize makes text from the model safe to put inside a question baton issues. Whitespace collapses to
// single spaces, double quotes become single quotes and backslashes slashes, and other control
// characters are dropped. Quoted for the model (%q), the text then reads exactly as baton stores and
// matches it, with nothing escaped for the model to reproduce or not.
func Sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case r == '\\':
			return '/'
		case unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, s)
	return Normalize(s)
}

// Normalize collapses runs of whitespace, so a question matches whatever line breaks it went through.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
