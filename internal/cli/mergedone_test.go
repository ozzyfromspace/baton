package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's baton done over the merge baton left in its worktree commits it once every file is
// resolved and added, and goes on; while a path is unmerged or a conflict marker is staged, it is refused,
// saying how to finish the merge, whatever --keep-dirty says.
func TestDoneOverAMergeInProgress(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		resolved string // a.txt as the agent leaves it, added ("": left as the merge left it)
		args     []string
		unmerged []string
		markers  []string
	}{
		{name: "resolved", resolved: "a\nagent\nlead\n"},
		{name: "unmerged", unmerged: []string{"a.txt"}},
		{name: "a marker left", resolved: "a\n<<<<<<< HEAD\nagent\nlead\n", markers: []string{"a.txt:2"}},
		{name: "--keep-dirty", args: []string{"--keep-dirty", "the merge is half done"}, unmerged: []string{"a.txt"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newAgentFixture(t)
			onto := f.conflicted()
			tip := gitx.Head(f.tree)
			f.kept(onto)
			if c.resolved != "" {
				gittest.Write(t, f.tree, "a.txt", c.resolved)
				gittest.Git(t, f.tree, "add", "a.txt")
			}
			code, out, errs := f.w.run("", append([]string{"done", "P1", "--notes", "the merge is resolved"}, c.args...)...)
			if c.unmerged != nil || c.markers != nil {
				if want := decide.MergeUnfinished("not done", "done P1", c.unmerged, c.markers); code != 1 || !strings.Contains(errs, want) {
					t.Fatalf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
				}
				if !gitx.Merging(ctx, f.tree) || gitx.Head(f.tree) != tip {
					t.Errorf("merging %v, HEAD %s (want %s): a refused done changed the merge", gitx.Merging(ctx, f.tree), gitx.Head(f.tree), tip)
				}
				if st := f.w.state(); st.Mode != state.ModeRunning || st.Phases["P1"].Status != state.PhaseActive {
					t.Errorf("the agent's run is %s, P1 %s: want P1 still in hand", st.Mode, st.Phases["P1"].Status)
				}
				if !f.w.logged("refused", map[string]any{"command": "done", "phase": "P1", "why": "merge unfinished"}) {
					t.Errorf("events:\n%s", f.w.events())
				}
				return
			}
			head := gitx.Head(f.tree)
			if want := decide.MergeConcluded(head); code != 0 || !strings.Contains(out, want) {
				t.Fatalf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if p := f.parents(); gitx.Merging(ctx, f.tree) || !slices.Equal(p, []string{tip, onto}) {
				t.Errorf("merging %v, HEAD's parents %q: want the merge committed", gitx.Merging(ctx, f.tree), p)
			}
			if msg := gittest.Git(t, f.tree, "log", "-1", "--format=%s"); msg != decide.MergeMessage(f.branch, "baton/sess-1/P1") {
				t.Errorf("the merge's message is %q", msg)
			}
			if st := f.w.state(); st.Mode != state.ModeComplete {
				t.Errorf("the agent's run is %s after done, want complete", st.Mode)
			}
			if !f.w.logged("merge_concluded", map[string]any{"phase": "P1", "by": "done", "commit": head}) || !f.w.logged("phase_done", map[string]any{"phase": "P1"}) {
				t.Errorf("events:\n%s", f.w.events())
			}
			if !f.announced(decide.MergeConcludedLine("P1", head)) {
				t.Errorf("the lead is not told: %q", f.lead.state().Run.Announce)
			}
		})
	}
}
