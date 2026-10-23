package brief

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
)

// A worker whose worktree holds the merge baton left at its conflicts starts from a brief that opens with
// it, right after who it is, since the message that told of it may have gone to an agent before it. Only
// a worker's brief says so, and only while a merge is in progress.
func TestAWorkersBriefOpensWithItsMergeInProgress(t *testing.T) {
	pl, doc, st := worker(t)
	for _, c := range []struct {
		name  string
		kind  string
		merge *MergeLeft
		want  string // the brief's second paragraph; "" for no merge in it
	}{
		{"unmerged", Worker, &MergeLeft{Branch: "main", Unmerged: []string{"a.go", "b.go"}}, decide.MergeInProgressBrief("R1", "main", []string{"a.go", "b.go"})},
		{"resolved", Worker, &MergeLeft{Branch: "main"}, decide.MergeInProgressBrief("R1", "main", nil)},
		{"no merge", Worker, nil, ""},
		{"not a worker's", Auto, &MergeLeft{Branch: "main", Unmerged: []string{"a.go"}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := Write(Input{Kind: c.kind, Plan: pl, Doc: doc, State: st, Handoff: handoff, Words: decide.Words{Git: true, Timeout: 5 * time.Minute, Worker: true},
				Tree: "/home/me/.baton/worktrees/k/R1", Branch: "baton/run/R1", Needs: []string{"P0"}, Merge: c.merge})
			paras := strings.SplitN(b, "\n\n", 3)
			if c.want == "" {
				if strings.Contains(b, "A merge is in progress") {
					t.Errorf("the brief tells of a merge:\n%s", b)
				}
				return
			}
			if len(paras) < 3 || !strings.HasPrefix(paras[0], "[baton] You are a worker") || paras[1] != c.want || !strings.HasPrefix(paras[2], "Progress:") {
				t.Errorf("the brief opens:\n%s\nwant its second paragraph %q", strings.Join(paras[:min(2, len(paras))], "\n\n"), c.want)
			}
		})
	}
}
