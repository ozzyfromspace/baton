package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What a replan changed reads as one line, for the human and for the decision that records it.
func TestReplanChanges(t *testing.T) {
	cs := []plan.Change{
		{Kind: plan.ChangeGoal, Phase: plan.GoalRules},
		{Kind: plan.ChangeAfter, Phase: "P4", Was: []string{"P3"}, Now: []string{"P1"}},
		{Kind: plan.ChangeText, Phase: "P4"},
		{Kind: plan.ChangeMoved, Phase: "P4"},
		{Kind: plan.ChangeAdded, Phase: "P2b", Now: []string{"P2"}},
		{Kind: plan.ChangeAdded, Phase: "P6", Now: []string{}},
		{Kind: plan.ChangeAfter, Phase: "P5", Was: []string{"P4"}, Now: []string{"P2", "P2b"}},
		{Kind: plan.ChangeDropped, Phase: "P3"},
	}
	want := "the standing rules section changed; P4 now after P1 (was after P3); P4 reworded; P4 moved; P2b added (after P2); " +
		"P6 added (needing no other phase); P5 now after P2 and P2b (was after P4); P3 dropped"
	if got := ReplanChanges(cs); got != want {
		t.Errorf("ReplanChanges:\n got %q\nwant %q", got, want)
	}
	if got := ReplanChanges(nil); got != "nothing" {
		t.Errorf("no change: %q", got)
	}
	if got := ReplanWhat(state.ReplanHuman, cs[1:2], `the web app needs only the API.`); got !=
		"baton replanned the phases not started, at the human's /baton replan: P4 now after P1 (was after P3) — why: the web app needs only the API" {
		t.Errorf("ReplanWhat: %q", got)
	}
	if got := ReplanUndo("/p/.baton/runs/s/replan/1.md", "/plans/x.md"); got !=
		"copy /p/.baton/runs/s/replan/1.md back over /plans/x.md; baton follows it at the next stop, as long as every phase started since is in it" {
		t.Errorf("ReplanUndo: %q", got)
	}
}

// A refused draft is told exactly what failed, each breach on a line of its own.
func TestReplanRefused(t *testing.T) {
	got := ReplanRefused([]plan.Breach{
		{Kind: plan.BreachBuild, Detail: "phase 3 (\"P2\"): After: names P4, which comes after it\nphase 4 (\"P3\"): title is empty"},
		{Kind: plan.BreachGone, Phase: "P0"},
		{Kind: plan.BreachText, Phase: "P1"},
		{Kind: plan.BreachDeps, Phase: "P1", Was: []string{"P0"}, Now: []string{"P0b"}},
		{Kind: plan.BreachGoal, Phase: plan.GoalPreamble},
		{Kind: plan.BreachDropped, Phase: "P3"},
		{Kind: plan.BreachAdded, Phase: "P2b"},
		{Kind: plan.BreachEdited, Phase: "P2"},
		{Kind: plan.BreachQA, Phase: "P5"},
		{Kind: BreachUnsaid, Phase: "P4"},
	}, "/r/replan/draft.md")
	for _, want := range []string{
		"not replanned — the draft changes what this replan may not. The plan file is untouched, and the replan stays open: fix the draft (/r/replan/draft.md)",
		"\n- the draft does not build as a plan:\n    phase 3 (\"P2\"): After: names P4, which comes after it\n    phase 4 (\"P3\"): title is empty",
		"\n- P0 has started or is done, and the draft has no such phase",
		"\n- P1 has started or is done: its text must stay byte for byte as it was",
		"\n- P1 has started or is done: it must wait for the phases it waited for (P0), not P0b",
		"\n- the title or the text before the first phase changed: it stays byte for byte unless the human's words ask for that",
		"\n- P3 is gone: this replan keeps every phase not started",
		"\n- P2b is new: this replan may not add phases",
		"\n- P2's text changed: this replan may change only its After: line and its place",
		"\n- P5 lost its `Type: qa` line",
		"\n- P4 is dropped, and your --notes do not name it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, got)
		}
	}
}

// A replan's brief says what the model replans, what it may change by the replan's limits, and how to
// finish; the human is told it opened, how it ended, and how it stands in baton status.
func TestReplanWords(t *testing.T) {
	b := ReplanBrief{Title: "Chains", By: state.ReplanHuman, Words: "the CI phase can wait", Draft: "/r/draft.md", Max: 20 * time.Minute,
		Pending: []ReplanPhase{{ID: "P2", Title: "CLI", Waits: "after P1"}},
		Frozen:  []ReplanPhase{{ID: "P0", Title: "Schema", Stands: "done"}},
		Shape:   plan.Shape{Phases: 1, Chain: []string{"P2"}, Width: 1},
		Limits:  plan.Limits{Add: true, Edit: true, Drop: true, Goal: true}}
	brief := b.Brief()
	for _, want := range []string{
		`The human asked you to consider: "the CI phase can wait". Weigh it first.`,
		"Not started (yours to restructure):\n  P2 CLI — after P1\n",
		"Started or done (never change these",
		"  P0 Schema — done\n",
		ShapeRules,
		"- merge or drop phases not started, only as far as the human's words ask; name each phase you drop or merge away in your --notes",
		"- the goal sections (the title and the text before the first phase, the standing rules, the end section) only where the human's words ask",
		"Edit only the draft, /r/draft.md, never the plan file",
		"If it is not finished within 20 minutes, baton drops the draft",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "The phases not started now") {
		t.Error("a part of one phase has no shape line to show")
	}
	b.Limits = plan.Limits{}
	if strict := b.Brief(); strings.Contains(strict, "split a phase") || strings.Contains(strict, "reword") || !strings.Contains(strict, "no phase not started is dropped") {
		t.Errorf("a structure-only brief:\n%s", strict)
	}
	for _, c := range []struct{ got, want string }{
		{ReplanOpened(state.ReplanHuman, `the "CI" phase`, 20*time.Minute), "baton: replanning the phases not started (you asked: \"the 'CI' phase\") → no new phase agent starts until Claude is done, 20 minutes at most"},
		{ReplanOpened(state.ReplanHuman, "", 20*time.Minute), "baton: replanning the phases not started (you asked) → no new phase agent starts until Claude is done, 20 minutes at most"},
		{ReplanLapsed(20 * time.Minute), "baton: the replan was not finished within 20 minutes → its draft is dropped, and the plan goes on as it was"},
		{ShapeShift(plan.Shape{Phases: 4, Chain: []string{"a", "b", "c", "d"}, Width: 1}, plan.Shape{Phases: 5, Chain: []string{"a", "b"}, Width: 3}),
			"4 → 5 phases · longest chain 4 → 2 · up to 1 → 3 at once"},
		{ReplanNext("", "P1 (API)", state.ModeRunning, false, true), "Carry on with P1 (API) as the plan describes it."},
		{ReplanNext("P2 (CLI)", "P2 (CLI)", state.ModeRunning, true, false), "No phase was in hand, and P2 (CLI) can start: end your turn now, so baton compacts this conversation, then P2 (CLI) starts with a fresh brief."},
		{ReplanNext("", "P2 (CLI)", state.ModeRunning, true, false), "End your turn now: baton compacts the context, then P2 (CLI) starts with a fresh brief."},
		{ReplanNext("", "", state.ModeRunning, false, false), "No phase can start yet: end your turn, and baton wakes you when there is something to do."},
		{ReplanNext("", "P1 (API)", state.ModePaused, false, false), "The run stays paused: /baton run gives it back to baton."},
		{ReplanNext("", "", state.ModeComplete, false, false), "Every phase left is done, so the plan is complete: sum up for the human and end your turn."},
		{ReplanStands(state.PhaseWorking, false), "a phase agent's"},
		{ReplanStands(state.PhasePending, true), "next, after the compaction"},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	line := ReplanLine(state.Replanned{N: 1, By: state.ReplanHuman, At: at, Decision: "d3", Undone: at.Add(time.Hour),
		Changed:  []plan.Change{{Kind: plan.ChangeAfter, Phase: "P3", Was: []string{"P2"}, Now: []string{"P1"}}},
		Approved: []plan.Change{{Kind: plan.ChangeDropped, Phase: "P6"}}})
	if line != "1  Oct 16 12:00  you asked: P3 now after P1 (was after P2) (d3), undone Oct 16 13:00\n       against the approved plan: P6 dropped" {
		t.Errorf("ReplanLine: %q", line)
	}
	if got := ReplanOpenLine(state.Replan{By: state.ReplanHuman, Since: at}, 20*time.Minute); got !=
		"replan: open since 12:00 (you asked); no new phase agent starts until it ends, at 12:20 at the latest" {
		t.Errorf("ReplanOpenLine: %q", got)
	}
}
