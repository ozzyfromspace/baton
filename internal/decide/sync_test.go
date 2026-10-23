package decide

import (
	"regexp"
	"strings"
	"testing"
)

// gitOnBranches is an instruction to run a git operation on branches, which no model is given: only the
// words that forbid one may name it.
var gitOnBranches = regexp.MustCompile(`git (merge|rebase|reset|push|checkout|switch)\b`)

// Every word a phase agent reads about a merge baton left in its worktree says how to finish it with
// edits, git add and git commit alone, and forbids every git operation on branches; none tells it to run
// one. The worker's brief sends it to baton sync for the run's branch.
func TestTheAgentNeverRunsAMerge(t *testing.T) {
	history := map[string][]ConflictCommit{"a.go": {{Hash: "abc1234", Subject: "parser: x", Phase: "P3"}}}
	for _, c := range []struct {
		name  string
		text  string
		wants []string
	}{
		{"the worker's brief", WorkerIntro("T", "/p.md", "P2", "/wt/P2", "baton/r/P2"),
			[]string{"never run git merge, rebase, reset or push on it", "run baton sync", "baton merges again as it lands your work"}},
		{"a sync's conflict", SyncConflict("P2", "main", []string{"a.go"}, history),
			[]string{"stopped at conflicts in a.go", "abc1234 parser: x (P3's work)", "git add the files, and git commit --no-edit", keptMergeNever, "go on with P2"}},
		{"done over unmerged paths", MergeUnfinished("not done", "done P2", []string{"a.go", "b.go"}, nil),
			[]string{"not done — ", "a.go, b.go still conflict", "git add the files", "git commit --no-edit, or run baton done P2 again", keptMergeNever}},
		{"sync over a marker left", MergeUnfinished("not synced", "sync", nil, []string{"a.go:3"}),
			[]string{"not synced — ", "conflict markers are left at a.go:3, in what is staged", "or run baton sync again", keptMergeNever}},
		{"a merge baton could not commit", MergeNotCommitted("not done", "done P2", "gpg failed"),
			[]string{"(gpg failed)", "git commit --no-edit", "baton done P2 again", keptMergeNever}},
		{"a fresh agent's brief, unmerged", MergeInProgressBrief("P2", "main", []string{"a.go"}),
			[]string{"baton merged main, the run's branch, into P2's branch", "Still in conflict: a.go.", "git commit --no-edit", keptMergeNever}},
		{"a fresh agent's brief, resolved", MergeInProgressBrief("P2", "", nil),
			[]string{"baton merged the run's branch into P2's branch", "Every conflict is resolved and added already: git commit --no-edit finishes it.", keptMergeNever}},
		{"a failed sync", SyncFailed("main", "untracked files in the way"),
			[]string{"changed nothing (git said: untracked files in the way)", "baton merges the run's branch in again as it lands your work"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, want := range c.wants {
				if !strings.Contains(c.text, want) {
					t.Errorf("lacks %q:\n%s", want, c.text)
				}
			}
			if bare := strings.NewReplacer(keptMergeNever, "", "never run git merge, rebase, reset or push", "").Replace(c.text); gitOnBranches.MatchString(bare) {
				t.Errorf("tells the agent to run %q:\n%s", gitOnBranches.FindString(bare), c.text)
			}
		})
	}
}

// What a sync brought in names its commits, the phases whose landings brought them, and at most maxFiles
// files.
func TestSyncedSaysWhatCameIn(t *testing.T) {
	files := make([]string, maxFiles+2)
	for i := range files {
		files[i] = "f" + strings.Repeat("x", i)
	}
	for _, c := range []struct {
		name    string
		commits []string
		phases  []string
		files   []string
		want    string
	}{
		{"one of the lead's", []string{"0123456789"}, nil, []string{"a.go"}, "1 commit came in (0123456), changing a.go. "},
		{"landings", []string{"aaaaaaaaaa", "bbbbbbbbbb"}, []string{"P3", "P4"}, []string{"a.go", "b.go"}, "2 commits came in (aaaaaaa, bbbbbbb), the work of P3 and P4, changing a.go, b.go. "},
		{"many files", []string{"aaaaaaaaaa"}, []string{"P3"}, files, "the work of P3, changing " + strings.Join(files[:maxFiles], ", ") + " and 2 more. "},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Synced("main", "fedcba98765", c.commits, c.phases, c.files)
			if !strings.HasPrefix(got, "baton: merged main, the run's branch, into your branch (now fedcba9): ") || !strings.Contains(got, c.want) {
				t.Errorf("Synced:\n%s\nwant %q", got, c.want)
			}
		})
	}
}
