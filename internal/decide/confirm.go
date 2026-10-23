package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The confirmation run (v0.8.15, state/confirm.go): after reruns cleared a QA phase's gate (slim.go), one
// more full run follows at its done only if the fixes could have broken what passed. It is baton's, queued
// at the done (Confirming) or once the rerun the phase verifies on passes (ConfirmOwed), and the human is told
// either way (ConfirmQueued); with only Markdown and the failing tests' own files changed, none follows
// (ConfirmSkipped).

// confirmWhy is why a phase owes confirmation c: "since files beyond the failing tests changed since the full
// run (j4): src/a.go and 3 more".
func confirmWhy(c state.Confirm) string {
	switch {
	case c.Of == "":
		return "since the full run is no longer on record"
	case c.Unknown:
		return fmt.Sprintf("since baton could not read what changed since the full run (%s)", c.Of)
	}
	files := c.Files
	if c.More > 0 {
		files = append(files[:len(files):len(files)], fmt.Sprintf("%d more", c.More))
	}
	return fmt.Sprintf("since files beyond the failing tests changed since the full run (%s): %s", c.Of, andList(files))
}

// Confirming tells the model, at phase's done, that baton queued confirmation c (c.Job), and why.
func Confirming(phase string, c state.Confirm) string {
	return fmt.Sprintf("%s is verifying: one full run confirms it (%s), %s. If it passes, %s is done; if it fails, it is %s's red gate, "+
		"and the same command reruns what failed in it.", phase, c.Job, confirmWhy(c), phase, phase)
}

// ConfirmOwed tells the model, at phase's done, that the rerun it verifies on (c.After) runs on, and that
// confirmation c follows once it passes.
func ConfirmOwed(phase string, c state.Confirm) string {
	return fmt.Sprintf("%s is verifying on %s, which reruns what failed; once it passes, one full run of `%s` confirms it, %s. "+
		"If either fails, %s reopens, and the same command reruns what failed.", phase, c.After, c.Command, confirmWhy(c), phase)
}

// ConfirmQueued tells the human of confirmation c of phase's reruns: queued (c.Job), or owed once the rerun
// it waits for passes (c.After).
func ConfirmQueued(phase string, c state.Confirm) string {
	if c.Job == "" {
		return fmt.Sprintf("once job %s passes, one full run of `%s` confirms %s's reruns, %s", c.After, c.Command, phase, confirmWhy(c))
	}
	return fmt.Sprintf("one full run confirms %s's reruns (job %s, `%s`), %s", phase, c.Job, c.Command, confirmWhy(c))
}

// ConfirmNotes are what phase resumes from if confirmation c fails: why it ran, then notes, where the phase
// stood before.
func ConfirmNotes(phase string, c state.Confirm, notes string) string {
	n := fmt.Sprintf("baton ran `%s` whole once more after reruns cleared %s's gate, %s.", c.Command, phase, confirmWhy(c))
	if notes != "" {
		n += " Before that: " + notes
	}
	return n
}

// ConfirmSkipped tells the model, at phase's done, that no full run follows the reruns that cleared its gate:
// of all the files changed since the full run, of (changed), none goes beyond Markdown and the failing tests'
// own.
func ConfirmSkipped(phase, of string, changed int) string {
	if changed == 0 {
		return fmt.Sprintf("no full run follows %s's reruns: nothing changed since the full run (%s).", phase, of)
	}
	return fmt.Sprintf("no full run follows %s's reruns: since the full run (%s), only Markdown and the failing tests' own files changed (%d %s).",
		phase, of, changed, plural(changed, "file", "files"))
}
