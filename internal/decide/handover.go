package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase the lead started goes to a phase agent when the lead does not run on its tier, and cannot restart
// on it while agents are at work (state/handover.go, loop/handover.go): what the human, the lead's model and
// the agent are told.

// maxHandedNotes bounds the lead's notes a handed phase's brief quotes.
const maxHandedNotes = 4000

// Handover is where the lead left a phase baton gives a phase agent, as the agent's brief opens with it
// (HandedBrief): the phase, the tier the agent runs on and the lead's, why baton raised it ("" for a tier
// its plan names), the commit the phase last started on in the lead's checkout (Since; "" when unknown) and
// the lead's commits for it since ("<short> <subject>", oldest first; More past those), the notes it was
// set aside with, and its last gate (nil for none).
type Handover struct {
	Phase      string
	Tier, Lead plan.Tier
	Raised     string
	Since      string
	Commits    []string
	More       int
	Notes      string
	Gate       *HandedGate
}

// HandedGate is the last gate of a phase the lead handed on: the job as it ended, and what its log says
// (lane.ReadExcerpt): where it is, the lines that report a failure (More past those), and its end.
type HandedGate struct {
	Job      state.Job
	Path     string
	Failures []string
	More     int
	Tail     string
}

// HandedWhy is why a phase baton gives a phase agent is not the lead's: its tier, raised (why baton raised
// it, "" for the tier its plan names), and the tier the lead runs on.
func HandedWhy(tier, lead plan.Tier, raised string) string {
	if raised != "" {
		return fmt.Sprintf("baton raised it to %s after %s, and the lead runs on %s", tier, raised, lead)
	}
	return fmt.Sprintf("its tier is %s, and the lead runs on %s", tier, lead)
}

// HandedBrief opens the brief of the phase agent baton gave h.Phase, which the lead's session started (the
// reverse of StoppedTakenBrief): why it is the agent's, where the lead's work on it is, the lead's notes,
// and its last gate, with the failures its log reports.
func HandedBrief(h Handover) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The lead's session started %s, and baton gave it to you, on %s: %s. The lead changes its tier only by a restart, "+
		"which would end every phase agent at work, so you carry %s on from where the lead left it.", h.Phase, h.Tier, HandedWhy(h.Tier, h.Lead, h.Raised), h.Phase)
	switch {
	case h.Since == "":
		fmt.Fprintf(&b, " The lead's work on %s is on the run's branch, which your branch starts from.", h.Phase)
	case len(h.Commits) == 0:
		fmt.Fprintf(&b, " The lead committed nothing for %s since it last took it up (at %s): its work, if any, is further back on the run's branch, "+
			"which your branch starts from.", h.Phase, short7(h.Since))
	default:
		fmt.Fprintf(&b, " The lead's work on %s is on the run's branch, which your branch starts from. Since it last took %s up (at %s), it committed:\n\n",
			h.Phase, h.Phase, short7(h.Since))
		for _, c := range h.Commits {
			b.WriteString("  " + c + "\n")
		}
		if h.More > 0 {
			fmt.Fprintf(&b, "  … and %d more\n", h.More)
		}
	}
	if notes := strings.TrimSpace(h.Notes); notes != "" {
		fmt.Fprintf(&b, "\n\nThe lead's notes as it set %s aside:\n\n%s", h.Phase, clipTo(notes, maxHandedNotes))
	}
	red := false
	if g := h.Gate; g != nil {
		red = g.Job.Red()
		fmt.Fprintf(&b, "\n\nIts last gate, job %s, ran %s and %s.", g.Job.ID, Ran(g.Job), Outcome(g.Job))
		if red && len(g.Failures) > 0 {
			fmt.Fprintf(&b, " Lines that report a failure in its log:\n\n%s\n", strings.Join(g.Failures, "\n"))
			if g.More > 0 {
				fmt.Fprintf(&b, "(and %d more such %s)\n", g.More, plural(g.More, "line", "lines"))
			}
		}
		if tail := strings.TrimSpace(g.Tail); red && tail != "" {
			fmt.Fprintf(&b, "\nThe end of its log (%s):\n\n%s", g.Path, tail)
		} else if red && g.Path != "" {
			fmt.Fprintf(&b, " Its log: %s", g.Path)
		}
	}
	if red {
		fmt.Fprintf(&b, "\n\nRead what the lead did (git log, git show) and why its gate failed before you change more, then finish %s here.", h.Phase)
	} else {
		fmt.Fprintf(&b, "\n\nRead what the lead did (git log, git show) before you change more, then finish %s here.", h.Phase)
	}
	return strings.TrimRight(b.String(), "\n")
}

// HandedNow is the "Now:" line of the brief of the phase agent given a phase the lead started (title:
// "P3 — Title").
func HandedNow(title string) string {
	return fmt.Sprintf("Now: carry %s on from where the lead's session left it (above), in this worktree.", title)
}

// PhaseHanded tells the human that baton set phase aside, mid-phase, for a phase agent on tier: their
// session runs on lead, and could not restart on tier while phase agents (agents of them) were at work.
// next is the phase their session takes up after a compaction ("" for none).
func PhaseHanded(phase string, tier, lead plan.Tier, agents int, next string) string {
	then := "your session takes up " + next + " after a compaction"
	if next == "" {
		then = "your session holds no phase until one can start"
	}
	return fmt.Sprintf("baton: %s runs on %s now, and your session, on %s, could not restart on it while %d %s at work, "+
		"so baton set %s aside for a phase agent on %s, which starts from %s's work on the run's branch; %s",
		phase, tier, lead, agents, plural(agents, "phase agent was", "phase agents were"), phase, tier, phase, then)
}

// HandedTakenUp tells the human that a phase agent takes up name ("P3 (Title)"), which their session
// started, on tier, which their session does not run on, in a worktree of its own on branch.
func HandedTakenUp(name string, tier plan.Tier, branch string) string {
	return fmt.Sprintf("baton: a phase agent takes %s up from where your session left it%s, a tier your session does not run on, "+
		"in a worktree of its own on the branch %s", name, OnTier(tier), branch)
}

// HandoverHeld tells the human, once a phase, that phase, on tier now, waits for their checkout's
// uncommitted changes (files of them) before a phase agent can take it on that tier: their session, on
// lead, cannot restart on it while agents are at work, and an agent starts from the run's branch alone.
func HandoverHeld(phase string, tier, lead plan.Tier, files int) string {
	return fmt.Sprintf("baton: %s runs on %s now, and your session, on %s, cannot restart on it while phase agents are at work; "+
		"baton gives %s to a phase agent on that tier once your checkout has no uncommitted change (%d uncommitted %s now)",
		phase, tier, lead, phase, files, plural(files, "file", "files"))
}

// BoundaryRedirected tells the human that phase, which their session was about to take up again, goes to a
// phase agent on tier instead, a tier their session does not run on; next is what their session takes up
// after the compaction ("" for none).
func BoundaryRedirected(phase string, tier plan.Tier, next string) string {
	then := next + " starts after the compaction instead"
	if next == "" {
		then = "your session holds no phase until one can start"
	}
	return fmt.Sprintf("baton: %s goes to a phase agent on %s, a tier your session does not run on, rather than back to your session; %s",
		phase, tier, then)
}
