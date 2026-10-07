package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestShellCommands(t *testing.T) {
	got := shellCommands(`cd src && GIT_PAGER=cat git commit -m "fix: a; b" | tee log; echo 'x y' \
z`)
	want := [][]string{{"cd", "src"}, {"GIT_PAGER=cat", "git", "commit", "-m", "fix: a; b"}, {"tee", "log"}, {"echo", "x y", "z"}}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if !equal(got[i], want[i]) {
			t.Errorf("command %d: %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSweepingGit(t *testing.T) {
	for _, c := range []struct {
		cmd   string
		sweep bool
	}{
		{"git add -A", true},
		{"git add --all", true},
		{"git add .", true},
		{"git add -u", true},
		{"git add -vA", true},
		{"git add :/", true},
		{`cd x && git add -A && git commit -m "wip"`, true},
		{"git -C sub add .", true},
		{"git commit -am 'quick fix'", true},
		{"git commit --all -m x", true},
		{"git stash", true},
		{"git stash push -m 'mine'", true},
		{"git stash -u", true},
		{"git reset --hard HEAD~1", true},
		{"git checkout .", true},
		{"git checkout -- .", true},
		{"git checkout -f main", true},
		{"git restore .", true},
		{"git clean -fd", true},
		{"/usr/bin/git add -A", true},
		{"env GIT_DIR=.git git add .", true},

		{"git add src/a.go src/b.go", false},
		{"git add -p src/a.go", false},
		{`git commit -m "add -A support"`, false},
		{"git commit -m -a", false},
		{"git commit src/a.go -m x", false},
		{"git stash list", false},
		{"git stash pop", false},
		{"git reset --soft HEAD~1", false},
		{"git reset src/a.go", false},
		{"git checkout -b feature", false},
		{"git checkout -- src/a.go", false},
		{"git restore --staged src/a.go", false},
		{"git clean -n", false},
		{"git status", false},
		{"git log --all", false},
		{"echo git add -A", false},
		{"grep -r 'git add -A' docs", false},
	} {
		if got := sweepingGit(c.cmd); (got != "") != c.sweep {
			t.Errorf("%q: %q, want sweep %v", c.cmd, got, c.sweep)
		}
	}
}

func TestEditedPath(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	edit := func(tool, key, path string) map[string]any {
		return map[string]any{"tool_name": tool, "cwd": root, "tool_input": map[string]any{key: path}}
	}
	for _, c := range []struct {
		in   map[string]any
		want string
	}{
		{edit("Edit", "file_path", filepath.Join(root, "src", "a.go")), "src/a.go"},
		{edit("Write", "file_path", filepath.Join(root, "new.md")), "new.md"},
		{edit("MultiEdit", "file_path", "src/b.go"), "src/b.go"},
		{edit("NotebookEdit", "notebook_path", filepath.Join(root, "n.ipynb")), "n.ipynb"},
		{edit("Write", "file_path", filepath.Join(t.TempDir(), "elsewhere.go")), ""},
		{edit("Read", "file_path", filepath.Join(root, "src", "a.go")), ""},
	} {
		if got := editedPath(c.in, root); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
}

// While another session works in the same checkout, git commands that take every change are refused,
// from the main agent and subagents alike; alone in the checkout, nothing is.
func TestSweepingGitIsRefusedOnlyInASharedCheckout(t *testing.T) {
	f := newFixture(t, true)
	decision := func(in map[string]any) (string, map[string]any) {
		out := f.fire("PreToolUse", in)
		hso, _ := out["hookSpecificOutput"].(map[string]any)
		d, _ := hso["permissionDecision"].(string)
		return d, out
	}
	if d, _ := decision(bash("git add -A")); d == "deny" {
		t.Fatal("refused with no other session in the checkout")
	}
	p, _ := state.OpenProject(f.dir, f.clock)
	other, _ := p.Bind(state.Binding{Session: "sess-2", Instance: "inst-2"})
	pl, _ := f.store.LoadPlan()
	pl.Title = "The other plan"
	other.SavePlan(pl)
	other.Update(func(st *state.State) error { *st = state.Reattach(*st, pl, f.now, state.Origin{}); return nil })

	for _, in := range []map[string]any{bash("git add -A && git commit -m wip"), byAgent(bash("git stash"), "sub")} {
		d, out := decision(in)
		hso, _ := out["hookSpecificOutput"].(map[string]any)
		reason, _ := hso["permissionDecisionReason"].(string)
		if d != "deny" || !strings.Contains(reason, `running "The other plan"`) || !strings.Contains(reason, "by path") {
			t.Errorf("%v: %s %q", in, d, reason)
		}
		if msg, _ := out["systemMessage"].(string); !strings.Contains(msg, "another baton session is working in this checkout") {
			t.Errorf("not announced: %v", out)
		}
	}
	if d, _ := decision(bash("git add src/a.go && git commit -m mine")); d == "deny" {
		t.Error("refused staging by path")
	}
	if n := strings.Count(f.eventLog(), `"kind":"git_refused"`); n != 2 {
		t.Errorf("%d git_refused events", n)
	}
}

// The files a session's edit tools write are recorded against the phase, subagents' included.
func TestEditsAreRecordedAgainstThePhase(t *testing.T) {
	f := newFixture(t, true)
	root := filepath.Dir(f.dir)
	write := func(rel string) map[string]any {
		return map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": filepath.Join(root, rel), "content": "x"}}
	}
	f.fire("PostToolUse", write("a.go"))
	f.fire("PostToolUse", byAgent(write("b.go"), "sub"))
	f.fire("PostToolUse", write("a.go"))
	f.fire("PostToolUseFailure", write("failed.go"))
	if got := f.state().Phases["P0"].Edited; !equal(got, []string{"a.go", "b.go"}) {
		t.Fatalf("edited %v", got)
	}
}
