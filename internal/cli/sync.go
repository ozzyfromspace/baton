package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// `baton sync` brings the work landed on the run's branch into a phase agent's branch, by baton's own merge
// in the agent's worktree (gitx.Merge), the one its landing runs: no model runs git merge (decide/sync.go).
// It merges only into a worktree with no uncommitted change to a tracked file (untracked files a merge
// leaves be, and git refuses one in its way before it changes anything), and with no gate of the agent's
// running there.
// A merge that stops on conflicts is left in progress for the agent, which resolves the files, adds them
// and commits, or has its next `baton sync` or `baton done` commit them (mergedone.go). The lead's
// checkout is on the run's branch itself, so the lead is refused it.

func init() {
	register("sync", "a phase agent's: bring the work landed on the run's branch into your branch, by baton's own merge in your worktree "+
		"(commit your changes first; a conflict is left in progress for you to resolve, add and commit)", cmdSync)
}

func cmdSync(args []string, io IO) int {
	p, err := parseArgs(args, nil, nil)
	if err != nil || len(p.pos) != 0 {
		return fail(io, "usage: baton sync%s", errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	phase := s.Worker()
	refuse := func(why, text string, fields map[string]any) int {
		ev := map[string]any{"command": "sync", "why": why}
		if phase != "" {
			ev["phase"] = phase
		}
		for k, v := range fields {
			ev[k] = v
		}
		s.Event("refused", ev)
		return fail(io, "%s", text)
	}
	if phase == "" {
		cur, _ := s.Load()
		return refuse("lead", decide.SyncLead(cur.RunBranch), nil)
	}
	up, err := s.ParentRun()
	if err != nil {
		return fail(io, "%v", err)
	}
	lead, err := up.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	branch := lead.RunBranch
	if branch == "" {
		return refuse("no run branch", decide.SyncNoBranch(phase), nil)
	}
	cur, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	if j := cur.HereGate(); j != nil {
		return refuse("gate here", decide.SyncHere(j.ID), map[string]any{"job": j.ID})
	}
	if code, on := concludeMerge(io, s, "sync", phase); !on {
		return code
	}
	ctx, cancel := context.WithTimeout(context.Background(), mergeWait)
	defer cancel()
	dirty, err := gitx.Uncommitted(ctx, s.Root)
	if err != nil {
		return fail(io, "%v", err)
	}
	if len(dirty) > 0 {
		return refuse("uncommitted work", decide.SyncDirty(fileList(dirty)), map[string]any{"files": len(dirty)})
	}
	onto, tip := gitx.RevParse(s.Root, "refs/heads/"+branch), gitx.Head(s.Root)
	if onto == "" || tip == "" {
		return refuse("merge failed", decide.SyncFailed(branch, fmt.Sprintf("no commit at %s or at your HEAD", branch)), nil)
	}
	if in, err := gitx.IsAncestor(ctx, s.Root, onto, tip); err != nil {
		return fail(io, "%v", err)
	} else if in {
		s.Event("synced", map[string]any{"phase": phase, "result": "up_to_date", "onto": onto, "commits": 0, "paths": 0})
		fmt.Fprintln(io.Out, decide.SyncUpToDate(branch, onto))
		return 0
	}
	commits, _ := gitx.Commits(ctx, s.Root, tip, onto)
	err = gitx.Merge(ctx, s.Root, onto, decide.MergeMessage(branch, gitx.Branch(s.Root)))
	var me *gitx.MergeError
	switch {
	case errors.As(err, &me) && me.Kept:
		s.Event("synced", map[string]any{"phase": phase, "result": "conflict", "onto": onto, "commits": len(commits), "paths": len(me.Paths), "conflicts": me.Paths})
		tellLead(s, decide.SyncConflictLine(phase, branch, me.Paths))
		return fail(io, "%s", decide.SyncConflict(phase, branch, me.Paths, syncHistory(ctx, s.Root, lead, onto, tip, me.Paths)))
	case errors.As(err, &me):
		return refuse("merge failed", decide.SyncFailed(branch, me.Msg), map[string]any{"error": me.Msg})
	case err != nil:
		return refuse("merge failed", decide.SyncFailed(branch, err.Error()), map[string]any{"error": err.Error()})
	}
	head := gitx.Head(s.Root)
	files, _ := gitx.ChangedBetween(ctx, s.Root, tip, head)
	s.Event("synced", map[string]any{"phase": phase, "result": "merged", "onto": onto, "commit": head, "commits": len(commits), "paths": len(files)})
	tellLead(s, decide.SyncedLine(phase, branch, len(commits), head))
	fmt.Fprintln(io.Out, decide.Synced(branch, head, commits, landedBy(lead, commits), files))
	return 0
}

// landedBy lists the phases whose landings on the run's branch (lead, the run's state) brought commits,
// in their order, each once. The lead's own commits have none.
func landedBy(lead state.State, commits []string) []string {
	by := landings(lead)
	var phases []string
	seen := map[string]bool{}
	for _, c := range commits {
		if ph := by[c]; ph != "" && !seen[ph] {
			seen[ph] = true
			phases = append(phases, ph)
		}
	}
	return phases
}

// landings maps each commit that a landing brought onto the run's branch to its phase.
func landings(lead state.State) map[string]string {
	by := map[string]string{}
	for _, l := range lead.Landed {
		for _, c := range l.Commits {
			by[c] = l.Phase
		}
	}
	return by
}

// syncHistory is, under each of paths, the commits of the run's branch (at onto) that changed it since it
// last met the agent's branch (at tip), newest first, each with the phase whose landing brought it, as a
// landing's conflict lists them (loop/conflict.go): none when git cannot say.
func syncHistory(ctx context.Context, root string, lead state.State, onto, tip string, paths []string) map[string][]decide.ConflictCommit {
	base, err := gitx.MergeBase(ctx, root, onto, tip)
	if err != nil {
		return nil
	}
	logs, err := gitx.LogPaths(ctx, root, base, onto, paths)
	if err != nil {
		return nil
	}
	by := landings(lead)
	out := make(map[string][]decide.ConflictCommit, len(logs))
	for p, cs := range logs {
		for _, c := range cs {
			out[p] = append(out[p], decide.ConflictCommit{Hash: c.Short(), Subject: c.Subject, Phase: by[c.Hash]})
		}
	}
	return out
}
