package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/hostcheck"
)

// The human's /baton replan through the CLI: baton replan opens it and prints the brief, the model edits
// the draft, and baton replanned has baton check it, follow it, and say what to do next; baton status
// lists it, with what it changed against the approved plan.
func TestReplanThroughTheCLI(t *testing.T) {
	s := attached(t)
	planFile := s.store().ReplanText(0)
	if b, err := os.ReadFile(planFile); err != nil || !strings.Contains(string(b), "## R1 — The roster can grow") {
		t.Fatalf("the approved text was not kept at attach: %v", err)
	}
	out := s.must("", "replan", "R1 needs only the overlay")
	for _, want := range []string{
		`Replan the phases of "The widget campaign" that have not started`,
		`The human asked you to consider: "R1 needs only the overlay".`,
		"  P1 The one-liners — after P0\n  R1 The roster can grow — after P1\n",
		"  P0 The overlay — in hand\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("baton replan lacks %q:\n%s", want, out)
		}
	}
	st := s.state()
	if st.Replan == nil || st.Replan.Words != "R1 needs only the overlay" {
		t.Fatalf("replan %+v", st.Replan)
	}
	if again := s.must("", "replan"); !strings.HasPrefix(again, "baton: a replan is open already (you asked, since") {
		t.Errorf("a second baton replan:\n%s", again)
	}
	draft := s.store().DraftPath()
	b, _ := os.ReadFile(draft)
	os.WriteFile(draft, []byte(strings.Replace(string(b), "Add people to a household.", "After: P0\n\nAdd people to a household.", 1)), 0o644)

	s.mustFail("usage: baton replanned", "replanned")
	s.mustFail("usage: baton replanned", "replanned", "--notes", "x", "--none", "y")
	out = s.must("", "replanned", "--notes", "R1 needs only P0")
	if want := "baton: replanned (d1): R1 now after P0 (was after P1). The plan file now reads as your draft, and baton follows it. Carry on with P0 (The overlay) as the plan describes it.\n"; out != want {
		t.Errorf("baton replanned:\n got %q\nwant %q", out, want)
	}
	if !s.logged("plan_replanned", map[string]any{"by": "human", "decision": "d1"}) {
		t.Errorf("events:\n%s", s.events())
	}
	status := s.must("", "status")
	for _, want := range []string{"replans (the plan as approved is kept in .baton/runs/", "you asked: R1 now after P0 (was after P1) (d1)",
		"against the approved plan: R1 now after P0 (was after P1)", "noted: baton replanned the phases not started"} {
		if !strings.Contains(status, want) {
			t.Errorf("baton status lacks %q:\n%s", want, status)
		}
	}
	s.mustFail("no replan is open", "replanned", "--none", "nothing")
}

// --none closes the replan with the plan as it was; an unhosted session opens none.
func TestReplanClosedOrRefused(t *testing.T) {
	s := attached(t)
	s.must("", "replan")
	if out := s.must("", "replanned", "--none", "every link of the chain holds"); out != "baton: the replan is closed, and the plan goes on as it is.\n" {
		t.Errorf("--none: %q", out)
	}
	if !s.logged("replan_declined", map[string]any{"why": "every link of the chain holds"}) || s.state().Replan != nil {
		t.Errorf("events:\n%s", s.events())
	}
	s.host = func() hostcheck.Status { return hostcheck.Plain }
	s.mustFail("no replan opened: this session is not hosted by baton", "replan")
	if s.state().Replan != nil {
		t.Error("an unhosted session opened a replan")
	}
}

// A command that panics midway fails as a hook fails open: it says so, exits 1, and changes nothing more.
func TestACommandThatPanicsFailsMidway(t *testing.T) {
	io, _, errb := testIO("", nil)
	code := func() (code int) {
		defer failedMidway(io, "replanned", &code)
		panic("boom")
	}()
	if code != 1 || !strings.Contains(errb.String(), "baton: replanned failed midway (boom); nothing more changed") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
}

// A dropped plan's replans go with its record.
func TestADroppedPlanKeepsItsReplans(t *testing.T) {
	s := attached(t)
	s.must("", "replan")
	st := s.store()
	s.must("", "drop")
	archives := st.Archives()
	if len(archives) == 0 {
		t.Fatal("nothing archived")
	}
	if _, err := os.Stat(archives[len(archives)-1].Dir + "/replan/0.md"); err != nil {
		t.Errorf("the dropped plan's replan folder is not with its record: %v", err)
	}
	if _, err := os.Stat(st.ReplanDir()); err == nil {
		t.Error("the replan folder is still in the run")
	}
}

// The approved text is what each replan compares with: a plan attached again keeps the old one's replans
// with its record.
func TestAPlanAttachedAgainStartsItsReplansAfresh(t *testing.T) {
	s := attached(t)
	s.must("", "replan")
	st := s.store()
	if _, err := os.Stat(st.DraftPath()); err != nil {
		t.Fatal("no draft")
	}
	pl, _ := st.LoadPlan()
	s.must("", "attach", pl.File, "--suggested", "--replace")
	if _, err := os.Stat(st.DraftPath()); err == nil {
		t.Error("the replaced plan's draft is still in the replan folder")
	}
	if _, err := os.Stat(st.ReplanText(0)); err != nil || s.state().Replan != nil {
		t.Errorf("the new plan's approved text: %v; replan %+v", err, s.state().Replan)
	}
	archives := st.Archives()
	if len(archives) == 0 {
		t.Fatal("nothing archived")
	}
	if _, err := os.Stat(archives[len(archives)-1].Dir + "/replan/draft.md"); err != nil {
		t.Errorf("the replaced plan's replan folder is not with its record: %v", err)
	}
}
