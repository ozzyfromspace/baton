package brief

import (
	"flag"
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

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// golden compares got with testdata/name, which -update rewrites. Line endings do not count: a Windows
// checkout may turn the golden file's, and the plan document's, into CRLF.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lf := strings.NewReplacer("\r\n", "\n")
	if lf.Replace(got) != lf.Replace(string(want)) {
		t.Errorf("the brief differs from %s (go test ./internal/brief -update rewrites it):\n%s", path, got)
	}
}

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
	if _, err := state.Done(&st, pl, "P0", now, state.DoneOpts{}); err != nil {
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

// A phase taken up by a switch is told why: the phase in hand was set aside while its job runs, not done.
func TestSwitchBrief(t *testing.T) {
	pl, doc, st := setup(t)
	st.Phases["P0"].Status = state.PhaseParked
	st.Run.Compaction.Switch = &state.Switched{From: "P0", To: "P1", Job: "j1"}
	b := Write(Input{Kind: Switch, Plan: pl, Doc: doc, State: st})
	for _, want := range []string{
		"[baton] The context was compacted to switch phases: P0 waits on its job j1, so baton set P0 aside and takes it up again once the job ends. You are running the plan \"The widget campaign\" (/work/plan.md).",
		"⏸ P0 — The overlay (parked)", "▶ P1 — The one-liners", "Now: P1 — The one-liners. Begin it now.",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "phase boundary") {
		t.Errorf("a switch called a phase boundary:\n%s", b)
	}
}

// A phase done by its verification says so: the notes it left were written while its gate still ran.
func TestTheBriefNamesAPhaseVerifiedSince(t *testing.T) {
	pl, doc, st := setup(t)
	for _, c := range []struct {
		status, want string
	}{
		{state.JobPassed, "✓ P0 — The overlay (verified: job j1 passed)\n"},
		{state.JobFailed, "✓ P0 — The overlay\n"}, // done despite it: the red phases say so
	} {
		st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: c.status}}
		if b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st}); !strings.Contains(b, c.want) {
			t.Errorf("%s: brief lacks %q:\n%s", c.status, c.want, b)
		}
	}
}

// The brief ends with how to report the phase and reach the human, in words that fit the project.
func TestBriefEndsWithTheWaysToReport(t *testing.T) {
	pl, doc, st := setup(t)
	git := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st, Words: decide.Words{Git: true}})
	for _, want := range []string{"When P1 is complete and committed, run `baton done P1", "`baton note`", "`baton propose`", "`baton blocked --tried`", "Never type /compact yourself"} {
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

// resumed is a run of testdata/resume.md with a phase of each kind: P0 done, P1 parked while its job runs,
// P2 resumed after its verification failed, P3 verifying, P4 waiting on them all.
func resumed(t *testing.T) (plan.Plan, []byte, state.State) {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("testdata", "resume.md"))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := plan.Build("/work/search.md", doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	st := state.Attach(pl, now, state.Origin{})
	st.Phases["P0"].Status = state.PhaseDone
	st.Phases["P1"] = &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: state.ParkJob, Since: now, Job: "j1", Notes: "Importer written."}}
	st.Phases["P2"] = &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: state.ParkRed, Since: now, Job: "j2",
		Notes: "Index and query done in 4f2a9c1; the ranking test is the slow one."}}
	st.Phases["P3"].Status = state.PhaseVerifying
	st.Jobs = []state.Job{
		{ID: "j0", Phase: "P0", Kind: state.JobVerify, Command: "make lint", Commit: "1a2b3c4d5e6f7a8b", Status: state.JobFailed, Exit: 2, Started: now, Ended: now.Add(3 * time.Minute)}, // done despite it
		{ID: "j1", Phase: "P1", Status: state.JobRunning},
		{ID: "j2", Phase: "P2", Kind: state.JobVerify, Command: "make test", Commit: "4f2a9c1d2e3f4a5b", Status: state.JobFailed, Exit: 1, Started: now, Ended: now.Add(14 * time.Minute)},
		{ID: "j3", Phase: "P3", Status: state.JobRunning},
	}
	st.Current, st.BoundaryOwed = "P2", true
	state.Start(&st, now, state.Origin{})
	return pl, doc, st
}

func TestResumeBrief(t *testing.T) {
	pl, doc, st := resumed(t)
	result := decide.JobResult(*st.Job("j2"), decide.Log{Path: "/work/.baton/runs/r1/jobs/j2.log", Tail: "--- FAIL: TestRanking (0.02s)\nFAIL"}, nil, "")
	b := Write(Input{Kind: Resume, Plan: pl, Doc: doc, State: st, Handoff: "## P0 — 2026-10-04 11:00\n\nTables in 1a2b3c4.\n", Words: decide.Words{Git: true}, Result: result})
	golden(t, "resume.golden", b)
}

// A phase whose verification ended without a verdict is checked again, not fixed.
func TestUnverifiedResumeBrief(t *testing.T) {
	pl, doc, st := resumed(t)
	st.Phases["P2"].Park.Reason = state.ParkUnverified
	b := Write(Input{Kind: Resume, Plan: pl, Doc: doc, State: st})
	if !strings.Contains(b, "Now: resume P2 — Search. It was finished, but its verification ended without a verdict") || strings.Contains(b, "verification failed") {
		t.Errorf("resume brief:\n%s", b)
	}
}

// A phase set aside while its job ran picks up from its park notes, and they stay in every later brief
// of the phase.
func TestResumedPhaseKeepsItsParkNotes(t *testing.T) {
	pl, doc, st := resumed(t)
	st.Phases["P2"].Park.Reason = state.ParkJob
	b := Write(Input{Kind: Resume, Plan: pl, Doc: doc, State: st})
	if !strings.Contains(b, "Now: resume P2 — Search from where it stood.") || strings.Contains(b, "verification failed") {
		t.Errorf("resume brief:\n%s", b)
	}
	for _, kind := range []string{Checkpoint, Auto} {
		b := Write(Input{Kind: kind, Plan: pl, Doc: doc, State: st})
		if !strings.Contains(b, "This phase's park notes (where P2 stood when it was set aside):\n\nIndex and query done in 4f2a9c1") || !strings.Contains(b, "continue P2 — Search from where you left off") {
			t.Errorf("%s brief of a resumed phase:\n%s", kind, b)
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

// worker is the state of a worker's sub-run on R1, in the plan narrowed to it, as it starts.
func worker(t *testing.T) (plan.Plan, []byte, state.State) {
	t.Helper()
	pl, doc, run := setup(t)
	only, err := pl.Only("R1")
	if err != nil {
		t.Fatal(err)
	}
	st := state.AttachWorker(run, "R1")
	state.Start(&st, time.Date(2026, 10, 4, 12, 5, 0, 0, time.UTC), state.Origin{})
	return only, doc, st
}

// handoff holds notes of a plan run before, of a phase R1 does not build on, and of the one it does.
const handoff = "## P0 — 2026-10-03 09:00\n\nAn older plan's P0.\n\n## P1 — 2026-10-04 11:00\n\nOne-liners shipped.\n\n## P0 — 2026-10-04 12:00\n\nOverlay done in abc123.\n"

// A worker starts from a brief of its own: its phase, where it works, the notes of the phases it builds
// on, and that baton lands its work. It never names the project's checkout.
func TestWorkerBrief(t *testing.T) {
	pl, doc, st := worker(t)
	b := Write(Input{Kind: Worker, Plan: pl, Doc: doc, State: st, Handoff: handoff, Words: decide.Words{Git: true, Timeout: 5 * time.Minute, Worker: true},
		Tree: "/home/me/.baton/worktrees/k/R1", Branch: "baton/run/R1", Needs: []string{"P0"}})
	golden(t, "worker.golden", b)
	for _, not := range []string{"An older plan's P0", "One-liners shipped", "compacted", "Add people to a household.\n\n## Verification", "/work\n"} {
		if strings.Contains(b, not) {
			t.Errorf("the worker's brief has %q", not)
		}
	}
}

// An agent taking up a phase where an agent before it left it is told so, and checks what is done before
// it goes on; the rest of its brief is a worker's.
func TestAWorkerTakingUpItsPhase(t *testing.T) {
	pl, doc, st := worker(t)
	in := Input{Kind: Worker, Plan: pl, Doc: doc, State: st, Handoff: handoff, Words: decide.Words{Git: true, Timeout: 5 * time.Minute, Worker: true},
		Tree: "/home/me/.baton/worktrees/k/R1", Branch: "baton/run/R1", Needs: []string{"P0"}}
	first := Write(in)
	in.Again = true
	b := Write(in)
	for _, want := range []string{decide.WorkerTakesUp("R1"), "Now: continue R1 — The roster can grow. Some of it may be done already: check what is (`git log`",
		"Notes left by the phases R1 builds on", "Report progress only with these commands"} {
		if !strings.Contains(b, want) {
			t.Errorf("the brief lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "Begin it now") || strings.Contains(first, decide.WorkerTakesUp("R1")) {
		t.Errorf("taken up:\n%s\nfirst:\n%s", b, first)
	}
}

// After a compaction a worker is reminded what it is, and how its phase ends.
func TestAWorkersBriefAfterACompaction(t *testing.T) {
	pl, doc, st := worker(t)
	for _, kind := range []string{Checkpoint, Auto, Resume} {
		b := Write(Input{Kind: kind, Plan: pl, Doc: doc, State: st, Handoff: handoff, Words: decide.Words{Git: true, Worker: true}, Tree: "/wt/R1", Needs: []string{"P0", "P1"}})
		for _, want := range []string{"You are a worker on R1 of the plan \"The widget campaign\" (/work/plan.md), in a git worktree of your own (/wt/R1)",
			"Notes left by the phases R1 builds on", "One-liners shipped.\n\n## P0 — 2026-10-04 12:00\n\nOverlay done in abc123.", "baton then lands your work on the run's branch"} {
			if !strings.Contains(b, want) {
				t.Errorf("%s: the brief lacks %q:\n%s", kind, want, b)
			}
		}
		if strings.Contains(b, "You are running the plan") || strings.Contains(b, "An older plan's P0") {
			t.Errorf("%s: the brief reads as the lead's:\n%s", kind, b)
		}
	}
}

func TestNotesOf(t *testing.T) {
	for _, c := range []struct {
		ids  []string
		want string
	}{
		{nil, ""},
		{[]string{"P0"}, "## P0 — 2026-10-04 12:00\n\nOverlay done in abc123."},
		{[]string{"P1", "P0"}, "## P1 — 2026-10-04 11:00\n\nOne-liners shipped.\n\n## P0 — 2026-10-04 12:00\n\nOverlay done in abc123."},
		{[]string{"R1"}, ""},
	} {
		if got := notesOf(handoff, c.ids); got != c.want {
			t.Errorf("notesOf(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
}

// The lead's brief marks the phases baton's workers have, and says baton lands them, so the lead leaves
// them alone; the decisions it lists are the whole run's, its workers' too.
func TestTheBriefMarksTheWorkersPhases(t *testing.T) {
	pl, doc, st := setup(t)
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	st.Phases["R1"] = &state.PhaseState{Status: state.PhaseWorking, Worker: "run/R1"}
	worker := state.Decision{ID: "R1.d1", Kind: state.Note, Phase: "R1", What: "used a fake clock", Undo: "revert the commit", At: now}
	b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st, Decisions: []state.Decision{worker}})
	for _, want := range []string{
		"  ⚙ R1 — The roster can grow (a worker's)\n\n" + decide.WorkingPhases([]string{"R1 — The roster can grow"}) + "\n\nNow: P1",
		"- R1.d1 (R1, ",
		"noted: used a fake clock. Undo: revert the commit",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("no %q in:\n%s", want, b)
		}
	}
	if b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st, Decisions: []state.Decision{}}); strings.Contains(b, "Decisions made without the human") {
		t.Errorf("listed decisions the run's record does not have:\n%s", b)
	}
}

// A conversation that starts without its phase's context (a /clear, a rewind past the brief, a fresh
// session after a crash) gets the phase's brief again: the phase goes on, and the model checks what is
// done before it redoes anything, in words that fit the project.
func TestFreshBrief(t *testing.T) {
	pl, doc, st := setup(t)
	at := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	state.Start(&st, at, state.Origin{})
	state.AddNote(&st, "skipped the flaky test", "re-enable it", at)
	notes := "## P0 — 2026-10-04 12:00\n\nOverlay done in abc123.\n\n## P1 (checkpoint) — 2026-10-04 12:40\n\nHalf the one-liners in def456.\n"
	for _, c := range []struct {
		git   bool
		check string
	}{
		{true, "Now: continue P1 — The one-liners. Some of it may be done already: check what is (`git log`, `git status`, and any notes the phase left below) before you redo anything, then go on from there.\n"},
		{false, "Now: continue P1 — The one-liners. Some of it may be done already: check what is (the files the phase is about, and any notes it left below) before you redo anything, then go on from there.\n"},
	} {
		b := Write(Input{Kind: Fresh, Plan: pl, Doc: doc, State: st, Handoff: notes, Words: decide.Words{Git: c.git, Timeout: 5 * time.Minute}})
		for _, want := range []string{
			"[baton] This conversation started without the phase's context (a /clear, a rewind past baton's brief, or a fresh session after a crash), " +
				"so baton gives you the phase's brief again: it goes on from the plan, the notes and the repository. You are running the plan \"The widget campaign\" (/work/plan.md).\n",
			"✓ P0 — The overlay", "▶ P1 — The one-liners",
			c.check,
			"Standing rules (from the plan):", "Stop only for a decision with no fallback",
			"Phase P1, from the plan:\n\n## P1 — The one-liners", "Small fixes.",
			"Overlay done in abc123.", "Half the one-liners in def456.",
			"Decisions made without the human so far", "noted: skipped the flaky test. Undo: re-enable it",
			"When P1 is complete", "Never type /compact",
		} {
			if !strings.Contains(b, want) {
				t.Errorf("git=%v: the brief lacks %q:\n%s", c.git, want, b)
			}
		}
		for _, not := range []string{"compacted", "Begin it now", "Add people to a household"} {
			if strings.Contains(b, not) {
				t.Errorf("git=%v: the brief has %q:\n%s", c.git, not, b)
			}
		}
	}
}

// A worker's fresh brief says what it is and where it works.
func TestAWorkersFreshBrief(t *testing.T) {
	pl, doc, st := worker(t)
	b := Write(Input{Kind: Fresh, Plan: pl, Doc: doc, State: st, Handoff: handoff, Words: decide.Words{Git: true, Worker: true}, Tree: "/wt/R1", Needs: []string{"P0"}})
	for _, want := range []string{
		"so baton gives you the phase's brief again: it goes on from the plan, the notes and the repository. You are a worker on R1 of the plan \"The widget campaign\" (/work/plan.md), in a git worktree of your own (/wt/R1)",
		"Now: continue R1 — The roster can grow. Some of it may be done already",
		"Notes left by the phases R1 builds on", "Overlay done in abc123.", "baton then lands your work on the run's branch",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the brief lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "You are running the plan") || strings.Contains(b, "One-liners shipped") {
		t.Errorf("the brief reads as the lead's:\n%s", b)
	}
}

// A worker's notes go to its run's handoff with everyone's. Its briefs quote those of the phases it
// builds on, and those it left on its own phase since it started: a checkpoint's, or its done notes if
// its verification reopened it. An older plan's phase of the same id left notes before that.
func TestAWorkerIsGivenTheNotesItLeft(t *testing.T) {
	pl, doc, st := worker(t) // R1 started at 12:05
	notes := handoff + "## R1 — 2026-10-03 10:00\n\nAn older plan's R1.\n\n## R1 (checkpoint) — 2026-10-04 12:20\n\nHalf the roster, in 9e8d7c6.\n"
	for _, kind := range []string{Checkpoint, Fresh, Worker} {
		b := Write(Input{Kind: kind, Plan: pl, Doc: doc, State: st, Handoff: notes, Words: decide.Words{Git: true, Worker: true}, Tree: "/wt/R1", Needs: []string{"P0"}})
		if !strings.Contains(b, "Notes R1 left so far (most recent last):\n\n## R1 (checkpoint) — 2026-10-04 12:20\n\nHalf the roster, in 9e8d7c6.\n\n") ||
			strings.Contains(b, "An older plan's R1") || !strings.Contains(b, "Overlay done in abc123.") {
			t.Errorf("%s: the worker's brief:\n%s", kind, b)
		}
	}
}

func TestNotesSince(t *testing.T) {
	notes := "## P2 — 2026-10-03 09:00\n\nolder\n\n## P1 — 2026-10-04 12:10\n\nnot P2's\n\n## P2 (checkpoint) — 2026-10-04 12:05\n\nsame minute\n\n" +
		"## P2 — 2026-10-04 13:00\n\ndone, then reopened\n\n## P2 — not a time\n\nunreadable\n"
	since := time.Date(2026, 10, 4, 12, 5, 30, 0, time.UTC)
	for _, c := range []struct {
		phase string
		since time.Time
		want  string
	}{
		{"P2", since, "## P2 (checkpoint) — 2026-10-04 12:05\n\nsame minute\n\n## P2 — 2026-10-04 13:00\n\ndone, then reopened"},
		{"P2", since.Add(time.Hour), ""},
		{"P2", time.Time{}, ""}, // not started
		{"P1", since, "## P1 — 2026-10-04 12:10\n\nnot P2's"},
		{"P3", since, ""},
	} {
		if got := notesSince(notes, c.phase, c.since); got != c.want {
			t.Errorf("notesSince(%s, %v) = %q, want %q", c.phase, c.since, got, c.want)
		}
	}
}

// While the plan aggregates its heavy checks (aggregate_qa), a phase before a QA phase is told to run
// focused checks only and leave the full ones to it, and a QA phase is told it is the place for them;
// with the setting off, or no QA phase ahead, the brief says nothing of it. A phase agent's narrowed plan
// still names its QA phase.
func TestTheBriefSaysWhereTheHeavyChecksRun(t *testing.T) {
	doc := []byte("# Demo\n\n## P0 — Parser\n\nAfter: none\n\nParse.\n\n## P1 — Printer\n\nPrint.\n\n## P2 — Check it all\n\nAfter: all\nType: qa\n\nRun everything.\n\n## P3 — Ship\n\nShip.\n\n## Verification\n")
	pl, err := plan.Build("/work/plan.md", doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	at := func(id string) state.State {
		st := state.Attach(pl, now, state.Origin{})
		st.Current = id
		return st
	}
	focus := "In this phase run focused checks only"
	for _, c := range []struct {
		name, phase string
		on          bool
		want, not   string
	}{
		{"before the QA phase", "P1", true, "the next is P2 (Check it all)", "this is a QA phase"},
		{"the QA phase", "P2", true, "QA: this is a QA phase. Run the full test suites, lints and the other checks the plan names, and fix what they find in the work of P0 and P1", focus},
		{"after the last QA phase", "P3", true, "", "QA:"},
		{"aggregate_qa off", "P1", false, "", "QA:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: at(c.phase), Words: decide.Words{AggregateQA: c.on}})
			if c.want != "" && !strings.Contains(b, c.want) {
				t.Errorf("brief lacks %q:\n%s", c.want, b)
			}
			if strings.Contains(b, c.not) {
				t.Errorf("brief has %q:\n%s", c.not, b)
			}
		})
	}
	only, err := pl.Only("P1")
	if err != nil {
		t.Fatal(err)
	}
	st := state.Attach(only, now, state.Origin{})
	st.Current = "P1"
	if b := Write(Input{Kind: Worker, Plan: only, Doc: doc, State: st, Words: decide.Words{AggregateQA: true, Worker: true}}); !strings.Contains(b, "the next is P2 (Check it all)") || !strings.Contains(b, focus) {
		t.Errorf("a phase agent's brief:\n%s", b)
	}
}
