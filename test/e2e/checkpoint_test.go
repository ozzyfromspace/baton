//go:build !windows

package e2e

import (
	"fmt"
	"strings"
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
		func(e map[string]any) bool {
			return e["kind"] == "answered" && e["answer"] == "Keep going" && e["by"] == "timeout" && e["via"] == "mod"
		},
	)
	// baton records the answer before the mod's report of it, so that a failure in between has the mod
	// send it again.
	if !s.Until("the mod's report", time.Minute, func() bool { return first(s.Events(), is("mod_deadline", "answer", "Keep going")) >= 0 }) {
		t.Errorf("the mod did not report the deadline:\n%s", eventLog(s))
	}
	none(t, s, "auto_answered")
}

// readingPlan's P0 reads ten files of about 5k tokens each, one at a time, so the context climbs a few
// thousand tokens a step: past the notice line first, then past the checkpoint line.
const readingPlan = `# Reading plan

## P0 — Read the parts
Read the files part01.txt to part10.txt with the Read tool, in order, one file per tool call and never two at once. After each one, run ` + "`echo read N`" + ` with the Bash tool, N being its number. Then write the last word of part10.txt into a new file named last, and commit it.
If baton asks you to checkpoint, do exactly as it says, at once.

## P1 — Wrap up
Run ` + "`echo done`" + `.
`

// The context valves on a small window (a 100k cap): at the notice line baton tells the model where its
// context stands and asks nothing, so the model reads on; at the checkpoint line it asks for a checkpoint,
// and the model takes one. The question to the human (warn) is off, so it cannot come first.
func TestTheContextNoticeAsksNothingBeforeTheCheckpoint(t *testing.T) {
	dir := NewProject(t)
	for i := 1; i <= 10; i++ {
		var b strings.Builder
		for l := 1; l <= 200; l++ {
			fmt.Fprintf(&b, "Part %d, line %d: the heron waits by the reeds while the river carries leaves past the old stone mill.\n", i, l)
		}
		fmt.Fprintf(&b, "The last word of part %d is lantern%d\n", i, i)
		name := fmt.Sprintf("part%02d.txt", i)
		writeFile(t, dir, name, b.String())
		Git(t, dir, "add", name)
	}
	Git(t, dir, "commit", "-q", "-m", "docs: the parts")
	Attach(t, dir, readingPlan)
	s := StartEnv(t, dir, []string{"BATON_NOTICE_PCT=1", "BATON_CHECKPOINT_PCT=55", "BATON_WARN_PCT=0"},
		"--autocompact", "100k", "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	s.ApprovePermissions()
	waitSequence(t, s, 10*time.Minute,
		kind("context_notice"),
		kind("context_nudge"),
		kind("checkpoint"),
		is("compact_queued", "reason", "checkpoint"),
		is("brief", "type", "checkpoint", "phase", "P0"),
	)
	evs := s.Events()
	n, nudge := first(evs, kind("context_notice")), first(evs, kind("context_nudge"))
	notice := evs[n]
	if lim, _ := notice["limit"].(float64); lim != 100_000 {
		t.Errorf("the notice's limit: %v", notice)
	}
	if at, cp := notice["notice"].(float64), notice["checkpoint"].(float64); at <= 0 || at >= cp || cp != 55_000 {
		t.Errorf("the notice at %v, the checkpoint at %v", at, cp)
	}
	// The notice asked nothing: the model read on (the nudge comes with a later call's result), and
	// checkpointed only once asked.
	for _, e := range evs[n:nudge] {
		if e["kind"] == "checkpoint" || e["kind"] == "stop_refused" {
			t.Errorf("before the checkpoint was asked: %v", e)
		}
	}
	none(t, s, "context_warning")
}
