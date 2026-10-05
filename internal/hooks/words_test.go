package hooks

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// denyReason is why PreToolUse denied a call, or "" if it did not.
func denyReason(out map[string]any) string {
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" {
		return ""
	}
	s, _ := hso["permissionDecisionReason"].(string)
	return s
}

// What the model reads fits the project: in a git repository it is told to commit its work; in a plain
// folder, or where git is not installed, it is never asked to commit (or to create a repository).
func TestTheWordingFitsTheProject(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root string)
		git   bool
	}{
		{"git repository", func(t *testing.T, root string) { gittest.Init(t, root) }, true},
		{"plain folder", func(*testing.T, string) {}, false},
		{"git not installed", func(t *testing.T, root string) { gittest.Init(t, root); gittest.NoGit(t) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			gittest.Isolate(t)
			f := newFixture(t, true)
			c.setup(t, filepath.Dir(f.dir))
			said := map[string]string{
				"primer":           additionalContext(f.fire("SessionStart", map[string]any{"source": "startup"})),
				"question refused": denyReason(f.fire("PreToolUse", ask("Which colour?", "Red", "Blue"))),
			}
			said["stop refused"], _ = f.fire("Stop", map[string]any{})["reason"].(string)
			f.vars = defaultCap
			f.setContext(500_000)
			said["checkpoint nudge"] = additionalContext(f.fire("PostToolUse", bash("ls")))
			said["checkpoint now"] = additionalContext(f.fire("PostToolUse", answer(ask(f.warned(), warnLabels...), "Checkpoint now")))

			// The brief at the next phase boundary.
			pl, _ := f.store.LoadPlan()
			f.store.Update(func(st *state.State) error {
				_, err := state.Done(st, pl, "P0", f.now, false)
				st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactActive, Reason: "boundary", ByBaton: true}
				return err
			})
			said["brief"] = additionalContext(f.fire("SessionStart", map[string]any{"source": "compact"}))

			for name, text := range said {
				commit := strings.Contains(strings.ToLower(text), "commit")
				switch {
				case text == "":
					t.Errorf("%s: nothing said", name)
				case commit && !c.git:
					t.Errorf("%s asks for a commit without git:\n%s", name, text)
				case !commit && c.git && name != "question refused": // that one is the same everywhere
					t.Errorf("%s does not ask for a commit in a git repository:\n%s", name, text)
				}
			}
			for _, name := range []string{"primer", "stop refused", "question refused"} {
				if !strings.Contains(said[name], "baton propose") || !strings.Contains(said[name], "--tried") {
					t.Errorf("%s does not name the ways to reach the human:\n%s", name, said[name])
				}
			}
		})
	}
}
