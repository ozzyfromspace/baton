package brief

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
