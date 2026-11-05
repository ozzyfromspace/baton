package decide

import (
	"fmt"
	"strings"
	"time"
)

// What the human is told while a session waits to restart on a newer baton (loop/upgradeidle.go). In a
// 74-phase campaign a background poll loop the lead's model had started 22 hours before kept every restart
// away, and a newer baton held new phase agents for it with no deadline: nothing said what held the run.

// UpgradeTask is a piece of the model's own background work that would end with a restart: what kind it
// is (Claude Code's task type: shell, subagent, workflow), what names it, how long it has run, and the
// phase it started in ("" when unknown).
type UpgradeTask struct {
	Type, Description, ID string
	Age                   time.Duration
	Phase                 string
}

// UpgradeHeld is what keeps a restart on a newer baton away: the model's own background work, the phases
// whose agents are at work, the session's wakeups and monitors, and the gate's own reason (a dialog open,
// the usage limit).
type UpgradeHeld struct {
	Tasks             []UpgradeTask
	Agents            []string
	Wakeups, Monitors int
	Gate              string
}

// UpgradeWaits tells the human, once a kind of thing has kept the session from restarting on version for
// waited, what keeps it away: each background task with its age and phase, the phase agents, wakeups,
// monitors, and the gate's reason. A task no longer needed is theirs to stop; the restart comes by itself
// once nothing is left.
func UpgradeWaits(version string, waited time.Duration, h UpgradeHeld) string {
	var what []string
	for _, t := range h.Tasks {
		what = append(what, upgradeTask(t))
	}
	if n := len(h.Agents); n > 0 {
		what = append(what, fmt.Sprintf("the phase %s on %s", plural(n, "agent", "agents"), andList(h.Agents)))
	}
	if h.Wakeups > 0 {
		what = append(what, fmt.Sprintf("%d %s (a cron or a /loop)", h.Wakeups, plural(h.Wakeups, "wakeup", "wakeups")))
	}
	if h.Monitors > 0 {
		what = append(what, fmt.Sprintf("%d %s", h.Monitors, plural(h.Monitors, "monitor", "monitors")))
	}
	s := fmt.Sprintf("baton: baton %s is installed, but for %s this session could not restart on it", version, Spell(waited))
	if len(what) > 0 {
		s += ", since a restart would end " + andList(what)
	}
	if h.Gate != "" {
		s += "; also, " + h.Gate
	}
	s += fmt.Sprintf(". It restarts on %s by itself at the first safe point with none of that left", version)
	if len(h.Tasks) > 0 {
		s += "; a background task no longer needed can be stopped"
	}
	return s + "."
}

// upgradeTask names a background task that would end with a restart: `the background shell "Wait for
// the tests" (22h05m, from P23)`.
func upgradeTask(t UpgradeTask) string {
	kind := "background " + t.Type
	switch t.Type {
	case "":
		kind = "background task"
	case "subagent":
		kind = "subagent"
	case "workflow":
		kind = "workflow"
	}
	name := strings.TrimSpace(t.Description)
	if name == "" {
		name = t.ID
	}
	var about []string
	if t.Age > 0 {
		about = append(about, "running "+Span(t.Age))
	}
	if t.Phase != "" {
		about = append(about, "started in "+t.Phase)
	}
	s := fmt.Sprintf("the %s %q", kind, name)
	if len(about) > 0 {
		s += " (" + strings.Join(about, ", ") + ")"
	}
	return s
}

// UpgradeGivenUp tells the human that baton held new phase agents for waited so that the session could
// restart on version once the agents at work (on agents) were done, and starts them again: a newer baton
// never holds a run for longer. The restart still comes at the first safe point with no agent at work.
func UpgradeGivenUp(version string, waited time.Duration, agents []string) string {
	on := "the phase agents at work"
	if n := len(agents); n > 0 {
		on = fmt.Sprintf("the phase %s on %s", plural(n, "agent", "agents"), andList(agents))
	}
	return fmt.Sprintf("baton: baton %s is installed. For %s baton held new phase agents, so that this session could restart on it once %s "+
		"had finished; it starts them again now, and the run goes on. The restart comes by itself at the first safe point with no agent at work.",
		version, Spell(waited), on)
}
