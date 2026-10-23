package decide

import (
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says of the one full run that confirms a QA phase's reruns: queued at the done, owed while the
// rerun runs, told to the human, what the phase resumes from if it fails, and none needed.
func TestConfirmWords(t *testing.T) {
	files := state.Confirm{Of: "j4", Command: "bin/qa-set", Commit: "c3", Files: []string{"src/a.go", "src/b.go"}, More: 3, Job: "j9"}
	owed := files
	owed.Job, owed.After = "", "j7"
	unknown := state.Confirm{Of: "j4", Command: "bin/qa-set", Unknown: true, Job: "j9"}
	gone := state.Confirm{Command: "bin/qa-set", Unknown: true, Job: "j9"}
	for _, c := range []struct{ got, want string }{
		{Confirming("P11", files), "P11 is verifying: one full run confirms it (j9), since files beyond the failing tests changed since the full run (j4): " +
			"src/a.go, src/b.go and 3 more. If it passes, P11 is done; if it fails, it is P11's red gate, and the same command reruns what failed in it."},
		{Confirming("P11", unknown), "P11 is verifying: one full run confirms it (j9), since baton could not read what changed since the full run (j4). " +
			"If it passes, P11 is done; if it fails, it is P11's red gate, and the same command reruns what failed in it."},
		{ConfirmQueued("P11", gone), "one full run confirms P11's reruns (job j9, `bin/qa-set`), since the full run is no longer on record"},
		{ConfirmOwed("P11", owed), "P11 is verifying on j7, which reruns what failed; once it passes, one full run of `bin/qa-set` confirms it, " +
			"since files beyond the failing tests changed since the full run (j4): src/a.go, src/b.go and 3 more. " +
			"If either fails, P11 reopens, and the same command reruns what failed."},
		{ConfirmQueued("P11", owed), "once job j7 passes, one full run of `bin/qa-set` confirms P11's reruns, since files beyond the failing tests changed since the full run (j4): " +
			"src/a.go, src/b.go and 3 more"},
		{ConfirmNotes("P11", files, "every red fixed"), "baton ran `bin/qa-set` whole once more after reruns cleared P11's gate, since files beyond the failing tests changed " +
			"since the full run (j4): src/a.go, src/b.go and 3 more. Before that: every red fixed"},
		{ConfirmSkipped("P11", "j4", 0), "no full run follows P11's reruns: nothing changed since the full run (j4)."},
		{ConfirmSkipped("P11", "j4", 2), "no full run follows P11's reruns: since the full run (j4), only Markdown and the failing tests' own files changed (2 files)."},
		{WatchEvent("qa_confirm_queued", map[string]any{"phase": "P11", "job": "j9"}), "P11 verifying with one full run (job j9)"},
	} {
		if c.got != c.want {
			t.Errorf("got\n%s\nwant\n%s", c.got, c.want)
		}
	}
	if len(files.Files) != 2 {
		t.Errorf("the words changed the confirmation's files: %q", files.Files)
	}
}
