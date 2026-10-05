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
	}
	var ids []string
	for _, d := range WithoutHuman(ds) {
		ids = append(ids, d.ID)
	}
	if got := strings.Join(ids, " "); got != "d1 d3" {
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
