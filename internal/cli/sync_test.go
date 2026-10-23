package cli

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// agentFixture is a phase agent on P1 of a run in a git repository: the lead session in the checkout, on
// the run's branch, and the agent in a worktree of its own, on its branch, with P1 started in its sub-run.
type agentFixture struct {
	t          *testing.T
	w, lead    *session
	root, tree string
	branch     string // the run's
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	lead, root := gitSession(t)
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v) // baton's merge makes a commit: the throwaway configuration has no identity
	}
	tree := filepath.Join(t.TempDir(), "P1")
	gittest.Git(t, root, "worktree", "add", "-q", "-b", "baton/sess-1/P1", tree)
	run := lead.store()
	pl, _ := run.LoadPlan()
	p, _ := state.OpenProject(lead.env["BATON_DIR"], nil)
	sub, err := p.NewWorker(run.Ref, "P1", "inst", tree)
	if err != nil {
		t.Fatal(err)
	}
	only, _ := pl.Only("P1")
	sub.SavePlan(only)
	sub.Update(func(x *state.State) error {
		*x = state.AttachWorker(*x, "P1")
		state.Start(x, lead.now, state.Origin{})
		return nil
	})
	w := &session{t: t, now: lead.now, env: maps.Clone(lead.env), dir: sub.Root}
	branch := lead.state().RunBranch
	if branch == "" {
		t.Fatal("the run has no branch")
	}
	return &agentFixture{t: t, w: w, lead: lead, root: root, tree: tree, branch: branch}
}

// commit commits files in dir, and returns the commit.
func (f *agentFixture) commit(dir, msg string, files map[string]string) string {
	f.t.Helper()
	var names []string
	for name, content := range files {
		gittest.Write(f.t, dir, name, content)
		names = append(names, name)
	}
	gittest.Commit(f.t, dir, msg, names...)
	return gitx.Head(dir)
}

// lands commits files on the run's branch, in the lead's checkout, as phase's landing, and returns the
// commit.
func (f *agentFixture) lands(phase, msg string, files map[string]string) string {
	f.t.Helper()
	c := f.commit(f.root, msg, files)
	f.lead.set(func(x *state.State) {
		x.Landed = append(x.Landed, state.Landing{Phase: phase, Commits: []string{c}, Head: c, At: f.lead.now})
	})
	return c
}

// conflicted has the agent's branch and the run's change a.txt each their own way, and returns the run's
// commit.
func (f *agentFixture) conflicted() string {
	f.t.Helper()
	f.commit(f.tree, "P1: a", map[string]string{"a.txt": "a\nagent\n"})
	return f.lands("P2", "P2: a", map[string]string{"a.txt": "a\nlead\n"})
}

// kept leaves baton's merge of the run's branch (at onto) in progress in the agent's worktree, at its
// conflicts, as a landing that conflicted leaves it.
func (f *agentFixture) kept(onto string) {
	f.t.Helper()
	if err := gitx.Merge(context.Background(), f.tree, onto, decide.MergeMessage(f.branch, "baton/sess-1/P1")); err == nil {
		f.t.Fatal("the merge went through; want a conflict")
	}
	if !gitx.Merging(context.Background(), f.tree) {
		f.t.Fatal("no merge in progress")
	}
}

// parents are the parents of the agent's HEAD.
func (f *agentFixture) parents() []string {
	return strings.Fields(gittest.Git(f.t, f.tree, "log", "-1", "--format=%P"))
}

func (f *agentFixture) announced(line string) bool {
	return slices.Contains(f.lead.state().Run.Announce, "baton: "+line)
}

// baton sync brings the run's branch into a phase agent's, by baton's own merge in its worktree: a clean
// merge says what came in, a conflict is left in progress with how to finish it, and a merge already
// there is finished first. Uncommitted changes, a gate running in the worktree and the lead are refused,
// with nothing changed.
func TestSync(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		setup func(f *agentFixture) // returns nothing; check reads what it needs
		lead  bool                  // run in the lead's session
		check func(t *testing.T, f *agentFixture, tip string, code int, out, errs string)
	}{
		{name: "clean", setup: func(f *agentFixture) {
			f.commit(f.tree, "P1: c", map[string]string{"c.txt": "c\n"})
			f.lands("P2", "P2: b", map[string]string{"b.txt": "b\n"})
			gittest.Write(f.t, f.tree, "scratch.txt", "untracked, which a merge leaves be\n")
		}, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			onto, head := gitx.Head(f.root), gitx.Head(f.tree)
			if b, _ := os.ReadFile(filepath.Join(f.tree, "scratch.txt")); string(b) != "untracked, which a merge leaves be\n" {
				t.Errorf("the untracked file is %q", b)
			}
			if want := decide.Synced(f.branch, head, []string{onto}, []string{"P2"}, []string{"b.txt"}); code != 0 || !strings.Contains(out, want) {
				t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if p := f.parents(); !slices.Equal(p, []string{tip, onto}) {
				t.Errorf("HEAD's parents %q, want the agent's tip and the run's", p)
			}
			if b, _ := os.ReadFile(filepath.Join(f.tree, "b.txt")); string(b) != "b\n" {
				t.Errorf("b.txt is %q", b)
			}
			if !f.w.logged("synced", map[string]any{"phase": "P1", "result": "merged", "commits": float64(1), "paths": float64(1), "onto": onto, "commit": head}) {
				t.Errorf("events:\n%s", f.w.events())
			}
			if !f.announced(decide.SyncedLine("P1", f.branch, 1, head)) {
				t.Errorf("the lead is not told: %q", f.lead.state().Run.Announce)
			}
		}},
		{name: "conflict", setup: func(f *agentFixture) { f.conflicted() }, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			onto := gitx.Head(f.root)
			history := map[string][]decide.ConflictCommit{"a.txt": {{Hash: onto[:7], Subject: "P2: a", Phase: "P2"}}}
			if want := "baton: " + decide.SyncConflict("P1", f.branch, []string{"a.txt"}, history); code != 1 || !strings.Contains(errs, want) {
				t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if paths, _ := gitx.Unmerged(ctx, f.tree); !gitx.Merging(ctx, f.tree) || !slices.Equal(paths, []string{"a.txt"}) || gitx.Head(f.tree) != tip {
				t.Errorf("merging %v, unmerged %q, HEAD %s: want the merge kept at its conflict", gitx.Merging(ctx, f.tree), paths, gitx.Head(f.tree))
			}
			if b, _ := os.ReadFile(filepath.Join(f.tree, "a.txt")); !strings.Contains(string(b), "<<<<<<<") {
				t.Errorf("a.txt has no conflict markers: %q", b)
			}
			if !f.w.logged("synced", map[string]any{"phase": "P1", "result": "conflict", "commits": float64(1), "paths": float64(1)}) {
				t.Errorf("events:\n%s", f.w.events())
			}
			if !f.announced(decide.SyncConflictLine("P1", f.branch, []string{"a.txt"})) {
				t.Errorf("the lead is not told: %q", f.lead.state().Run.Announce)
			}
		}},
		{name: "up to date", setup: func(f *agentFixture) {
			f.commit(f.tree, "P1: c", map[string]string{"c.txt": "c\n"})
		}, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			if want := decide.SyncUpToDate(f.branch, gitx.Head(f.root)); code != 0 || !strings.Contains(out, want) {
				t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if gitx.Head(f.tree) != tip || !f.w.logged("synced", map[string]any{"phase": "P1", "result": "up_to_date", "commits": float64(0), "paths": float64(0)}) {
				t.Errorf("HEAD %s (want %s), events:\n%s", gitx.Head(f.tree), tip, f.w.events())
			}
		}},
		{name: "dirty", setup: func(f *agentFixture) {
			f.lands("P2", "P2: b", map[string]string{"b.txt": "b\n"})
			gittest.Write(f.t, f.tree, "a.txt", "a\nnot committed\n")
		}, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			if want := decide.SyncDirty("    a.txt"); code != 1 || !strings.Contains(errs, want) {
				t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if b, _ := os.ReadFile(filepath.Join(f.tree, "a.txt")); gitx.Head(f.tree) != tip || gitx.Merging(ctx, f.tree) || string(b) != "a\nnot committed\n" {
				t.Errorf("HEAD %s (want %s), merging %v, a.txt %q: a refused sync changed the worktree", gitx.Head(f.tree), tip, gitx.Merging(ctx, f.tree), b)
			}
			if !f.w.logged("refused", map[string]any{"command": "sync", "phase": "P1", "why": "uncommitted work", "files": float64(1)}) {
				t.Errorf("events:\n%s", f.w.events())
			}
		}},
		{name: "lead", lead: true, setup: func(f *agentFixture) {
			f.lands("P2", "P2: b", map[string]string{"b.txt": "b\n"})
		}, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			if want := decide.SyncLead(f.branch); code != 1 || !strings.Contains(errs, want) {
				t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if gitx.Head(f.tree) != tip || !f.lead.logged("refused", map[string]any{"command": "sync", "why": "lead"}) {
				t.Errorf("HEAD %s (want %s), events:\n%s", gitx.Head(f.tree), tip, f.lead.events())
			}
		}},
		{name: "a gate here", setup: func(f *agentFixture) {
			f.lands("P2", "P2: b", map[string]string{"b.txt": "b\n"})
			f.w.set(func(x *state.State) {
				state.QueueJob(x, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make e2e", Here: true}, f.w.now)
			})
		}, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			if want := decide.SyncHere("P1.j1"); code != 1 || !strings.Contains(errs, want) {
				t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if gitx.Head(f.tree) != tip || !f.w.logged("refused", map[string]any{"command": "sync", "why": "gate here", "job": "P1.j1"}) {
				t.Errorf("HEAD %s (want %s), events:\n%s", gitx.Head(f.tree), tip, f.w.events())
			}
		}},
		{name: "a merge left unresolved", setup: func(f *agentFixture) { f.kept(f.conflicted()) }, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			if want := decide.MergeUnfinished("not synced", "sync", []string{"a.txt"}, nil); code != 1 || !strings.Contains(errs, want) {
				t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
			}
			if !gitx.Merging(ctx, f.tree) || !f.w.logged("refused", map[string]any{"command": "sync", "why": "merge unfinished"}) {
				t.Errorf("merging %v, events:\n%s", gitx.Merging(ctx, f.tree), f.w.events())
			}
		}},
		{name: "a merge left resolved", setup: func(f *agentFixture) {
			f.kept(f.conflicted())
			gittest.Write(f.t, f.tree, "a.txt", "a\nagent\nlead\n")
			gittest.Git(f.t, f.tree, "add", "a.txt")
		}, check: func(t *testing.T, f *agentFixture, tip string, code int, out, errs string) {
			head, onto := gitx.Head(f.tree), gitx.Head(f.root)
			for _, want := range []string{decide.MergeConcluded(head), decide.SyncUpToDate(f.branch, onto)} {
				if code != 0 || !strings.Contains(out, want) {
					t.Errorf("exit %d, says:\n%s%s\nwant %q", code, out, errs, want)
				}
			}
			if p := f.parents(); gitx.Merging(ctx, f.tree) || !slices.Equal(p, []string{tip, onto}) {
				t.Errorf("merging %v, HEAD's parents %q: want the merge committed", gitx.Merging(ctx, f.tree), p)
			}
			if !f.w.logged("merge_concluded", map[string]any{"phase": "P1", "by": "sync", "commit": head}) || !f.announced(decide.MergeConcludedLine("P1", head)) {
				t.Errorf("events:\n%s\nthe lead's announcements: %q", f.w.events(), f.lead.state().Run.Announce)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newAgentFixture(t)
			c.setup(f)
			tip := gitx.Head(f.tree)
			s := f.w
			if c.lead {
				s = f.lead
			}
			code, out, errs := s.run("", "sync")
			c.check(t, f, tip, code, out, errs)
		})
	}
}
