package decide

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestTheRecordSaysWhatHappenedAndHowToUndoIt(t *testing.T) {
	at := time.Date(2026, 10, 5, 15, 3, 0, 0, time.UTC)
	deadline := at.Add(5 * time.Minute)
	stamp := func(t time.Time) string { return t.Local().Format("Jan 2 15:04") }
	for _, c := range []struct {
		name string
		d    state.Decision
		want string
	}{
		{"a note", state.Decision{ID: "d1", Kind: state.Note, Phase: "P4", What: "committed P4 unsigned.", Undo: "git commit --amend --no-edit -S", At: at},
			"d1 (P4, " + stamp(at) + ") noted: committed P4 unsigned. Undo: git commit --amend --no-edit -S"},
		{"a note with no undo", state.Decision{ID: "d2", Kind: state.Note, Phase: "P4", What: "skipped the flaky test", At: at},
			"d2 (P4, " + stamp(at) + ") noted: skipped the flaky test. Undo: none was given"},
		{"a proposal nobody answered", state.Decision{ID: "d3", Kind: state.Proposal, Phase: "P5", What: "commit unsigned", Undo: "re-sign it", At: at, Deadline: deadline,
			Resolved: &state.Resolution{By: state.ByTimeout, Answer: "Go ahead", At: deadline.Add(time.Minute)}},
			"d3 (P5, " + stamp(deadline.Add(time.Minute)) + ") went ahead, as nobody answered by " + deadline.Local().Format("15:04") + ": commit unsigned. Undo: re-sign it"},
		{"a proposal held for the human, whose hold lapsed", state.Decision{ID: "d4", Kind: state.Proposal, Phase: "P5", What: "commit unsigned", Undo: "re-sign it", At: at,
			Deadline: deadline, Lapsed: deadline.Add(20 * time.Minute), Resolved: &state.Resolution{By: state.ByTimeout, Answer: "Go ahead", At: deadline.Add(20 * time.Minute)}},
			"d4 (P5, " + stamp(deadline.Add(20*time.Minute)) + ") went ahead (or a more reversible way did) once its hold for you lapsed at " +
				deadline.Add(20*time.Minute).Local().Format("15:04") + ", as nobody answered: commit unsigned. Undo: re-sign it"},
	} {
		if got := Entry(c.d); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestOnlyDecisionsMadeWithoutTheHumanAreRecorded(t *testing.T) {
	ds := []state.Decision{
		{ID: "d1", Kind: state.Note},
		{ID: "d2", Kind: state.Proposal, Resolved: &state.Resolution{By: state.ByHuman, Answer: "Go ahead"}},
		{ID: "d3", Kind: state.Proposal, Resolved: &state.Resolution{By: state.ByTimeout, Answer: "Go ahead"}},
		{ID: "d4", Kind: state.Proposal, Resolved: &state.Resolution{By: state.ByHeld}},
		{ID: "d5", Kind: state.Proposal}, // still pending
		// Answered Not now for the human: not done on baton's word (what the model did of it, it noted).
		{ID: "d6", Kind: state.Proposal, Resolved: &state.Resolution{By: state.ByTimeout, Answer: state.NotNow}},
		{ID: "d7", Kind: state.Proposal, Lapsed: time.Now(), Resolved: &state.Resolution{By: state.ByTimeout, Answer: "Go ahead"}},
	}
	var ids []string
	for _, d := range WithoutHuman(ds) {
		ids = append(ids, d.ID)
	}
	if got := strings.Join(ids, " "); got != "d1 d3 d7" {
		t.Errorf("without the human: %s", got)
	}
	if Tally(1) != "1 decision made without you" || Tally(3) != "3 decisions made without you" {
		t.Errorf("tally: %q, %q", Tally(1), Tally(3))
	}
}

// A list for the model shows the newest ones and counts the rest, which baton status has.
func TestListsKeepTheNewest(t *testing.T) {
	var ds []state.Decision
	for i := 1; i <= 12; i++ {
		ds = append(ds, state.Decision{ID: fmt.Sprintf("d%d", i), Kind: state.Note, Phase: "P1", What: fmt.Sprintf("step %d", i)})
	}
	got := Listed(ds, MostListed)
	if lines := strings.Split(strings.TrimSpace(got), "\n"); len(lines) != 11 || lines[0] != "- …and 2 earlier (baton status lists every one)" ||
		!strings.HasPrefix(lines[1], "- d3 (P1,") || !strings.HasPrefix(lines[10], "- d12 (P1,") {
		t.Errorf("newest 10:\n%s", got)
	}
	if all := Listed(ds, 0); strings.Contains(all, "earlier") || !strings.Contains(all, "- d1 (P1,") {
		t.Errorf("no limit:\n%s", all)
	}
	for _, text := range []string{RecordSection(ds), LateAnswer(ds)} {
		if strings.Contains(strings.ToLower(text), "commit") {
			t.Errorf("the record asks for a commit:\n%s", text)
		}
	}
}

// A proposal answered Not now, by baton or by the human, is left for the human unless the model did it as
// the safe kind and noted it: baton status lists it, and the plan's end names it, to the human and to the
// model that sums the run up.
// A plan of one phase reads as one: "1 phase", never "1 phases".
func TestOnePhaseIsCountedAsOne(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{PhaseCount(1), "1 phase"},
		{PhaseCount(12), "12 phases"},
		{PlanDone(1, 0, 0), "its 1 phase done"},
		{PlanDone(1, 1, 0), "its 1 phase done · 1 decision made without you — /baton status"},
		{AttachedAtBoundary("T", 1, "/p.md", "P0"), `[baton] baton attached the plan the human just approved: "T", 1 phase (/p.md).`},
		{Words{}.AttachedNow("T", 1, "/p.md", "P0", "Fix it"), `[baton] baton attached the plan you were just given: "T", 1 phase (/p.md),`},
		{RunCarried("T", "s1", 0, 1, "P0"), `baton: carried on the run of "T" that session s1 left with no baton host: 0 of 1 phase done, at P0.`},
	} {
		if !strings.HasPrefix(c.got, c.want) {
			t.Errorf("%q, want it to start %q", c.got, c.want)
		}
	}
}

func TestProposalsAnsweredNotNowAreLeftForTheHuman(t *testing.T) {
	deadline := time.Date(2026, 10, 15, 13, 40, 0, 0, time.Local)
	ds := []state.Decision{
		{ID: "d1", Kind: state.Note, What: "skipped the flaky test"},
		{ID: "d2", Kind: state.Proposal, What: "push the tag v1.2.", Deadline: deadline, Resolved: &state.Resolution{By: state.ByTimeout, Answer: state.NotNow}},
		{ID: "d3", Kind: state.Proposal, What: "commit unsigned", Resolved: &state.Resolution{By: state.ByTimeout, Answer: "Go ahead"}},
		{ID: "P3.d1", Kind: state.Proposal, What: "email the vendor", Resolved: &state.Resolution{By: state.ByHuman, Answer: state.NotNow}},
		{ID: "d4", Kind: state.Proposal, What: "deploy", Lapsed: deadline.Add(20 * time.Minute), Resolved: &state.Resolution{By: state.ByTimeout, Answer: state.NotNow}},
		{ID: "d5", Kind: state.Proposal, What: "publish", Outward: "x"}, // still pending
	}
	var ids []string
	for _, d := range LeftForYou(ds) {
		ids = append(ids, d.ID)
	}
	if got := strings.Join(ids, " "); got != "d2 P3.d1 d4" {
		t.Errorf("left for you: %s", got)
	}
	for _, c := range []struct {
		d    state.Decision
		want string
	}{
		{ds[1], "nobody answered by 13:40, so baton answered Not now: done only if it was safely reversible and stayed in the project (a note then says so), otherwise left for you — push the tag v1.2"},
		{ds[3], "you answered Not now: done only if"},
		{ds[4], "its hold for you lapsed at 14:00, so baton answered Not now"},
	} {
		if got := LeftHow(c.d); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: %q, want it to start %q", c.d.ID, got, c.want)
		}
	}

	for _, c := range []struct {
		decided, left int
		want          string
	}{
		{0, 0, "all 4 phases done"},
		{2, 0, "all 4 phases done · 2 decisions made without you — /baton status"},
		{0, 1, "all 4 phases done · 1 proposal left for you — /baton status"},
		{1, 2, "all 4 phases done · 1 decision made without you · 2 proposals left for you — /baton status"},
	} {
		if got := PlanDone(4, c.decided, c.left); got != c.want {
			t.Errorf("plan done (%d, %d): %q, want %q", c.decided, c.left, got, c.want)
		}
	}

	got := PlanComplete(ds)
	for _, want := range []string{"including the 2 decisions this run made without them",
		"Name the proposals answered Not now, which are left for them unless it was done as safely reversible and noted: d2 (push the tag v1.2); P3.d1 (email the vendor); d4 (deploy)",
		"Then end your turn."} {
		if !strings.Contains(got, want) {
			t.Errorf("plan complete lacks %q:\n%s", want, got)
		}
	}
	if got := PlanComplete(ds[:1]); got != "Summarize the results for the human, including the 1 decision this run made without them (baton status lists them, each with its undo), and end your turn." {
		t.Errorf("plan complete, none left: %q", got)
	}
	if got := VerifiedComplete(nil); !strings.HasSuffix(got, "the plan is complete. Summarize the results for the human, and end your turn.") {
		t.Errorf("verified complete: %q", got)
	}
}
