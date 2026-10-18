package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A plan may name a record file (plan.Plan.Record): baton appends each phase's record to it as the phase
// is done (a lead phase) or lands (an agent's), commits that file alone in the lead's checkout (package
// loop, record.go), and refuses every model's edit of it (package hooks).

// RecordOwned is why baton refused a model's edit of the plan's record file, path.
func RecordOwned(path string) string {
	return fmt.Sprintf("[baton] Not run: %s is the plan's record file, which baton alone writes: as each phase is done or lands, baton appends its record "+
		"(its title, when, its `baton done` notes and its commits) and commits the file. Leave it to baton, and put what the record should say of your phase "+
		"in your `baton done --notes`. If the human wants the file changed, they edit it themselves.", path)
}

// RecordEditRefused tells the human that baton refused a model's edit of the plan's record file, path.
func RecordEditRefused(path string) string {
	return "baton: refused an edit of " + path + " — the plan's record file is baton's to write"
}

// RecordEntry is a phase's record, as baton appends it to the plan's record file.
type RecordEntry struct {
	Phase, Title string
	At           time.Time // when the phase was done, or landed
	Landed       bool      // its phase agent's work landed; otherwise the lead did the phase
	Notes        string    // its `baton done` notes, "" for none
	Commits      []string  // its commits, oldest first: "<short hash> <subject>" each
}

// maxRecordCommits is how many commits a phase's record lists.
const maxRecordCommits = 30

// Text is the entry, from its heading to the blank line after it.
func (e RecordEntry) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s — %s\n\n", e.Phase, e.Title)
	when := e.At.Format("2006-01-02 15:04")
	if e.Landed {
		fmt.Fprintf(&b, "Landed %s, from its phase agent.\n\n", when)
	} else {
		fmt.Fprintf(&b, "Done %s, by the lead.\n\n", when)
	}
	if notes := strings.TrimSpace(e.Notes); notes != "" {
		b.WriteString(notes + "\n\n")
	} else {
		b.WriteString("No notes.\n\n")
	}
	if len(e.Commits) == 0 {
		b.WriteString("Commits: none.\n\n")
		return b.String()
	}
	b.WriteString("Commits:\n\n")
	commits := e.Commits
	if len(commits) > maxRecordCommits {
		commits = commits[:maxRecordCommits]
	}
	for _, c := range commits {
		b.WriteString("- " + c + "\n")
	}
	if more := len(e.Commits) - len(commits); more > 0 {
		fmt.Fprintf(&b, "- and %d more\n", more)
	}
	b.WriteString("\n")
	return b.String()
}

// RecordHeader opens a record file baton starts, for the plan titled title.
func RecordHeader(title string) string {
	return fmt.Sprintf("# Record: %s\n\nbaton writes this file: each phase's record, as the phase is done or lands.\n\n", title)
}

// RecordCommit is the message of the commit that holds phase's record.
func RecordCommit(phase, title string) string {
	if r := []rune(title); len(r) > 60 {
		title = strings.TrimSpace(string(r[:59])) + "…"
	}
	return fmt.Sprintf("chore(baton): record %s — %s", phase, title)
}

// RecordsTold tells the lead's model of the records baton wrote to the plan's record file, and committed
// underneath it.
func RecordsTold(notes []state.RecordNote) string {
	var parts []string
	path := ""
	for _, n := range notes {
		path = n.Path
		if n.Commit == "" {
			parts = append(parts, n.Phase)
		} else {
			parts = append(parts, fmt.Sprintf("%s (%s)", n.Phase, short7(n.Commit)))
		}
	}
	return fmt.Sprintf("[baton] baton wrote the %s of %s to %s, the plan's record file, and committed the file alone on the run's branch, underneath you: "+
		"HEAD moved, and nothing else changed (your own changes, staged or not, were not touched). The file is baton's alone to write: leave it to baton.",
		plural(len(notes), "record", "records"), strings.Join(parts, ", "), path)
}

// RecordSay tells the human that phase's record was written to path, in commit ("" for a project with
// no git).
func RecordSay(phase, path, commit string) string {
	if commit == "" {
		return fmt.Sprintf("baton: %s's record → %s", phase, path)
	}
	return fmt.Sprintf("baton: %s's record → %s (%s)", phase, path, short7(commit))
}

// RecordHeld tells the human that phase's record waits, and why (RecordDirty, RecordOffBranch).
func RecordHeld(phase, why string) string {
	return fmt.Sprintf("baton: %s's record waits: %s", phase, why)
}

// RecordDirty is why a record waits on the record file, path, which has uncommitted changes.
func RecordDirty(path string) string {
	return path + " has uncommitted changes, which baton leaves alone; it writes the record once the file is committed or restored"
}

// RecordOffBranch is why a record waits on the lead's checkout, which is on the branch on rather than the
// run's branch, run.
func RecordOffBranch(on, run string) string {
	return fmt.Sprintf("the lead's checkout is on %q, which lacks commits of the run's branch %s; it writes the record once the checkout is back on it, or on a branch that holds it", on, run)
}

// RecordFailed tells the human that phase's record could not be written to path, and why.
func RecordFailed(phase, path, why string) string {
	return fmt.Sprintf("baton: could not write %s's record to %s (%s); baton tries again", phase, path, why)
}
