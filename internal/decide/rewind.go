package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Rewind is what still stands after the human rewound the conversation (Esc Esc): what baton recorded since
// the point it went back to, which the model no longer remembers. A rewind never rolls baton's record back
// (docs/research/recovery.md).
type Rewind struct {
	Since time.Time // when the part rewound away began
	// Decisions were made, or answered, since then.
	Decisions []state.Decision
	// Jobs were handed to baton since then, or had their result given since then (each with its Log).
	Jobs []state.Job
	// Done are the phases baton finished since then, at the model's `baton done`.
	Done []string
	// Blocked is why the phase was reported blocked since then, while it still is.
	Blocked string
	// Files are the files Claude's edit tools wrote since then.
	Files []string
	// Brief is the phase's brief again, when the part rewound away held it.
	Brief string
}

// Any reports something to tell the model.
func (r Rewind) Any() bool {
	return len(r.Decisions)+len(r.Jobs)+len(r.Done)+len(r.Files) > 0 || r.Blocked != "" || r.Brief != ""
}

// Rewound tells the model that the human rewound the conversation, and what of baton's record from the part
// rewound away still stands: the record is never rolled back, so the model goes on with it as it is.
func (w Words) Rewound(r Rewind) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[baton] The human rewound this conversation (Esc Esc) to before %s: what came after that is gone from it, "+
		"but not from baton's record or from the files on disk. baton does not roll anything back for a rewind.", r.Since.Local().Format("15:04"))
	var stands []string
	for _, d := range r.Decisions {
		stands = append(stands, rewoundDecision(d))
	}
	for _, j := range r.Jobs {
		stands = append(stands, rewoundJob(j))
	}
	for _, id := range r.Done {
		stands = append(stands, id+" is done on baton's record (you ran `baton done "+id+"`).")
	}
	if r.Blocked != "" {
		stands = append(stands, "The phase is blocked on baton's record ("+clause(r.Blocked)+"): it waits for the human.")
	}
	if len(stands) > 0 {
		b.WriteString(" What it recorded since then still stands:\n\n- " + strings.Join(stands, "\n- "))
	}
	if len(r.Files) > 0 {
		check := "check them"
		if w.Git {
			check = "check `git status` and `git diff`"
		}
		fmt.Fprintf(&b, "\n\nFiles you edited since then, which are as they are on disk now: %s. The human may have restored them with the rewind, or not: "+
			"%s before you rely on what you remember of them.", strings.Join(r.Files, ", "), check)
	}
	if len(stands)+len(r.Files) > 0 {
		b.WriteString("\n\nGo on from the human's message with all this as it stands. If they want any of it undone, that is theirs to ask for, and each decision has its undo.")
	}
	if r.Brief != "" {
		b.WriteString("\n\nThe part rewound away held baton's brief, which this conversation no longer has, so here it is again:\n\n" + r.Brief)
	}
	return b.String()
}

// rewoundDecision is a decision from the part rewound away, and where it stands.
func rewoundDecision(d state.Decision) string {
	if d.Kind != state.Proposal {
		return fmt.Sprintf("%s, a note: %s. Undo: %s.", d.ID, clause(d.What), clause(UndoOf(d)))
	}
	var where string
	switch r := d.Resolved; {
	case r == nil:
		where = "it waits for the human's answer"
	case r.By == state.ByHuman:
		where = "the human answered it: " + clause(r.Answer)
	case r.By == state.ByTimeout:
		where = "nobody answered it by " + d.Deadline.Local().Format("15:04") + ", so baton went ahead with it"
	case r.By == state.ByPaused:
		where = "the human paused baton over it"
	default:
		where = "it is held for the human: do not act on it"
	}
	return fmt.Sprintf("%s, a proposal: %s; %s. Undo: %s.", d.ID, clause(d.What), where, clause(UndoOf(d)))
}

// rewoundJob is a job from the part rewound away, and where it stands.
func rewoundJob(j state.Job) string {
	kind := j.Kind
	if kind == "" {
		kind = state.JobVerify
	}
	what := fmt.Sprintf("Job %s (%s, %s %s)", j.ID, j.Phase, kind, Ran(j))
	switch {
	case !j.Finished():
		return what + " still runs: baton gives you its result when it ends."
	case j.Delivered.IsZero():
		return what + " " + Outcome(j) + ": baton gives you its result."
	case j.Log != "":
		return what + " " + Outcome(j) + ". You were given its result before the rewind; its log is " + j.Log + "."
	}
	return what + " " + Outcome(j) + ". You were given its result before the rewind."
}
