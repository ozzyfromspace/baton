package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says about what a phase left running (package leftover): a test stack, a server, containers
// compose started in its worktree. A real run's phase agent kept its own test stack up after its work,
// beside the main one, and the QA gate competed with it for the machine. So a phase agent's `baton done`
// is refused once while it leaves something running (LeftoverAsk), retiring a worker stops the processes
// it left and tells the lead of the rest (LeftoverRetired), and a gate slow on a busy machine has its owner
// told what finished phases left running (LeftoverLookIn). baton removes no container itself, and stops no
// process it cannot tell is a phase's: each is listed with the command that stops it.

// maxLeftListed is the most of what a phase left that one message lists.
const maxLeftListed = 12

// LeftoverAsk is the refusal, once, of phase's `baton done` while it leaves left running in its worktree
// (tree) or a lane its gates used. The same call again goes through.
func LeftoverAsk(phase, tree string, left leftover.Left) string {
	var b strings.Builder
	fmt.Fprintf(&b, "not done yet — %s leaves this running, in its worktree (%s) or a lane its gates used, "+
		"and it would go on using the machine beside the phases after it:\n", phase, tree)
	b.WriteString(leftLines(left, "  "))
	fmt.Fprintf(&b, "Stop what %s started%s, then run this `baton done` again. "+
		"If something must stay up, because a later phase or the human needs it, run this `baton done` again as it is: baton asks this once.",
		phase, composeDown(left))
	return b.String()
}

// LeftoverRetired is the look-in that tells the lead what phase's agent left running once it ended, in its
// worktree (tree), that baton did not stop: containers, and processes it could not tell were the phase's
// or could not stop.
func LeftoverRetired(phase, tree string, left leftover.Left) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[baton] %s's agent has ended, but it left this running in its worktree (%s), which baton does not stop by itself: "+
		"it removes no container, and stops no process it cannot tell is the phase's.\n", phase, tree)
	b.WriteString(leftLines(left, "  "))
	b.WriteString("If no phase and not the human still needs them, stop them with the command after each; otherwise leave them. baton asks nothing about them.")
	return b.String()
}

// LeftoverRetiredSaid is the line that tells the human what phase left running after its agent ended.
func LeftoverRetiredSaid(phase string, left leftover.Left) string {
	return fmt.Sprintf("baton: %s left %s running after its agent ended (%s)", phase, leftCount(left), andList(leftNames(left)))
}

// LeftoverStoppedSaid is the line that tells the human what baton stopped of what phase's agent left
// running in its worktree, as it retired it.
func LeftoverStoppedSaid(phase string, stopped []procs.Proc) string {
	var names []string
	for _, p := range stopped {
		names = append(names, fmt.Sprintf("%s (pid %d)", p.Comm, p.PID))
	}
	return fmt.Sprintf("baton: stopped what %s's agent left running in its worktree: %s", phase, andList(clipNames(names)))
}

// PhaseLeft is what a finished phase left running.
type PhaseLeft struct {
	Phase string
	Left  leftover.Left
}

// LeftoverLookIn is the look-in for gate j, which has run ran, past its slowest earlier pass, while the
// machine has no room (busy: why, as the fleet's room reading says it), and finished phases left left
// running.
func LeftoverLookIn(j state.Job, ran time.Duration, busy string, left []PhaseLeft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[baton] Job %s (%s, `%s`) has run %s, past its slowest earlier pass, while %s; and phases that finished left this running:\n",
		j.ID, j.Phase, j.Command, Span(ran), busy)
	for _, l := range left {
		fmt.Fprintf(&b, "  %s:\n%s", l.Phase, leftLines(l.Left, "    "))
	}
	b.WriteString("It may be what slows your gate. If nothing still needs it, stop it with the command after each; baton stops none of it by itself, and asks nothing.")
	return b.String()
}

// LeftoverLookInSaid is the line that tells the human about gate j, slow on a busy machine beside what
// finished phases left running.
func LeftoverLookInSaid(j state.Job, left []PhaseLeft) string {
	var phases []string
	for _, l := range left {
		phases = append(phases, l.Phase)
	}
	return fmt.Sprintf("baton: job %s (`%s`, %s) runs past its history on a busy machine, beside what %s left running", j.ID, j.Command, j.Phase, andList(phases))
}

// leftLines lists what was left running, a line each with the command that stops it, each line after
// indent; past maxLeftListed, how many more.
func leftLines(left leftover.Left, indent string) string {
	var lines []string
	group := map[int]int{} // a process group, and a process of Procs in it
	for _, p := range left.Procs {
		if _, ok := group[p.PGID]; !ok {
			group[p.PGID] = p.PID
		}
		lines = append(lines, fmt.Sprintf("process %d (%s) in %s: kill %d", p.PID, p.Comm, p.Cwd, p.PID))
	}
	for _, p := range left.Hidden {
		in := fmt.Sprintf("in process group %d", p.PGID) // the one started under baton beside it is stopped
		if pid, ok := group[p.PGID]; ok {
			in = fmt.Sprintf("in the process group of %d", pid)
		}
		lines = append(lines, fmt.Sprintf("process %d (%s) in %s, %s: kill %d", p.PID, p.Comm, p.Cwd, in, p.PID))
	}
	for _, c := range left.Containers {
		lines = append(lines, fmt.Sprintf("container %s (%s), which compose started in %s: docker rm -f %s", c.Name, c.Status, c.Dir, c.Name))
	}
	var b strings.Builder
	for i, l := range lines {
		if i == maxLeftListed {
			fmt.Fprintf(&b, "%s- and %d more\n", indent, len(lines)-i)
			break
		}
		b.WriteString(indent + "- " + l + "\n")
	}
	return b.String()
}

// composeDown is the clause that says compose stops its own containers, where left has any.
func composeDown(left leftover.Left) string {
	if len(left.Containers) == 0 {
		return ""
	}
	return " (for containers, `docker compose down` where compose ran stops them with their network)"
}

// leftCount is how much was left running: "2 containers and a process".
func leftCount(left leftover.Left) string {
	var parts []string
	if n := len(left.Containers); n > 0 {
		parts = append(parts, countOf(n, "a container", "containers"))
	}
	if n := len(left.Procs) + len(left.Hidden); n > 0 {
		parts = append(parts, countOf(n, "a process", "processes"))
	}
	return andList(parts)
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// leftNames names what was left running: each container by its name, each process by its name and pid.
func leftNames(left leftover.Left) []string {
	var names []string
	for _, c := range left.Containers {
		names = append(names, c.Name)
	}
	for _, p := range append(append([]procs.Proc{}, left.Procs...), left.Hidden...) {
		names = append(names, fmt.Sprintf("%s (pid %d)", p.Comm, p.PID))
	}
	return clipNames(names)
}

// clipNames keeps the first maxLeftListed of names, and says how many more there are.
func clipNames(names []string) []string {
	if len(names) <= maxLeftListed {
		return names
	}
	return append(names[:maxLeftListed:maxLeftListed], fmt.Sprintf("%d more", len(names)-maxLeftListed))
}
