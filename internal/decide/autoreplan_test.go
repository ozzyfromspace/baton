package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The brief of a replan baton opens by itself, at the lead's phase boundary, says why it opened, what its
// kind may change, when baton keeps it, and that the phase whose brief follows begins once it is done.
func TestAutoReplanWords(t *testing.T) {
	base := ReplanBrief{Title: "Chains", Draft: "/r/draft.md", Max: 20 * time.Minute, Then: "P4",
		Pending: []ReplanPhase{{ID: "P5", Title: "Web", Waits: "after P4"}, {ID: "P6", Title: "Docs", Waits: "after P5"}},
		Shape:   plan.Shape{Phases: 2, Chain: []string{"P5", "P6"}, Width: 1}}
	for _, c := range []struct {
		name        string
		by          string
		cue         []ReplanPhase
		limits      plan.Limits
		wants, lack []string
	}{
		{"upgrade", state.ReplanUpgrade, nil, plan.Limits{}, []string{
			`[baton] Before you begin P4: replan the phases of "Chains" that have not started, so more of them can run at once. Do it first, in this turn; P4's brief follows this one.`,
			"Why now: this plan was made under older planning rules than those of the baton running it now",
			"- nothing else of a phase: its heading, its steps and its done-when stay byte for byte",
			"\n\nbaton keeps the draft only if it shortens the longest chain of the phases not started (2 now) or lets more of them run at once (1 now); otherwise the plan stays as it is.\n",
			"Once baton replanned has answered, begin P4, as its brief below says.",
		}, []string{"The human", "split a phase", "reword"}},
		{"qa", state.ReplanQA, []ReplanPhase{{ID: "P4", Title: "QA"}}, plan.Limits{}, []string{
			"Why now: P4 (QA), a QA phase, starts, and the phases not started are mostly one chain. Check each link of that chain against the shape rules below",
			"baton keeps the draft only if",
		}, []string{"split a phase"}},
		{"marked", state.ReplanMarked, []ReplanPhase{{ID: "P3", Title: "Spike"}}, plan.Limits{Add: true, Edit: true}, []string{
			`replan the phases of "Chains" that have not started, with what P3 found. Do it first`,
			"Why now: P3 (Spike) is done, and the plan marks it `Replan: after`: what it found should reshape the phases not started. What it found is in its notes (/r/handoff.md).",
			"- split a phase not started",
			"- rework the steps of a phase not started with what P3 found, and its done-when where that needs it",
			"- no phase not started is dropped or merged away",
		}, []string{"baton keeps the draft only if", "reword a phase", "nothing else of a phase"}},
		{"two marked", state.ReplanMarked, []ReplanPhase{{ID: "P2", Title: "Probe"}, {ID: "P3", Title: "Spike"}}, plan.Limits{Add: true, Edit: true}, []string{
			"Why now: P2 (Probe) and P3 (Spike) are done, and the plan marks them `Replan: after`: what they found should reshape the phases not started. What they found is in their notes",
		}, nil},
		{"conflict", state.ReplanConflict, []ReplanPhase{{ID: "P5", Title: "Web"}, {ID: "P6", Title: "Docs"}}, plan.Limits{Add: true, Edit: true}, []string{
			`replan the phases of "Chains" that have not started, so the phases that name the files that conflicted stop colliding when they land. Do it first`,
			"Why now: notes.md (named by P5 and P6) and go.sum each conflicted in a second landing of this run, and phases not started name them. " +
				"One file that phases running at once each change serializes them: each landing must merge the last one's changes to it, and conflicts there. " +
				"Give each phase not started that names one a file of its own, and have a QA phase merge them; " +
				"where one truly builds on another's change to it, have it wait for that one instead (shape rule 4).",
			"- split a phase not started",
			"- reword a phase not started so it works on a file of its own (its steps name the file it changes), and its done-when where that needs it",
			"- no phase not started is dropped or merged away",
		}, []string{"baton keeps the draft only if", "The human", "Also:"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := base
			b.By, b.Cue, b.Limits = c.by, c.cue, c.limits
			if c.by == state.ReplanMarked {
				b.Notes = "/r/handoff.md"
			}
			if c.by == state.ReplanConflict {
				b.Files = []ReplanFile{{Path: "notes.md", Phases: []string{"P5", "P6"}}, {Path: "go.sum"}}
			}
			brief := b.Brief()
			for _, want := range c.wants {
				if !strings.Contains(brief, want) {
					t.Errorf("the brief lacks %q:\n%s", want, brief)
				}
			}
			for _, lack := range c.lack {
				if strings.Contains(brief, lack) {
					t.Errorf("the brief says %q:\n%s", lack, brief)
				}
			}
		})
	}
	say, tell := ReplanUnimproved(plan.Shape{Phases: 4, Chain: []string{"a", "b", "c"}, Width: 2}, plan.Shape{Phases: 4, Chain: []string{"a", "b", "c"}, Width: 2})
	for _, c := range []struct{ got, want string }{
		{AutoReplanOpened(state.ReplanUpgrade, nil, 20*time.Minute),
			"baton: replanning the phases not started (a newer baton's planning rules) → Claude does it before the next phase; no new phase agent starts until it is done, 20 minutes at most"},
		{AutoReplanOpened(state.ReplanQA, []string{"P4"}, 20*time.Minute),
			"baton: replanning the phases not started (P4, a QA phase, starts over a chain of phases not started) → Claude does it before the next phase; no new phase agent starts until it is done, 20 minutes at most"},
		{AutoReplanOpened(state.ReplanMarked, []string{"P2", "P3"}, 20*time.Minute),
			"baton: replanning the phases not started (P2 and P3 are done, marked Replan: after) → Claude does it before the next phase; no new phase agent starts until it is done, 20 minutes at most"},
		{AutoReplanOpened(state.ReplanConflict, []string{"P5"}, 20*time.Minute),
			"baton: replanning the phases not started (a file that conflicted in a second landing is named by P5, not started) → Claude does it before the next phase; no new phase agent starts until it is done, 20 minutes at most"},
		{ReplanOpened(state.ReplanConflict, "", 20*time.Minute),
			"baton: replanning the phases not started (a file conflicted in a second landing) → no new phase agent starts until Claude is done, 20 minutes at most"},
		{say, "baton: replan closed, the plan unchanged — Claude's draft left the phases not started no better (4 phases · longest chain 3 → 3 · up to 2 → 2 at once)"},
		{tell, "baton: not replanned — the draft leaves the phases not started no better (4 phases · longest chain 3 → 3 · up to 2 → 2 at once), and baton keeps a replan of its own " +
			"only if it shortens the longest chain or lets more phases run at once. The plan file stays as it was, and the replan is closed."},
		{CompactedReplan("P4 (Web)", false), "[baton] Context compacted. Replan first, as the brief above says (finish with baton replanned), then begin P4 (Web) — its brief above has your instructions."},
		{CompactedReplan("P4 (Web)", true), "[baton] Context compacted. Replan first, as the brief above says (finish with baton replanned), then resume P4 (Web) — its brief above has your instructions."},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
}
