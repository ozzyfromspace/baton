package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestRewound(t *testing.T) {
	since := time.Date(2026, 10, 12, 14, 2, 0, 0, time.Local)
	deadline := time.Date(2026, 10, 12, 14, 30, 0, 0, time.Local)
	const opening = "[baton] The human rewound this conversation (Esc Esc) to before 14:02: what came after that is gone from it, " +
		"but not from baton's record or from the files on disk. baton does not roll anything back for a rewind."
	const goOn = "\n\nGo on from the human's message with all this as it stands. If they want any of it undone, that is theirs to ask for, and each decision has its undo."
	ended := since.Add(3 * time.Minute)
	for _, c := range []struct {
		name string
		git  bool
		r    Rewind
		want string
	}{
		{"decisions", false, Rewind{Since: since, Decisions: []state.Decision{
			{ID: "d1", Kind: state.Note, What: "Committed P2 unsigned.", Undo: "re-sign it"},
			{ID: "d2", Kind: state.Proposal, What: "skip the e2e run", Undo: "run it later"},
			{ID: "d3", Kind: state.Proposal, What: "pin go 1.25", Resolved: &state.Resolution{By: state.ByHuman, Answer: "Go ahead"}},
			{ID: "d4", Kind: state.Proposal, What: "drop the cache", Deadline: deadline, Resolved: &state.Resolution{By: state.ByTimeout}},
			{ID: "d5", Kind: state.Proposal, What: "use the mirror", Resolved: &state.Resolution{By: state.ByHeld}},
			{ID: "d6", Kind: state.Proposal, What: "retry", Resolved: &state.Resolution{By: state.ByPaused}},
		}}, opening + " What it recorded since then still stands:\n\n" +
			"- d1, a note: Committed P2 unsigned. Undo: re-sign it.\n" +
			"- d2, a proposal: skip the e2e run; it waits for the human's answer. Undo: run it later.\n" +
			"- d3, a proposal: pin go 1.25; the human answered it: Go ahead. Undo: none was given.\n" +
			"- d4, a proposal: drop the cache; nobody answered it by 14:30, so baton went ahead with it. Undo: none was given.\n" +
			"- d5, a proposal: use the mirror; it is held for the human: do not act on it. Undo: none was given.\n" +
			"- d6, a proposal: retry; the human paused baton over it. Undo: none was given." + goOn},
		{"jobs", false, Rewind{Since: since, Jobs: []state.Job{
			{ID: "j1", Phase: "P2", Kind: state.JobVerify, Command: "make test", Commit: "abc1234", Status: state.JobRunning},
			{ID: "j2", Phase: "P2", Kind: state.JobAwait, Command: "gh run watch", Status: state.JobPassed, Started: since, Ended: ended},
			{ID: "j3", Phase: "P2", Command: "make e2e", Commit: "def5678", Status: state.JobFailed, Exit: 1, Started: since, Ended: ended, Delivered: ended, Log: "/r/jobs/j3.log"},
			{ID: "j4", Phase: "P2", Command: "make lint", Status: state.JobPassed, Started: since, Ended: ended, Delivered: ended},
		}}, opening + " What it recorded since then still stands:\n\n" +
			"- Job j1 (P2, verify `make test` on abc1234) still runs: baton gives you its result when it ends.\n" +
			"- Job j2 (P2, await `gh run watch`) passed after 3m: baton gives you its result.\n" +
			"- Job j3 (P2, verify `make e2e` on def5678) failed (exit 1) after 3m. You were given its result before the rewind; its log is /r/jobs/j3.log.\n" +
			"- Job j4 (P2, verify `make lint`) passed after 3m. You were given its result before the rewind." + goOn},
		{"a phase done, and a block", false, Rewind{Since: since, Done: []string{"P1"}, Blocked: "the signing key is gone."},
			opening + " What it recorded since then still stands:\n\n" +
				"- P1 is done on baton's record (you ran `baton done P1`).\n" +
				"- The phase is blocked on baton's record (the signing key is gone): it waits for the human." + goOn},
		{"files, with git", true, Rewind{Since: since, Files: []string{"a.go", "b/c.go"}},
			opening + "\n\nFiles you edited since then, which are as they are on disk now: a.go, b/c.go. The human may have restored them with the rewind, or not: " +
				"check `git status` and `git diff` before you rely on what you remember of them." + goOn},
		{"files, without git", false, Rewind{Since: since, Files: []string{"a.go"}},
			opening + "\n\nFiles you edited since then, which are as they are on disk now: a.go. The human may have restored them with the rewind, or not: " +
				"check them before you rely on what you remember of them." + goOn},
		// The brief comes alone when nothing else of the record changed.
		{"the brief", false, Rewind{Since: since, Brief: "THE BRIEF"},
			opening + "\n\nThe part rewound away held baton's brief, which this conversation no longer has, so here it is again:\n\nTHE BRIEF"},
	} {
		if got := (Words{Git: c.git}).Rewound(c.r); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestRewindAny(t *testing.T) {
	for _, r := range []Rewind{{Decisions: []state.Decision{{}}}, {Jobs: []state.Job{{}}}, {Done: []string{"P1"}}, {Blocked: "x"}, {Files: []string{"a"}}, {Brief: "b"}} {
		if !r.Any() {
			t.Errorf("%+v: nothing to tell", r)
		}
	}
	if (Rewind{Since: time.Now()}).Any() {
		t.Error("an empty rewind has something to tell")
	}
}
