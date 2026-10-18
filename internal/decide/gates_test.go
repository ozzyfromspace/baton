package decide

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// gatePlan builds a plan with one phase per entry of heads, each entry the lines under its heading
// ("After: none", "Type: qa", or "" for none).
func gatePlan(t *testing.T, heads ...string) plan.Plan {
	t.Helper()
	var b strings.Builder
	b.WriteString("# Gates\n\n## Standing rules\n\nBe quick.\n")
	for i, h := range heads {
		fmt.Fprintf(&b, "\n## P%d — Phase %d\n\n", i, i)
		if h != "" {
			b.WriteString(h + "\n\n")
		}
		b.WriteString("Work.\n")
	}
	b.WriteString("\n## Verification\n\nAll green.\n")
	doc := []byte(b.String())
	pl, err := plan.Build("gates.md", doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

// The gate rules open with the frame, a human who feels time, and hold the ten rules in order.
func TestGateRulesWords(t *testing.T) {
	if !strings.HasPrefix(GateRules, "Test the way a human who feels time does: scoped checks while you work, and the big one once, at the end.") {
		t.Errorf("the rules open with %q", GateRules[:80])
	}
	at := 0
	for _, want := range []string{
		"1. QA phases sit at real seams, never per phase: at the end, and, in a long plan, at the end of a group of related phases that later work builds on.",
		"Each comes `After:` everything it checks. A short plan usually needs one.",
		"2. Every other phase runs scoped checks that finish in seconds or minutes: the tests of what it changed, by name.",
		"Its \"done when\" is a commit, never a green full suite.",
		"3. Gate scripts are committed in the repo, built from `baton stage <name> -- <command>` steps, and stream every stage.",
		"4. A gate stops at its first failure. After a red, `baton verify --failed` reruns only what failed",
		"5. A setup phase proves the lane recipe",
		"6. Timings on a shared machine are warnings; bytes and counts gate.",
		"7. No file many phases edit: records per phase, or the plan's `Record:` file.",
		"8. Long checks go through `baton verify` or `baton await`, never a background shell.",
		"9. Push to CI early in the QA phase, batching fixes per round trip.",
		"10. A gate script tears down what it starts (`trap … EXIT`), and a phase stops what it started (servers, containers, test stacks) before `baton done`.",
	} {
		i := strings.Index(GateRules[at:], want)
		if i < 0 {
			t.Errorf("the rules lack %q, or have it out of order", want)
			continue
		}
		at += i + len(want)
	}
}

// Every plan is asked to check its gates against the rules, with its QA phases listed, each with the
// phases it covers since the one before. The QA phases before the last are flagged as costing a round of
// full checks mid-plan, a QA phase not `After:` what it checks is named, and a plan with no QA phase is
// told where its full checks would go. Where the seams are stays the model's judgment.
func TestGateCheckWords(t *testing.T) {
	common := []string{
		"[baton] Check this plan's gates too: every check its phases run, and where its QA phases sit. baton's gate rules:\n\n" + GateRules + "\n\n",
		"in that phase's steps, or in the Standing rules for every phase",
		"baton asks this once per plan too.",
	}
	for _, c := range []struct {
		name      string
		pl        plan.Plan
		extra     []string
		want, not []string
	}{
		{
			name:  "one QA phase at the end",
			pl:    gatePlan(t, "", "After: none", "", "After: all\nType: qa", ""),
			extra: nil,
			want: []string{
				"Its QA phases, each with the phases it covers since the one before:\n- P3 (Phase 3): 3 phases since the plan's start\n\n",
				"Confirm that each sits at a real seam: the end of the plan, or the end of a group of related phases that later work builds on, `After:` everything it checks.",
				"Move any that does not sit at a seam to one, or merge it into the next QA phase: that is your judgment, not a number.",
			},
			not: []string{"before the last QA phase", "is not `After:`", "no QA phase"},
		},
		{
			name:  "a QA phase per phase: the extra ones flagged",
			pl:    gatePlan(t, "", "Type: qa", "", "Type: qa", "", "Type: qa"),
			extra: []string{"P1", "P3"},
			want: []string{
				"- P1 (Phase 1): 1 phase since the plan's start\n- P3 (Phase 3): 1 phase since P1\n- P5 (Phase 5): 1 phase since P3\n",
				" P1 and P3 come before the last QA phase: each is a round of full checks mid-plan that costs the human time, worth it only where such a group ends.",
			},
		},
		{
			name:  "two QA phases in a row, one not after what it checks",
			pl:    gatePlan(t, "", "After: none", "After: P0\nType: qa", "Type: qa"),
			extra: []string{"P2"},
			want: []string{
				"- P2 (Phase 2): 2 phases since the plan's start; it is not `After:` P1, which it checks\n- P3 (Phase 3): no phase since P2\n",
				" P2 comes before the last QA phase: a round of full checks mid-plan costs the human time, worth it only where such a group ends.",
			},
		},
		{
			name: "no QA phase",
			pl:   gatePlan(t, "", ""),
			want: []string{"This plan has no QA phase (a line `Type: qa` under a phase's heading), so each phase checks itself in full as it ends. " +
				"A short plan may need none; otherwise add one at the end, `After:` everything it checks, and keep every other phase's checks scoped.\n\n"},
			not: []string{"Its QA phases", "Confirm that each"},
		},
		{
			name: "a plan baton could not read: the rules alone",
			pl:   plan.Plan{},
			not:  []string{"Its QA phases", "no QA phase", "Confirm that each"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := GateCheck(c.pl)
			for _, want := range append(common, c.want...) {
				if !strings.Contains(got, want) {
					t.Errorf("GateCheck lacks %q:\n%s", want, got)
				}
			}
			for _, not := range c.not {
				if strings.Contains(got, not) {
					t.Errorf("GateCheck has %q:\n%s", not, got)
				}
			}
			if extra := ExtraQA(c.pl); strings.Join(extra, " ") != strings.Join(c.extra, " ") {
				t.Errorf("ExtraQA = %v, want %v", extra, c.extra)
			}
		})
	}
}

// A phase's brief gives the gate rules in short: scoped checks by name and no full suite before the QA
// phase, stop at the first failure and rerun only what failed, long gates staged and through baton
// verify, and the environment recorded for every phase; without git, no lane, so no verify or stages.
func TestTheBriefsShortFormOfTheGateRules(t *testing.T) {
	focus := QAFocus("P9 (QA)")
	for _, want := range []string{
		"the next is P9 (QA).",
		"In this phase, run checks that finish in seconds or minutes: the tests of what you changed, by name.",
		"No full suite, end-to-end run or whole-project lint before P9 (QA), even where a convention says to run them before every commit;",
		"commit work in progress all the same.",
		"`baton verify --qa \"<why now>\" --notes \"…\" -- <command>`",
	} {
		if !strings.Contains(focus, want) {
			t.Errorf("QAFocus lacks %q:\n%s", want, focus)
		}
	}
	qa := QAPhase("P0 and P1")
	for _, want := range []string{
		"QA: this is a QA phase. Run the full test suites, lints and the other checks the plan names, and fix what they find in the work of P0 and P1, which ran scoped checks only",
		"The heavy checks run here, once: after a red, rerun only what failed.",
		"Push to CI early, batching fixes per round trip.",
	} {
		if !strings.Contains(qa, want) {
			t.Errorf("QAPhase lacks %q:\n%s", want, qa)
		}
	}
	habits := GateHabits(true)
	for _, want := range []string{
		"Gates: a gate stops at its first failure.",
		"stop it (`baton stop <job>`) unless every failure is worth the wait",
		"rerun only what failed (`baton verify --failed`), never the whole suite again.",
		"built from `baton stage <name> -- <command>` steps so it streams every stage, and runs through `baton verify`, never a background shell.",
		"record what it needs with `baton env \"<what it needs>\"`, so every phase sees it.",
	} {
		if !strings.Contains(habits, want) {
			t.Errorf("GateHabits(git) lacks %q:\n%s", want, habits)
		}
	}
	bare := GateHabits(false)
	if !strings.Contains(bare, "Long checks go through `baton await`, never a background shell.") || !strings.Contains(bare, "`baton env") {
		t.Errorf("GateHabits(no git):\n%s", bare)
	}
	for _, not := range []string{"baton verify", "baton stage"} {
		if strings.Contains(bare, not) {
			t.Errorf("GateHabits(no git) has %q:\n%s", not, bare)
		}
	}
	// Short: the old focus paragraph alone ran to about 700 characters.
	for name, s := range map[string]string{"QAFocus": focus, "QAPhase": qa, "GateHabits": habits} {
		if len(s) > 600 {
			t.Errorf("%s runs to %d characters:\n%s", name, len(s), s)
		}
	}
}
