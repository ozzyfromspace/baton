package brief

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

func setup(t *testing.T) (plan.Plan, []byte, state.State) {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "plan", "testdata", "headings-emdash.md"))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := plan.Build("/work/plan.md", doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	st := state.Attach(pl, now, state.Origin{})
	if _, err := state.Done(&st, pl, "P0", now, false); err != nil {
		t.Fatal(err)
	}
	return pl, doc, st
}

func TestBoundaryBrief(t *testing.T) {
	pl, doc, st := setup(t)
	b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st, Handoff: "## P0 — 2026-10-04 12:00\n\nOverlay done in abc123.\n"})
	for _, want := range []string{
		"compacted at a phase boundary", `"The widget campaign"`, "/work/plan.md",
		"✓ P0 — The overlay", "▶ P1 — The one-liners", "· R1 — The roster can grow",
		"Now: P1 — The one-liners. Begin it now.",
		"Standing rules (from the plan):", "Stop only for a decision with no fallback",
		"Phase P1, from the plan:", "## P1 — The one-liners", "Small fixes.",
		"Overlay done in abc123.",
		"baton done P1 --notes", "Never type /compact",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
	if strings.Contains(b, "Add people to a household") {
		t.Error("the brief quoted the next phase's section too")
	}
}

// The brief ends with how to report the phase and reach the human, in words that fit the project.
func TestBriefEndsWithTheWaysToReport(t *testing.T) {
	pl, doc, st := setup(t)
	git := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st, Words: decide.Words{Git: true}})
	for _, want := range []string{"When P1 is complete and committed, run `baton done P1", "`baton note`", "`baton propose`", "`baton blocked --tried`", "baton waiting"} {
		if !strings.Contains(git, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
	plain := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st})
	closing := plain[strings.LastIndex(plain, "\n")+1:] // the plan's own rules may well mention commits
	if !strings.HasPrefix(closing, "When P1 is complete, run") || strings.Contains(closing, "commit") {
		t.Errorf("brief without git ends:\n%s", closing)
	}
}

func TestCheckpointAndAutoBriefsContinueThePhase(t *testing.T) {
	pl, doc, st := setup(t)
	state.Start(&st, time.Now(), state.Origin{})
	for _, kind := range []string{Checkpoint, Auto} {
		b := Write(Input{Kind: kind, Plan: pl, Doc: doc, State: st})
		if !strings.Contains(b, "continue P1 — The one-liners from where you left off") || strings.Contains(b, "Begin it now") {
			t.Errorf("%s brief:\n%s", kind, b)
		}
	}
}

func TestBriefSurvivesAnEditedPlan(t *testing.T) {
	pl, doc, st := setup(t)
	edited := []byte(strings.Replace(string(doc), "## P1 — The one-liners", "## P1 — Renamed", 1))
	b := Write(Input{Kind: Boundary, Plan: pl, Doc: edited, State: st})
	if !strings.Contains(b, "could not quote phase P1") || !strings.Contains(b, "Read /work/plan.md directly") {
		t.Fatalf("brief:\n%s", b)
	}
}

func TestLongNotesKeepTheMostRecent(t *testing.T) {
	pl, doc, st := setup(t)
	var notes strings.Builder
	for i := 0; i < 400; i++ {
		notes.WriteString("## P0 — note\n\nfiller filler filler filler filler\n\n")
	}
	notes.WriteString("## P0 — last\n\nTHE LATEST NOTE\n")
	b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st, Handoff: notes.String()})
	if !strings.Contains(b, "THE LATEST NOTE") || !strings.Contains(b, "earlier notes omitted") {
		t.Fatal("long notes were not trimmed to the most recent")
	}
}

// After a compaction the model still knows what the run decided without the human, so a late "undo that"
// finds its undo; the newest ten, the rest in baton status.
func TestBriefListsTheDecisionsMadeWithoutTheHuman(t *testing.T) {
	pl, doc, st := setup(t)
	if b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st}); strings.Contains(b, "Decisions made without the human") {
		t.Fatal("a section with no decisions in it")
	}
	at := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	for i := 1; i <= 11; i++ {
		state.AddNote(&st, fmt.Sprintf("step %d", i), fmt.Sprintf("revert step %d", i), at)
	}
	state.AddProposal(&st, state.Decision{What: "skip the flaky test", Undo: "re-enable it", Deadline: at.Add(5 * time.Minute)}, at)
	state.Resolve(&st, "d12", state.ByTimeout, "Go ahead", at.Add(5*time.Minute))
	state.AddProposal(&st, state.Decision{What: "use staging", Undo: "switch back"}, at)
	state.Resolve(&st, "d13", state.ByHuman, "Go ahead", at) // the human said so

	for _, git := range []bool{true, false} {
		b := Write(Input{Kind: Auto, Plan: pl, Doc: doc, State: st, Words: decide.Words{Git: git, Timeout: 5 * time.Minute}})
		i := strings.Index(b, "Decisions made without the human so far (if they ask, each has its undo):\n\n- …and 2 earlier (baton status lists every one)\n- d3 (P1, ")
		if i < 0 {
			t.Fatalf("git=%v: no section of the newest ten:\n%s", git, b)
		}
		section := b[i:strings.Index(b, "When P1 is complete")]
		for _, want := range []string{"noted: step 11. Undo: revert step 11\n", "went ahead, as nobody answered by " + at.Add(5*time.Minute).Local().Format("15:04") + ": skip the flaky test. Undo: re-enable it\n"} {
			if !strings.Contains(section, want) {
				t.Errorf("git=%v: section lacks %q:\n%s", git, want, section)
			}
		}
		if strings.Contains(section, "step 2.") || strings.Contains(section, "use staging") {
			t.Errorf("git=%v: section lists an old decision, or one the human made:\n%s", git, section)
		}
		if !git && strings.Contains(strings.ToLower(b[i:]), "commit") { // the plan quoted above may, baton's words may not
			t.Errorf("a brief without git mentions committing:\n%s", b[i:])
		}
	}
}
