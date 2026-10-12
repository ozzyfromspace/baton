package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// worktreeTimeout bounds the git commands that make a worktree: checking the project out takes a while.
const worktreeTimeout = 2 * time.Minute

// worktreeCreate makes the worktree Claude Code asks for, and prints its path. A WorktreeCreate hook takes
// the place of Claude Code's own making of one, in the whole session (docs/research/visible-workers.md),
// so it makes every worktree the hosted session asks for.
//
// The one for the phase agent baton's mod is starting is that phase's (phaseWorktree). Claude Code names
// neither the agent's type nor the call it comes from, so it is the phase's only while the mod's call is
// open (Runtime.Spawning, from its PreToolUse to its result) and no call of the model's own may ask for a
// worktree too (Runtime.AgentCallsOpen: each one's PreToolUse comes before its worktree), and the bridge
// must have that phase's spawn open. Any other is made where Claude Code makes one (plainWorktree). Neither
// is ever mistaken for the other: a worktree baton cannot tell for a phase's is not a phase's, and a phase
// agent that starts in one is ended, and its phase started again later (loop.Coordinator.spawnAcked).
func (h *handlers) worktreeCreate(c Context) (res Result, err error) {
	name := str(c.Input, "name")
	defer func() {
		if r := recover(); r != nil {
			// Whatever went wrong, the agent that asked gets a worktree, where Claude Code would have made it.
			fmt.Fprintf(os.Stderr, "baton: hook WorktreeCreate failed open after a panic: %v\n", r)
			res, err = h.plainWorktree(c, nil, name)
		}
	}()
	s, err := h.d.Open(c)
	if err != nil {
		return h.plainWorktree(c, nil, name)
	}
	agent, isAgent := strings.CutPrefix(name, "agent-")
	st, err := s.Load()
	sp := st.Run.Spawning
	if err != nil || !isAgent || sp == nil || st.Run.AgentCallsOpen(c.Now) || s.Worker() != "" {
		return h.plainWorktree(c, s, name)
	}
	return h.phaseWorktree(s, st, *sp, agent)
}

// phaseWorktree gives the phase agent agent, which baton's mod is starting as sp says, its phase's
// worktree: the one its worker on the run's roster names, made from the run's branch (gitx.AddBranch, as a
// worker's always was), or kept from an agent before it on the phase. The agent is linked to the phase's
// sub-run (Project.LinkAgent) before Claude Code starts it, so that its first hook finds its phase. If
// baton cannot place it, no worktree is made: the agent does not start, the spawn fails, and the host tries
// again later. It never starts anywhere else.
func (h *handlers) phaseWorktree(s *state.Store, st state.State, sp state.Spawning, agent string) (Result, error) {
	fail := func(why string) (Result, error) {
		s.Event("agent_unplaced", map[string]any{"phase": sp.Phase, "agent": agent, "why": why})
		return Result{Fail: fmt.Sprintf("no worktree for the agent on %s: %s", sp.Phase, why)}, nil
	}
	rec, err := bridge.Open(s.Dir, s.Now, nil).Peek()
	if err != nil {
		return fail("baton could not read its bridge: " + err.Error())
	}
	if open, ok := rec.PendingSpawn(s.Instance); !ok || open.Phase != sp.Phase {
		return fail("baton has no spawn open for it")
	}
	e := st.WorkerOn(sp.Phase)
	if e == nil || e.Worktree == "" || e.Branch == "" {
		return fail("the run's roster has no worker on it")
	}
	kept := isDir(e.Worktree)
	if kept && !gitx.Usable(e.Worktree) {
		return fail(e.Worktree + " is there, and is not a git worktree")
	}
	if !kept {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
		defer cancel()
		if _, err := gitx.AddBranch(ctx, s.Checkout, e.Worktree, e.Branch, e.Base); err != nil {
			return fail(err.Error())
		}
	}
	p, err := state.OpenProject(s.Project, s.Now)
	if err == nil {
		err = p.LinkAgent(s.Ref, sp.Phase, agent)
	}
	if err != nil {
		return fail("baton could not link it to its phase: " + err.Error())
	}
	if _, err := s.Update(func(st *state.State) error {
		if w := st.WorkerOn(sp.Phase); w != nil && w.Ref == e.Ref && w.Assigned.Equal(e.Assigned) {
			w.AgentID = agent
		}
		if cur := st.Run.Spawning; cur != nil && cur.ToolUse == sp.ToolUse {
			st.Run.Spawning = nil
		}
		return nil
	}); err != nil {
		return fail("baton could not record it: " + err.Error())
	}
	s.Event("agent_placed", map[string]any{"phase": sp.Phase, "agent": agent, "worktree": e.Worktree, "branch": e.Branch, "kept": kept})
	return Result{Stdout: e.Worktree}, nil
}

// plainWorktree makes the worktree name for an agent that is not one of baton's phases, where Claude Code
// would have: under .claude/worktrees at the repository's root, on a new branch worktree-<name>, from the
// repository's default branch (or HEAD, by worktree.baseRef). s (nil: none) is the run that keeps a record
// of it, so that once its agent stops it goes as Claude Code's own go (plainStopped).
func (h *handlers) plainWorktree(c Context, s *state.Store, name string) (Result, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return Result{Fail: fmt.Sprintf("no worktree named %q", name)}, nil
	}
	cwd := str(c.Input, "cwd")
	root := gitx.MainRoot(cwd)
	if root == "" {
		return Result{Fail: "no worktree: " + cwd + " is not in a git repository"}, nil
	}
	dir := filepath.Join(root, ".claude", "worktrees", name)
	branch := "worktree-" + name
	state.ExcludeWorktrees(root) // best effort, as Claude Code's own
	base := plainBase(root, cwd)
	if base == "" {
		return Result{Fail: "no worktree: the repository has no commit to start one from"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
	defer cancel()
	if _, err := gitx.AddBranch(ctx, root, dir, branch, base); err != nil {
		return Result{Fail: "no worktree: " + err.Error()}, nil
	}
	if s != nil {
		t := state.PlainTree{Name: name, Path: dir, Branch: branch, Base: base, At: c.Now}
		s.Update(func(st *state.State) error {
			st.Run.MadePlain(t)
			if st.Run.Spawning == nil {
				st.Run.AgentPlaced() // the agent of the model's own call that asked first: none else can be
			}
			return nil
		})
		s.Event("worktree_made", map[string]any{"name": name, "worktree": dir, "branch": branch, "base": base})
	}
	return Result{Stdout: dir}, nil
}

// plainBase is the commit a plain worktree starts from: the remote's default branch as git last fetched it
// (Claude Code's worktree.baseRef "fresh", its default), or HEAD where there is none or the settings say
// "head".
func plainBase(root, cwd string) string {
	head := gitx.RevParse(cwd, "HEAD")
	if baseRef(root) == "head" {
		return head
	}
	if c := gitx.RevParse(root, "refs/remotes/origin/HEAD"); c != "" {
		return c
	}
	return head
}

// claudeSettings are the Claude Code settings files worktree.baseRef is read from, the later winning
// (a variable, so a test can point it elsewhere).
var claudeSettings = func(root string) []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".claude", "settings.local.json")}
}

// baseRef is worktree.baseRef in the Claude Code settings of the project at root, or "".
func baseRef(root string) string {
	ref := ""
	for _, f := range claudeSettings(root) {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var v struct {
			Worktree struct {
				BaseRef string `json:"baseRef"`
			} `json:"worktree"`
		}
		if json.Unmarshal(data, &v) == nil && v.Worktree.BaseRef != "" {
			ref = v.Worktree.BaseRef
		}
	}
	return ref
}

// plainStopped lets go of the plain worktree baton made for the agent of c that stopped, if it works in
// one, and removes it with its branch when the agent changed nothing there (no change, no untracked file,
// no commit), as Claude Code removes its own. One with work in it stays, as Claude Code's do.
func (h *handlers) plainStopped(c Context, s *state.Store) {
	if s == nil {
		return
	}
	var t state.PlainTree
	var found bool
	s.Update(func(st *state.State) error { t, found = st.Run.PlainAt(str(c.Input, "cwd")); return nil })
	if !found || !isDir(t.Path) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
	defer cancel()
	removed, err := gitx.RemoveUntouched(ctx, t.Path, t.Branch, t.Base)
	fields := map[string]any{"worktree": t.Path, "branch": t.Branch}
	if !removed {
		if err != nil {
			fields["error"] = err.Error()
		}
		s.Event("worktree_kept", fields)
		return
	}
	s.Event("worktree_removed", fields)
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return path != "" && err == nil && fi.IsDir()
}
