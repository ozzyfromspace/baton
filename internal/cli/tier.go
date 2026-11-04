package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/upgrade"
)

// The lead's tier is claude's --model and --effort (docs/research/tiers.md). A restart of the lead on a
// phase's tier (loop/leadtier.go) hands the tier to the restarted host in loop.TierEnv, and the host puts it
// on claude's command line in place of the human's own flags (withTier). The human's arguments stay as they
// were everywhere else, so a recovery or an upgrade restart carries the tier on in the environment, and the
// human's own flags come back once the lead goes back on their tier.

// withTier is claude's arguments with --model and --effort set to model and effort, each in place of the
// human's own when given ("" leaves the human's), before any "--" (what follows it is a prompt).
func withTier(args []string, model, effort string) []string {
	if model == "" && effort == "" {
		return args
	}
	drop := map[string]bool{}
	if model != "" {
		drop["--model"] = true
	}
	if effort != "" {
		drop["--effort"] = true
	}
	var out []string
	end := len(args)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			end = i
			break
		}
		name, _, hasVal := strings.Cut(a, "=")
		if drop[name] {
			if !hasVal && i+1 < len(args) {
				i++ // its value
			}
			continue
		}
		out = append(out, a)
	}
	if model != "" {
		out = append(out, "--model", model)
	}
	if effort != "" {
		out = append(out, "--effort", effort)
	}
	return append(out, args[end:]...)
}

// launchTier is the tier claude's arguments launch it on: its --model's family and its --effort, each ""
// when the arguments leave it to the human's settings.
func launchTier(args []string) plan.Tier {
	var t plan.Tier
	if m, ok := flagValue(args, "--model"); ok {
		t.Model = plan.Family(m)
	}
	if e, ok := flagValue(args, "--effort"); ok {
		t.Effort = strings.ToLower(strings.TrimSpace(e))
	}
	return t
}

// restartTier hands this process over to a host on this same binary that resumes session on tier (as
// loop.TierEnv carries it), with the human's own claude arguments: it returns only if that failed.
func restartTier(io IO, session, tier string, args []string, logf func(string, ...any)) int {
	argv := append(append([]string{"baton"}, elevate.KeepUserArgs(args)...), "--resume", session)
	env := append(withoutVar(withoutVar(os.Environ(), "BATON_RECOVER"), loop.TierEnv), loop.TierEnv+"="+tier)
	exe, err := os.Executable()
	if err == nil {
		model, effort := loop.DecodeTier(tier)
		fmt.Fprintf(io.Out, "baton: restarting this session in place on %s…\n", plan.Tier{Model: model, Effort: effort})
		logf("host: restarting in place on %s: %s %q", tier, exe, argv)
		err = upgrade.Exec(exe, argv, env)
	}
	logf("host: could not restart in place on %s: %v", tier, err)
	return fail(io, "could not restart the session on %s (%v); resume it with: baton --resume %s", tier, err, session)
}

// tierLabel is what `baton status` says of phase's tier: "  · opus/high", raised by baton
// ("  · opus/xhigh, raised from opus/high"), or "" for a phase that leaves its tier to the session.
func tierLabel(st state.State, pl plan.Plan, phase string) string {
	t := state.TierFor(st, pl, phase, st.Run.Models)
	if t.Zero() {
		return ""
	}
	label := "  · " + t.String()
	if rs := state.Raises(st, pl, phase); len(rs) > 0 {
		label += ", raised from " + rs[0].From.String()
	}
	return label
}

// tierChanged says and logs, in run st of store s, the tiers the run's following plan now in place of was
// changed (state.TierChanges, decide.TierChanged): the human is told at the next hook (Run.Announce).
func tierChanged(s *state.Store, st *state.State, was, now plan.Plan) {
	changes := state.TierChanges(*st, was, now)
	for _, c := range changes {
		s.Event("tier_changed", c.Fields())
	}
	if line := decide.TierChanged(changes); line != "" {
		st.Run.Announce = append(st.Run.Announce, line)
	}
}
