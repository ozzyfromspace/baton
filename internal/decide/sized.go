package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's landing is verified again when other work came on the run's branch since its gate
// (loop/merge.go), sized to the change (v0.8.7, loop/sized.go): skipped when nothing that came shares a
// file with the phase, Markdown aside, and a QA phase not started yet follows it with aggregate_qa on,
// and otherwise run with the phase's land check (`baton done --land-check`), when it gave one.

// NoSharedFiles is why a landing was not verified again: the landed files (changed on branch, the run's
// branch, since its gate) and mine (what the phase changes) share none, or only the Markdown files
// markdown.
func NoSharedFiles(branch string, landed, mine int, markdown []string) string {
	share := "share none with"
	if len(markdown) > 0 {
		share = "share only Markdown (" + strings.Join(markdown, ", ") + ") with"
	}
	return fmt.Sprintf("the %d %s changed on %s since its gate %s the %d it changes", landed, plural(landed, "file", "files"), branch, share, mine)
}

// SkippedOver is what the announcement of a landing adds when its verification again was skipped: it
// landed over commits its gate did not run with, which change none of its files, and qa, the QA phase
// ahead, checks them together.
func SkippedOver(commits []string, qa string) string {
	return fmt.Sprintf(", over %d %s its gate did not run with (%s), which change none of its files, Markdown aside: not verified again, as %s, the QA phase ahead, checks them together",
		len(commits), plural(len(commits), "commit", "commits"), strings.Join(shorts(commits), ", "), qa)
}

// skippedGate is what the lead is told of a landing whose verification again was skipped (Landing.QA).
func skippedGate(l state.Landing) string {
	return fmt.Sprintf("%s's gate passed on %s, before %s %s, which change none of its files, Markdown aside; %s, the QA phase ahead, checks them together.",
		l.Phase, short7(l.Verified), plural(len(l.Unverified), "commit", "commits"), strings.Join(shorts(l.Unverified), ", "), l.QA)
}

// LandCheckHow tells a phase agent of `baton done --land-check`: the focused command its landing is
// verified again with, when its gate is long. A quoted command must not start with a shell or git, which
// Claude Code refuses from an agent isolated in a worktree.
const LandCheckHow = "If other work lands on the run's branch before yours, baton merges it into your branch and may verify yours again with your last passing gate. " +
	"When that gate is long (a measurement, an end-to-end suite), give a focused check of what your phase changed as you report it done: " +
	"`baton done <phase> --land-check \"<command>\"`, a command that does not start with a shell or git, and baton runs it instead."

// LandCheckRecorded is what `baton done --land-check` says it recorded: cmd verifies phase again at its
// landing.
func LandCheckRecorded(phase, cmd string) string {
	return fmt.Sprintf("baton: land check recorded — if other work lands before %s does, baton verifies %s again with `%s` instead of its last passing gate.", phase, phase, cmd)
}

// LandCheckLead is what `baton done --land-check` says for a phase of the lead's own, which no merge queue
// lands.
func LandCheckLead(phase string) string {
	return fmt.Sprintf("baton: note — a land check is for a phase agent's landing; %s is the lead's own, so --land-check recorded nothing.", phase)
}
