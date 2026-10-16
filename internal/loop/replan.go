package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A change the human makes to the plan file reaches the agents at work. The lead follows it (the Stop
// hook's planDrift, `baton done`, `baton run`), which rewrites the run's plan.json; each step of the
// coordinator then passes it on to the sub-run of every worker of the plan on the roster (replan): its
// plan.json becomes the new plan narrowed to its phase (plan.Only), so that its brief and its sections read
// the new text, and, when the standing rules or its phase's section read differently from what its agent
// was given (state.PlanText), its agent is told the new text (state.MessagePlan), by the mail path its
// results take: at its next tool call or its stop, an idle one woken for it. A change is told once, and one
// that touches only other phases is not told at all. A fresh agent is told nothing of a change its brief
// already reads (hooks' agentStart drops the message).

// errPassed leaves a sub-run whose plan another step passed on already as it is.
var errPassed = errors.New("passed on already")

// replan passes the run's plan pl (the lead's, as st has it) on to its workers' sub-runs whose plan is an
// older one, or its file where it was before baton moved it out of Claude Code's way (state.Aside): their
// briefs read it there. The plan file is read once, and only while it is the text pl was made from: one
// changed again since is the lead's to follow first, which may refuse it.
func (c *Coordinator) replan(now time.Time, s *state.Store, st state.State, pl plan.Plan) {
	if pl.SHA256 == "" || pl.File == "" {
		return
	}
	var doc []byte
	for _, e := range st.LiveWorkers() {
		if e.Generation < st.Generation {
			continue // a replaced plan's: it is retired
		}
		sub, err := c.Project.ExistingRun(e.Ref, "")
		if err != nil {
			continue
		}
		if have, err := sub.LoadPlan(); err != nil || have.SHA256 == pl.SHA256 && have.File == pl.File {
			continue
		}
		if doc == nil {
			b, err := os.ReadFile(pl.File)
			if sum := sha256.Sum256(b); err != nil || hex.EncodeToString(sum[:]) != pl.SHA256 {
				return
			}
			doc = b
		}
		only, err := pl.Only(e.Phase)
		if err != nil {
			continue
		}
		c.passOn(now, s, sub, e.Phase, only, doc)
	}
}

// passOn saves only, the run's plan narrowed to phase, as the plan of phase's sub-run sub, and tells its
// agent what changed for it in doc (planNews): only while its phase is in hand there, since an agent whose
// phase is done has nothing to follow it with.
func (c *Coordinator) passOn(now time.Time, s, sub *state.Store, phase string, only plan.Plan, doc []byte) {
	var text state.PlanText
	var rules, section string
	var told bool
	_, err := sub.Update(func(st *state.State) error {
		if have, err := sub.LoadPlan(); err == nil && have.SHA256 == only.SHA256 && have.File == only.File {
			return errPassed
		}
		if err := sub.WritePlan(only); err != nil {
			return err
		}
		text, rules, section = planNews(only, doc, st.PlanText)
		if told = (rules != "" || section != "") && st.Current == phase; told {
			state.Tell(st, state.Message{Kind: state.MessagePlan, Text: decide.PlanChanged(phase, rules, section), Phase: phase}, now)
		}
		st.PlanText = &text
		return nil
	})
	switch {
	case errors.Is(err, errPassed):
		return
	case err != nil:
		c.Lead.logf("loop: %s: the plan's change not passed on: %v", phase, err)
		return
	}
	sub.Event("plan_followed", map[string]any{"plan": short(only.SHA256), "told": told})
	if told {
		c.Lead.logf("loop: %s: its agent is told of the plan's change", phase)
		s.Event("plan_told", map[string]any{"phase": phase, "rules": rules != "", "section": section != ""})
		c.announce(s, decide.PlanTold(phase, rules != "", section != ""))
	}
}

// planNews is what doc says, under only (a worker's plan, narrowed to its phase), of the standing rules
// and the phase's section: their hashes (text), and the text of each that reads differently from what was
// given (was; nil when nothing is on record), or "" for one that does not.
func planNews(only plan.Plan, doc []byte, was *state.PlanText) (text state.PlanText, rules, section string) {
	rules = only.Rules(doc)
	section, _ = only.Section(doc, 0)
	text = state.PlanText{Rules: state.TextSum(rules), Section: state.TextSum(section)}
	if was != nil && was.Rules == text.Rules {
		rules = ""
	}
	if was != nil && was.Section == text.Section {
		section = ""
	}
	return text, rules, section
}

// planText is what doc says of the standing rules and the section of only's phase, as hashes: what the
// agent of a sub-run made now is given.
func planText(only plan.Plan, doc []byte) *state.PlanText {
	if doc == nil {
		return nil
	}
	text, _, _ := planNews(only, doc, nil)
	return &text
}
