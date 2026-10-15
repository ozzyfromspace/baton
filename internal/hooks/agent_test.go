package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// agentRun is a run (sess-1 on inst-1, the lead on P1) in a git checkout whose roster has a worker on P2,
// starting, as the host leaves it before baton's mod starts its phase agent: its sub-run made, its
// worktree not yet (the worktree hook makes it), and its spawn open in the bridge.
type agentRun struct {
	*fixture
	sub      *state.Store
	entry    state.Worker
	checkout string
}

func newAgentRun(t *testing.T) *agentRun {
	t.Helper()
	gittest.Isolate(t)
	claudeSettings = func(string) []string { return nil }
	t.Cleanup(func() { claudeSettings = defaultSettings })
	lead := newFixture(t, false)
	checkout := filepath.Dir(lead.dir)
	gittest.Git(t, checkout, "init", "-q", "-b", "main")
	gittest.Write(t, checkout, "a.go", "package a\n")
	gittest.Commit(t, checkout, "first", "a.go")
	file := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(file, []byte(workerDoc), 0o644)
	pl, err := plan.Build(file, []byte(workerDoc), plan.Suggest([]byte(workerDoc)))
	if err != nil {
		t.Fatal(err)
	}
	lead.store.SavePlan(pl)
	tree := filepath.Join(gitxReal(t, t.TempDir()), "worktrees", "P2")
	e := state.Worker{Phase: "P2", Ref: state.WorkerRef(lead.store.Ref, "P2"), Worktree: tree,
		Branch: "baton/sess-1/P2", Base: git(t, checkout, "rev-parse", "HEAD"), Assigned: lead.now}
	lead.store.Update(func(st *state.State) error {
		*st = state.Reattach(*st, pl, lead.now, state.Origin{})
		st.Phases["P0"].Status = state.PhaseDone
		st.Current = "P1"
		state.Start(st, lead.now, state.Origin{})
		return state.Assign(st, pl, e, lead.now)
	})
	p, _ := state.OpenProject(lead.dir, lead.clock)
	sub, err := p.NewWorker(lead.store.Ref, "P2", lead.inst, tree)
	if err != nil {
		t.Fatal(err)
	}
	only, _ := pl.Only("P2")
	sub.SavePlan(only)
	sub.Update(func(st *state.State) error { *st = state.AttachWorker(*st, "P2"); return nil })
	r := &agentRun{fixture: lead, sub: sub, entry: e, checkout: checkout}
	r.issueSpawn()
	return r
}

var defaultSettings = claudeSettings

func git(t *testing.T, dir string, args ...string) string { return gittest.Git(t, dir, args...) }

func (r *agentRun) bridge() *bridge.Bridge { return bridge.Open(r.store.Dir, r.clock, nil) }

// issueSpawn opens the spawn of P2's agent in the bridge, as the host does.
func (r *agentRun) issueSpawn() {
	if _, err := r.bridge().Issue(bridge.Command{Kind: bridge.Spawn, Instance: r.inst, Phase: "P2", Name: "P2", Prompt: "Begin P2."}); err != nil {
		r.t.Fatal(err)
	}
}

// spawnCall is baton's mod's call to start P2's agent, as the lead's PreToolUse sees it.
func spawnCall() map[string]any {
	return map[string]any{"tool_name": "Agent", "tool_use_id": "toolu_plugin_1", "prompt_id": "p-mod",
		"tool_input": map[string]any{"subagent_type": state.PhaseAgent, "name": "P2", "description": "P2 — Third", "prompt": "Begin P2."}}
}

// modelCall is a call of the model's own to the Agent tool, id, for an isolated subagent.
func modelCall(id string) map[string]any {
	return map[string]any{"tool_name": "Agent", "tool_use_id": id, "prompt_id": "p-turn",
		"tool_input": map[string]any{"subagent_type": "general-purpose", "description": "Look around", "prompt": "Look around.", "isolation": "worktree"}}
}

// worktree fires WorktreeCreate for the worktree name and returns its exit code, its stdout and its
// stderr. Its prompt id is the session's latest prompt's, whoever's: here the lead's.
func (r *agentRun) worktree(name string) (int, string, string) {
	r.t.Helper()
	in := map[string]any{"hook_event_name": "WorktreeCreate", "session_id": r.sid, "transcript_path": r.leadTranscript(),
		"cwd": r.checkout, "name": name, "prompt_id": "p-turn"}
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	code := Dispatch("WorktreeCreate", bytes.NewReader(raw), &out, &errb, r.env(r.inst), r.clock, Handlers(Deps{Open: OpenRun(r.clock)}))
	return code, strings.TrimSpace(out.String()), errb.String()
}

// asAgent is a hook's input as agent, a baton:phase agent, fires it in dir.
func asAgent(in map[string]any, agent, dir string) map[string]any {
	in["agent_id"], in["agent_type"], in["cwd"] = agent, state.PhaseAgent, dir
	return in
}

// The worktree baton's mod asks for as it starts a phase agent is that phase's: made from the run's
// branch where the roster says, on the phase's branch, and the agent is linked to the phase's sub-run
// before its first hook. The agent is briefed at its first start, once, and its stop is the Stop table's.
func TestAPhaseAgentWorksInItsPhasesWorktree(t *testing.T) {
	r := newAgentRun(t)
	if out := r.fire("PreToolUse", spawnCall()); out != nil {
		t.Fatalf("the mod's call: %v", out)
	}
	code, path, errs := r.worktree("agent-a1")
	if code != 0 || path != r.entry.Worktree {
		t.Fatalf("exit %d, path %q (want %q): %s", code, path, r.entry.Worktree, errs)
	}
	if b := git(t, path, "rev-parse", "--abbrev-ref", "HEAD"); b != r.entry.Branch {
		t.Errorf("the worktree is on %q", b)
	}
	p, _ := state.OpenProject(r.dir, r.clock)
	if ref := p.AgentRun("a1"); ref != r.entry.Ref {
		t.Errorf("a1 is linked to %q", ref)
	}
	st := r.state()
	if w := st.WorkerOn("P2"); w == nil || w.AgentID != "a1" || st.Run.Spawning != nil {
		t.Errorf("roster %+v, spawning %+v", w, st.Run.Spawning)
	}
	if !r.logged("agent_placed", map[string]any{"phase": "P2", "agent": "a1", "kept": false}) {
		t.Errorf("events:\n%s", r.eventLog())
	}

	out := r.fire("SubagentStart", asAgent(map[string]any{}, "a1", path))
	ctx, _ := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	sub := loaded(t, r.sub)
	if !strings.Contains(ctx, "P2 — Third") || sub.Phases["P2"].Status != state.PhaseActive || sub.Run.Briefed != "a1" || !sub.Run.TurnOpen {
		t.Fatalf("the brief %q; sub-run %+v", ctx, sub.Run)
	}
	if out := r.fire("SubagentStart", asAgent(map[string]any{}, "a1", path)); out != nil {
		t.Errorf("a message that starts it again gives a brief again: %v", out)
	}
	out = r.fire("SubagentStop", asAgent(map[string]any{"stop_hook_active": false}, "a1", path))
	if sub := loaded(t, r.sub); out["decision"] != "block" || sub.Run.StopBlocks != 1 || sub.Run.TurnOpen {
		t.Errorf("a stop with no status: %v; sub-run %+v", out, sub.Run)
	}
	if st := r.state(); st.Run.Subagents != 0 || st.Run.TurnOpen {
		t.Errorf("the lead's run counts the phase agent: %+v", st.Run)
	}
}

// A worktree baton cannot tell for a phase's is made where Claude Code makes one: the model's own isolated
// subagent's, with no spawn of baton's under way or while one is (each call's PreToolUse comes before its
// worktree; baton's spawn then gets a plain one too, and its agent is ended), or a workflow's agent's.
// Nothing of the phase's changes.
func TestAWorktreeNotAPhasesIsPlain(t *testing.T) {
	for _, c := range []struct {
		name, tree      string
		spawning, model bool
		batons, settled bool // baton's own worktree after it is the phase's; the model's call is settled
	}{
		{"the model's own subagent, no spawn under way", "agent-m1", false, true, false, true},
		{"the model's own subagent while baton's spawn is open", "agent-m1", true, true, false, false},
		{"a workflow's agent", "wf_run-1", true, false, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAgentRun(t)
			if c.spawning {
				r.fire("PreToolUse", spawnCall())
			}
			if c.model {
				r.fire("PreToolUse", modelCall("toolu_m1"))
			}
			code, path, errs := r.worktree(c.tree)
			want := filepath.Join(gitxReal(t, r.checkout), ".claude", "worktrees", c.tree)
			if code != 0 || path != want {
				t.Fatalf("exit %d, path %q (want %q): %s", code, path, want, errs)
			}
			if b := git(t, path, "rev-parse", "--abbrev-ref", "HEAD"); b != "worktree-"+c.tree {
				t.Errorf("on %q", b)
			}
			// It is not the checkout's work, as none of Claude Code's own worktrees are.
			if dirty := git(t, r.checkout, "status", "--porcelain", "--untracked-files=all", "--", ".claude"); dirty != "" {
				t.Errorf("git status in the checkout: %s", dirty)
			}
			st := r.state()
			if w := st.WorkerOn("P2"); w.AgentID != "" || len(st.Run.Plain) != 1 || st.Run.Plain[0].Name != c.tree || (st.Run.Spawning != nil) != c.spawning {
				t.Errorf("roster %+v, plain %+v, spawning %+v", w, st.Run.Plain, st.Run.Spawning)
			}
			if st.Run.AgentCallsOpen(r.now) == c.settled {
				t.Errorf("the model's calls %+v", st.Run.AgentCalls)
			}
			if isDir(r.entry.Worktree) {
				t.Error("the phase's worktree was made")
			}
			if c.spawning {
				code, path, _ := r.worktree("agent-a1")
				if phase := path == r.entry.Worktree; code != 0 || phase != c.batons {
					t.Errorf("baton's own: exit %d, %q", code, path)
				}
			}
		})
	}
}

// A call of the model's own to the Agent tool is settled by its result, by time, or by the worktree baton
// makes for its agent; until then baton's spawn cannot tell its worktree from the model's. One waiting on
// the human's permission is not settled by time.
func TestTheModelsAgentCallsHoldThePhasesWorktree(t *testing.T) {
	for _, c := range []struct {
		name   string
		before func(r *agentRun)
		phase  bool // baton's spawn then gets the phase's worktree
	}{
		{"none", func(*agentRun) {}, true},
		{"one just made", func(r *agentRun) { r.fire("PreToolUse", modelCall("toolu_m1")) }, false},
		{"one whose result came", func(r *agentRun) {
			r.fire("PreToolUse", modelCall("toolu_m1"))
			r.fire("PostToolUse", modelCall("toolu_m1"))
		}, true},
		{"one refused at its prompt", func(r *agentRun) {
			r.fire("PreToolUse", modelCall("toolu_m1"))
			r.fire("PermissionDenied", modelCall("toolu_m1"))
		}, true},
		{"one made long enough ago", func(r *agentRun) {
			r.fire("PreToolUse", modelCall("toolu_m1"))
			r.now = r.now.Add(state.AgentCallWait)
		}, true},
		{"one waiting on the human's permission", func(r *agentRun) {
			r.fire("PreToolUse", modelCall("toolu_m1"))
			r.fire("PermissionRequest", modelCall("toolu_m1"))
			r.now = r.now.Add(time.Hour)
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAgentRun(t)
			c.before(r)
			r.fire("PreToolUse", spawnCall())
			code, path, errs := r.worktree("agent-a2")
			if phase := path == r.entry.Worktree; code != 0 || phase != c.phase {
				t.Errorf("exit %d, path %q, the phase's %v (want %v): %s", code, path, phase, c.phase, errs)
			}
		})
	}
}

// A phase agent's own calls to the Agent tool count in the lead's run, where worktrees are told apart, from
// their PreToolUse to their result, under the agent's id, as its permission prompt is.
func TestAPhaseAgentsAgentCallsCountInTheLeadsRun(t *testing.T) {
	r := newAgentRun(t)
	r.fire("PreToolUse", spawnCall())
	r.worktree("agent-a1")
	r.fire("PreToolUse", asAgent(modelCall("toolu_m1"), "a1", r.entry.Worktree))
	if sub, st := loaded(t, r.sub), r.state(); len(sub.Run.AgentCalls) != 0 || len(st.Run.AgentCalls) != 1 || st.Run.AgentCalls[0].ToolUse != "toolu_m1" || st.Run.AgentCalls[0].Agent != "a1" {
		t.Errorf("the lead's run %+v, the sub-run's %+v", st.Run.AgentCalls, sub.Run.AgentCalls)
	}
	r.fire("PostToolUse", asAgent(modelCall("toolu_m1"), "a1", r.entry.Worktree))
	if st := r.state(); len(st.Run.AgentCalls) != 0 {
		t.Errorf("its result left %+v", st.Run.AgentCalls)
	}
}

func gitxReal(t *testing.T, dir string) string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// A phase agent that baton cannot place does not start at all, rather than anywhere else: with no spawn of
// its open in the bridge, or no worker on the roster, the worktree is refused, and nothing is linked.
func TestAPhaseAgentBatonCannotPlaceDoesNotStart(t *testing.T) {
	for _, c := range []struct {
		name    string
		without func(r *agentRun)
	}{
		{"no spawn open in the bridge", func(r *agentRun) {
			rec, _ := r.bridge().Load()
			open, _ := rec.PendingSpawn(r.inst)
			r.bridge().SayHello(bridge.HelloInfo{Instance: r.inst, Protocol: bridge.Protocol})
			r.bridge().Record(bridge.Report{Seq: 1, Kind: bridge.Ack, Instance: r.inst, Command: open.ID, Error: "spawn failed"})
		}},
		{"no worker on the roster", func(r *agentRun) {
			r.set(func(st *state.State) { st.Workers = nil })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAgentRun(t)
			r.fire("PreToolUse", spawnCall())
			c.without(r)
			code, path, errs := r.worktree("agent-a1")
			if code != 1 || path != "" || !strings.Contains(errs, "no worktree for the agent on P2") {
				t.Errorf("exit %d, path %q, stderr %q", code, path, errs)
			}
			p, _ := state.OpenProject(r.dir, r.clock)
			if isDir(r.entry.Worktree) || p.AgentRun("a1") != "" || !r.logged("agent_unplaced", map[string]any{"phase": "P2", "agent": "a1"}) {
				t.Errorf("placed after all:\n%s", r.eventLog())
			}
		})
	}
}

// A phase taken up again by another agent gets its worktree back as the agent before left it, and the new
// agent is briefed to take the phase up where it stands.
func TestARespawnedAgentGetsItsPhasesWorktreeBack(t *testing.T) {
	r := newAgentRun(t)
	r.fire("PreToolUse", spawnCall())
	r.worktree("agent-a1")
	r.fire("SubagentStart", asAgent(map[string]any{}, "a1", r.entry.Worktree))
	gittest.Write(t, r.entry.Worktree, "b.go", "package a\n")
	gittest.Commit(t, r.entry.Worktree, "P2's start", "b.go")
	tip := git(t, r.entry.Worktree, "rev-parse", "HEAD")

	rec, _ := r.bridge().Load()
	open, _ := rec.PendingSpawn(r.inst)
	r.bridge().SayHello(bridge.HelloInfo{Instance: r.inst, Protocol: bridge.Protocol})
	r.bridge().Record(bridge.Report{Seq: 1, Kind: bridge.Ack, Instance: r.inst, Command: open.ID, OK: true, Agent: "a1"})
	r.bridge().Forget("a1")
	r.issueSpawn()
	r.now = r.now.Add(time.Minute)
	r.fire("PreToolUse", spawnCall())
	code, path, errs := r.worktree("agent-a2")
	if code != 0 || path != r.entry.Worktree || git(t, path, "rev-parse", "HEAD") != tip {
		t.Fatalf("exit %d, path %q: %s", code, path, errs)
	}
	if !r.logged("agent_placed", map[string]any{"phase": "P2", "agent": "a2", "kept": true}) {
		t.Errorf("events:\n%s", r.eventLog())
	}
	out := r.fire("SubagentStart", asAgent(map[string]any{}, "a2", path))
	ctx, _ := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "P2 — Third") || loaded(t, r.sub).Run.Briefed != "a2" {
		t.Errorf("the new agent's brief %q", ctx)
	}
}

// A plain worktree baton made goes, with its branch, when its agent stops having changed nothing in it,
// as Claude Code's own go; one with work in it stays.
func TestAPlainWorktreeGoesWhenItsAgentChangedNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		work func(t *testing.T, dir string)
		gone bool
	}{
		{"nothing changed", func(*testing.T, string) {}, true},
		{"a file changed", func(t *testing.T, dir string) { gittest.Write(t, dir, "a.go", "package b\n") }, false},
		{"a file added", func(t *testing.T, dir string) { gittest.Write(t, dir, "new.txt", "x") }, false},
		{"a commit", func(t *testing.T, dir string) {
			gittest.Write(t, dir, "c.go", "package a\n")
			gittest.Commit(t, dir, "more", "c.go")
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAgentRun(t)
			_, path, _ := r.worktree("agent-m1")
			c.work(t, path)
			r.fire("SubagentStop", map[string]any{"agent_id": "m1", "agent_type": "general-purpose", "cwd": path})
			gone := !isDir(path) && gittest.Git(t, r.checkout, "branch", "--list", "worktree-agent-m1") == ""
			if gone != c.gone || len(r.state().Run.Plain) != 0 {
				t.Errorf("gone %v, want %v; plain %+v\n%s", gone, c.gone, r.state().Run.Plain, r.eventLog())
			}
		})
	}
}

// A plain worktree starts from the remote's default branch as git last fetched it, as Claude Code's do, or
// from HEAD where the settings say worktree.baseRef "head", or there is no remote.
func TestAPlainWorktreeStartsWhereClaudeCodesWould(t *testing.T) {
	r := newAgentRun(t)
	first := git(t, r.checkout, "rev-parse", "HEAD")
	git(t, r.checkout, "update-ref", "refs/remotes/origin/main", first)
	git(t, r.checkout, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gittest.Write(t, r.checkout, "a.go", "package a // later\n")
	gittest.Commit(t, r.checkout, "later", "a.go")
	head := git(t, r.checkout, "rev-parse", "HEAD")
	if _, path, _ := r.worktree("fresh"); git(t, path, "rev-parse", "HEAD") != first {
		t.Errorf("fresh: from %s, want the remote's %s", git(t, path, "rev-parse", "HEAD"), first)
	}
	settings := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(settings, []byte(`{"worktree":{"baseRef":"head"}}`), 0o644)
	claudeSettings = func(string) []string { return []string{settings} }
	if _, path, _ := r.worktree("head"); git(t, path, "rev-parse", "HEAD") != head {
		t.Errorf("head: from %s, want %s", git(t, path, "rev-parse", "HEAD"), head)
	}
}

// The calls baton's mod makes in the lead's session are not the model's: nothing baton owes the model is
// given with their results, where no model would read it.
func TestTheModsCallsCarryNothingForTheModel(t *testing.T) {
	r := newAgentRun(t)
	r.fire("PreToolUse", spawnCall())
	r.set(func(st *state.State) { st.Run.Briefing = &state.Briefing{Kind: "auto", Phase: "P1", At: r.now} })
	res := spawnCall()
	res["tool_response"] = map[string]any{"status": "async_launched", "agentId": "a1"}
	if out := r.fire("PostToolUse", res); out != nil {
		t.Errorf("the mod's result carried %v", out)
	}
	if st := r.state(); st.Run.Briefing == nil || st.Run.Spawning != nil {
		t.Errorf("briefing %+v, spawning %+v", st.Run.Briefing, st.Run.Spawning)
	}
}

// The worktree hook fails open in its own way: a panic in it makes a plain worktree, so that no agent of
// the model's goes without one, and exits 0.
func TestTheWorktreeHookFailsOpen(t *testing.T) {
	r := newAgentRun(t)
	r.fire("PreToolUse", spawnCall())
	open := func(ctx Context) (*state.Store, error) {
		s, err := OpenRun(r.clock)(ctx)
		if s != nil {
			s.Now = func() time.Time { panic("boom") }
		}
		return s, err
	}
	in := map[string]any{"hook_event_name": "WorktreeCreate", "session_id": r.sid, "cwd": r.checkout, "name": "agent-a1", "prompt_id": "p-mod"}
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	code := Dispatch("WorktreeCreate", bytes.NewReader(raw), &out, &errb, r.env(r.inst), r.clock, Handlers(Deps{Open: open}))
	want := filepath.Join(gitxReal(t, r.checkout), ".claude", "worktrees", "agent-a1")
	if code != 0 || strings.TrimSpace(out.String()) != want {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}
