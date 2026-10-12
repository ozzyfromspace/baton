//go:build !windows

package e2e

import (
	"testing"
	"time"
)

const longPhasePlan = `# Long plan

## P0 — Count to twelve
This phase is long. Run ` + "`echo step N`" + ` for every N from 1 to 12, one Bash call per step, in order.

## P1 — Wrap up
Run ` + "`echo done`" + `.
`

// warnPlan's first phase ends a turn halfway. A model may put off a context question that reaches it
// mid-turn (Haiku in auto mode often takes hook feedback beside a tool result for an injection); baton
// then refuses its stop once with the question, which it does ask. A phase finished within one turn
// would never get there: its boundary compaction makes the question moot.
const warnPlan = `# Long plan

## P0 — Count to twelve
This phase has two halves. Run ` + "`echo step N`" + ` for every N from 1 to 6, one Bash call per step, in order, then end your turn: the human reads the output before the second half. When told to go on, run steps 7 to 12 the same way.

## P1 — Wrap up
Run ` + "`echo done`" + `.
`

// Mid-phase valve. The nudge is asserted as an event (it is emitted by a hook, deterministically); whether
// a model checkpoints immediately or finishes a short phase first is its judgment, so the checkpoint
// itself is requested explicitly here: baton compacts, briefs the same phase, and wakes the model.
func TestCheckpointCompactsAndContinuesThePhase(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, longPhasePlan)
	s := StartEnv(t, dir, []string{"BATON_CHECKPOINT_PCT=5"}, "--model", "haiku",
		"Run `echo step 1` with the Bash tool. Then run `baton checkpoint --notes \"step 1 done; next is step 2\"` and end your turn. After that, follow baton's instructions.")
	s.Trust()
	waitSequence(t, s, 4*time.Minute,
		kind("context_nudge"),
		kind("checkpoint"),
		func(e map[string]any) bool { return e["kind"] == "compact_queued" && e["reason"] == "checkpoint" },
		func(e map[string]any) bool { return e["kind"] == "compact_started" && e["by_baton"] == true },
		func(e map[string]any) bool {
			return e["kind"] == "brief" && e["type"] == "checkpoint" && e["phase"] == "P0"
		},
		kind("rewake"),
		func(e map[string]any) bool { return e["kind"] == "turn_started" && e["by"] == "baton" },
	)
}

// Context warning. Past the warning line (forced low here), a hook has the model ask the human with a
// fixed question, mid-turn or when it stops; picking "Checkpoint now" (the first option, which
// AutoApprove's Enter selects) makes the next stop a checkpoint, and baton compacts and continues the
// same phase. AutoApprove also stands in for the human on Haiku's permission prompts.
func TestContextWarningAsksTheHuman(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, warnPlan)
	s := StartEnv(t, dir, []string{"BATON_WARN_TOKENS=1000"}, "--model", "haiku", "Work on P0, and follow baton's instructions.")
	s.Trust()
	s.AutoApprove()
	waitSequence(t, s, 6*time.Minute,
		kind("context_warning"),
		func(e map[string]any) bool { return e["kind"] == "dialog_open" && e["tool"] == "AskUserQuestion" },
		kind("checkpoint_asked"),
		func(e map[string]any) bool { return e["kind"] == "compact_queued" && e["reason"] == "checkpoint" },
		func(e map[string]any) bool {
			return e["kind"] == "brief" && e["type"] == "checkpoint" && e["phase"] == "P0"
		},
		kind("rewake"),
	)
}

// Nobody answers the context question: after the (shortened) timeout, and never sooner than a minute
// after it was shown, baton's mod answers "Keep going" itself, with no key typed. baton records it as it
// would the answer from the dialog, so the run carries on instead of waiting all night.
func TestContextQuestionTimesOut(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, warnPlan)
	s := StartEnv(t, dir, []string{"BATON_WARN_TOKENS=1000", "BATON_WARN_TIMEOUT=20s"}, "--model", "haiku", "Work on P0, and follow baton's instructions.")
	s.Trust()
	waitSequence(t, s, 5*time.Minute,
		kind("context_warning"),
		func(e map[string]any) bool { return e["kind"] == "dialog_open" && e["dialog"] == "context_warning" },
		is("mod_deadline", "answer", "Keep going"),
		func(e map[string]any) bool {
			return e["kind"] == "answered" && e["answer"] == "Keep going" && e["by"] == "timeout" && e["via"] == "mod"
		},
	)
	none(t, s, "auto_answered")
}
