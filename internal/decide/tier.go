package decide

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The words about a phase's tier, the model and effort it runs on (plan/tier.go, state/tier.go).

// OnTier is " on opus/high", for a line that says what a phase runs on, or "" for a tier left to the
// session.
func OnTier(t plan.Tier) string {
	if t.Zero() {
		return ""
	}
	return " on " + t.String()
}

// TierCapped tells the human that phase asked for tier and its loop was served another: Claude Code clamps
// an effort to the settings' maxEffortLevel, the organization's cap and the model's own, and may serve
// another model, and nothing baton does changes that, so it is said once and the phase goes on.
func TierCapped(phase string, asked, served plan.Tier) string {
	return fmt.Sprintf("baton: %s asked for %s and was served %s. Claude Code chose that (a maxEffortLevel in your settings, "+
		"your organization's cap, or the model's own); the phase goes on as served.", phase, asked, served)
}

// TierRestarting tells the human that baton restarts the lead's session in place on tier t, phase's ("" for
// the human's own, as no plan runs): the same conversation, on the flags the tier needs.
func TierRestarting(phase string, t plan.Tier) string {
	if phase == "" {
		return fmt.Sprintf("baton: restarting your session back on your own %s, as no plan runs: the same conversation, on your tier again", t)
	}
	return fmt.Sprintf("baton: restarting your session on %s, %s's tier: the same conversation, on the flags that tier needs (--model, --effort)", t, phase)
}

// TierRestarted is the note a session restarted in place on tier t starts with, for phase ("" for the
// human's own tier).
func TierRestarted(phase string, t plan.Tier) string {
	if phase == "" {
		return fmt.Sprintf("baton: restarted this session back on your own %s · ", t)
	}
	return fmt.Sprintf("baton: restarted this session on %s, %s's tier · ", t, phase)
}

// TierRestartedMail tells the lead's model, restarted in place mid-phase on tier t, to take phase up again:
// nothing of its work is lost, and the conversation is the same. why is why baton raised it ("" for a tier
// the phase had from its plan).
func TierRestartedMail(phase string, t plan.Tier, why string) string {
	if why != "" {
		return fmt.Sprintf("baton raised %s to %s after %s, and restarted this session on it (the same conversation; nothing of your work is lost). "+
			"Take %s up again where you left it: look again at why its gate keeps failing before you change more.", phase, t, why, phase)
	}
	return fmt.Sprintf("baton restarted this session on %s, the tier the plan names for %s (the same conversation; nothing of your work is lost). "+
		"Take %s up again where you left it.", t, phase, phase)
}

// TierBrief is the line a phase's brief says its tier in: what it runs on, from its plan or raised.
func TierBrief(phase string, t plan.Tier, raised bool) string {
	if raised {
		return fmt.Sprintf("You run on %s: baton raised %s to it.", t, phase)
	}
	return fmt.Sprintf("You run on %s, %s's tier from its plan.", t, phase)
}

// TierRaisedBrief tells a fresh phase agent that baton raised its phase from one tier to another and why,
// and that the agent before it worked on it in this worktree.
func TierRaisedBrief(phase string, from, to plan.Tier, why string) string {
	return fmt.Sprintf("baton raised %s from %s to %s after %s, and started you on it in place of the agent before you, "+
		"which worked on it in this worktree: read what it left (`git status`, `git log`, its notes below) and why its gate kept failing, "+
		"then take %s up again.", phase, from, to, why, phase)
}

// RaiseWhy is why a phase was raised: reds red runs of its gate, command (or "" when not known).
func RaiseWhy(reds int, command string) string {
	why := fmt.Sprintf("%d red runs of its gate", reds)
	if command != "" {
		why += " (`" + clipCommand(command) + "`)"
	}
	return why
}

// clipCommand is a gate's command, short enough for one line.
func clipCommand(command string) string {
	if r := []rune(command); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return command
}

// TierRaised tells the human that baton raised phase from one tier to the next, and why; lead says the lead
// holds it (it restarts on the new tier once idle), else a fresh phase agent takes it up on it.
func TierRaised(phase string, from, to plan.Tier, why string, lead bool) string {
	then := "a fresh phase agent takes it up on that tier in its worktree, told why"
	if lead {
		then = "your session restarts on that tier once it is idle, in the same conversation"
	}
	return fmt.Sprintf("baton: raised %s from %s to %s after %s; %s", phase, from, to, why, then)
}

// AgentTierRaised is why a phase agent's worker is suspended for a fresh agent on tier t.
func AgentTierRaised(t plan.Tier) string {
	return "baton raised its phase to " + t.String() + ", and a fresh agent takes it up on that tier"
}

// AgentTierChanged is why a phase agent's worker is suspended for a fresh agent on tier t, the tier the
// plan file names for its phase now.
func AgentTierChanged(t plan.Tier) string {
	return "the plan file puts its phase on " + t.String() + " now, and a fresh agent takes it up on that tier"
}

// TierRules are the rules a plan's tiers are set by, as the planning model reads them at its first
// ExitPlanMode (TierCheck): what each tier is for, what it costs (Claude Code 2.1.318's own index of
// tokens per effort, docs/research/tiers.md), erring on the side of caution, and how baton raises a phase.
// fable says the human allows Fable; steps and after are escalate and escalate_after (0 steps: none).
func TierRules(fable bool, steps, after int) string {
	models := "haiku, sonnet or opus"
	if fable {
		models = "haiku, sonnet, opus or fable"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "1. **Every phase names its tier.** Two lines under its heading: `Model:` (%s) and `Effort:` (low, medium, high, xhigh or max). "+
		"baton spawns the phase's agent on that tier, or restarts the lead's session on it, and checks what each request was served.\n", models)
	b.WriteString("2. **Size it to be done right the first time.** Too low a tier spends runs of the phase's gate before it is raised; too high spends tokens " +
		"and the account's usage allowance. Effort costs per token, against high: Opus 5.5 low 0.48, medium 0.66, xhigh 1.66, max 3.44; " +
		"Sonnet 5.5 costs about half as much as Opus per token at each level.\n")
	b.WriteString("3. **Err on the side of caution.** When torn between two tiers, take the higher.\n")
	b.WriteString("4. **opus/high** is the default for a phase that builds or changes code.\n")
	b.WriteString("5. **opus/xhigh** for design, a cross-cutting change, concurrency, a migration, security, unfamiliar or tangled code, " +
		"a phase many others build on, a bug with no known cause, and a QA phase that fixes across phases.\n")
	b.WriteString("6. **opus/max** for the hardest: a subtle correctness or concurrency problem, a novel algorithm, " +
		"a phase where a wrong answer is costly and hard to see.\n")
	if fable {
		b.WriteString("   **fable** only for what Opus at max cannot do: it can bill beyond a subscription's plan.\n")
	}
	b.WriteString("7. **opus/medium or sonnet/high** for mechanical work that follows a pattern already there: renames, moving code, wiring, config, docs, test scaffolding.\n")
	b.WriteString("8. **sonnet/medium or haiku** only for a trivial, fully specified edit: a typo, a version bump, a list kept in step.\n")
	if steps > 0 {
		fmt.Fprintf(&b, "9. **Up only, from the record.** baton raises a phase a step after %d red runs of its own gate (effort first, as far as xhigh, then the next model, then max), "+
			"at most %d %s; it never lowers one mid-phase, and each phase starts on its own tier.", after, steps, plural(steps, "step", "steps"))
	} else {
		b.WriteString("9. **No raises.** The human turned escalation off: each phase runs on the tier you give it.")
	}
	return b.String()
}

// TierCheck is the tiers' part of baton's one turn-back at a plan's first ExitPlanMode, beside the gate
// rules: the rules (TierRules), the phases that name no model or no effort, and those that name a model
// the human does not allow, with the model they would run on.
func TierCheck(untiered []string, disallowed map[string]string, fable bool, steps, after int) string {
	var b strings.Builder
	b.WriteString("[baton] Give every phase its tier too: the model and effort it runs on. baton's tier rules:\n\n")
	b.WriteString(TierRules(fable, steps, after) + "\n\n")
	if len(untiered) > 0 {
		fmt.Fprintf(&b, "These phases name no model or no effort: %s. Give each its Model: and Effort: lines.\n\n", strings.Join(untiered, ", "))
	}
	if len(disallowed) > 0 {
		var ids []string
		for id := range disallowed {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		var says []string
		for _, id := range ids {
			says = append(says, id+" (it would run on "+disallowed[id]+")")
		}
		fmt.Fprintf(&b, "These phases name a model the human has not allowed (models in baton's config): %s.\n\n", strings.Join(says, ", "))
	}
	b.WriteString("Where a phase's tier breaks a rule, change it. baton asks this once per plan too.")
	return b.String()
}

// PlanTiers is the plan's tiers in one part of the line the human sees as it goes to them for approval:
// "tiers 8 opus/high, 3 opus/xhigh, 1 the session's", the most common first. "" for a plan with no tier.
func PlanTiers(pl plan.Plan) string {
	if !pl.Tiered() {
		return ""
	}
	count := map[string]int{}
	var order []string
	for _, ph := range pl.Phases {
		k := ph.Tier().String()
		if count[k] == 0 {
			order = append(order, k)
		}
		count[k]++
	}
	slices.SortStableFunc(order, func(a, b string) int { return count[b] - count[a] })
	var parts []string
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%d %s", count[k], k))
	}
	return "tiers " + strings.Join(parts, ", ")
}

// TierGivenUp tells the human that phase goes on on the tier the lead runs on, not its own (want), since no
// restart could come for wait: a phase agent at work all that time, most likely one waiting on them.
func TierGivenUp(phase string, want, runs plan.Tier, wait time.Duration) string {
	return fmt.Sprintf("baton: %s goes on on %s, not its tier %s: for %s the lead could not restart on it, since a phase agent was at work "+
		"(one may be waiting on you). baton restarts the lead on %s as soon as nothing is at work.", phase, runs, want, Spell(wait), want)
}

// TierHold says what holds the lead's boundary compaction while it waits for a restart on t, its next
// phase's tier, for the status line and the board (Run.Gate: "compact held: restart on opus/max waits for 6
// agents"): the gate's own reason when that is what holds the restart, else what would end with the lead's
// claude (agents: phase agents and the model's own subagents and workflows; tasks: its other background
// work, such as shells; wakeups and monitors) or the human's draft.
// Most of it fits the status line's 40 characters. In a 74-phase campaign the status line read
// "compacting…" through such a wait, since nothing recorded it.
func TierHold(t plan.Tier, gate string, agents, tasks, wakeups int, draft bool) string {
	if gate != "" {
		return gate
	}
	var what []string
	if agents > 0 {
		what = append(what, fmt.Sprintf("%d %s", agents, plural(agents, "agent", "agents")))
	}
	if tasks > 0 {
		what = append(what, fmt.Sprintf("%d background %s", tasks, plural(tasks, "task", "tasks")))
	}
	if wakeups > 0 {
		what = append(what, fmt.Sprintf("%d %s", wakeups, plural(wakeups, "wakeup", "wakeups")))
	}
	if draft {
		what = append(what, "your draft")
	}
	s := "restart on " + t.String() + " waits"
	if len(what) > 0 {
		s += " for " + andList(what)
	}
	return s
}

// TierChanged tells the human the tiers a change of the plan file changed (state.TierChanges), so that
// they never have to wonder whether an edit took: each phase under way by name, with what follows for it,
// and how many not started, which start on theirs. "" for none.
func TierChanged(changes []state.TierChange) string {
	var under []string
	later := 0
	for _, c := range changes {
		then := ""
		switch {
		case c.Held == state.HeldByLead && c.Moves:
			then = "your session restarts on it once it is idle, in the same conversation"
		case c.Held == state.HeldByLead:
			then = "your session runs on it already"
		case c.Held == state.HeldByAgent && c.Moves:
			then = "a fresh phase agent takes it up on it in its worktree, at its agent's next idle moment"
		case c.Held == state.HeldByAgent:
			then = "its agent goes on as it is"
		case c.Held == state.HeldStarted:
			then = "it goes on on it once it is taken up again"
		default:
			later++
			continue
		}
		under = append(under, fmt.Sprintf("%s from %s to %s (%s)", c.Phase, c.From, c.To, then))
	}
	notStarted := fmt.Sprintf("%d %s not started, which %s on %s new %s", later, plural(later, "phase", "phases"),
		plural(later, "starts", "start"), plural(later, "its", "their"), plural(later, "tier", "tiers"))
	switch {
	case len(under) == 0 && later == 0:
		return ""
	case len(under) == 0:
		return "baton: the plan file changed the " + plural(later, "tier", "tiers") + " of " + notStarted
	case later == 0:
		return "baton: the plan file changed the tier of " + strings.Join(under, "; ")
	}
	return "baton: the plan file changed the tier of " + strings.Join(under, "; ") + "; and of " + notStarted
}
