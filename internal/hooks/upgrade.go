package hooks

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/upgrade"
)

// newerBaton tells the human, once per version, that a newer baton is installed and what becomes of this
// session: the host restarts it on a compatible release by itself (package upgrade); across a major
// version, or with auto_restart off, it stays on its version until the human restarts it.
func newerBaton(st *state.State, c Context) string {
	running := c.Env("BATON_VERSION")
	if !upgrade.Release(running) {
		return "" // a build from a checkout: there is nothing to say
	}
	root := config.Root(c.Env)
	to, ok := upgrade.Installed(root)
	if !ok || upgrade.Compare(to, running) <= 0 || st.Run.NewerNoted == to {
		return ""
	}
	st.Run.NewerNoted = to
	stays := fmt.Sprintf("baton %s is installed; this session stays on %s until you restart it (exit, then run `baton --continue`).", to, running)
	switch {
	case !upgrade.Compatible(running, to):
		return "baton: " + stays + " It is a major update: read what changed first, " + upgrade.ChangelogURL
	case !upgrade.CanRestart || !config.Load(root, c.Env).Restarts():
		return "baton: " + stays
	case st.Mode == state.ModeRunning:
		return fmt.Sprintf("baton: baton %s is installed. baton restarts this session on it at the next phase boundary; the conversation and the plan carry on.", to)
	default:
		return fmt.Sprintf("baton: baton %s is installed. baton restarts this session on it once the session is idle; the conversation carries on.", to)
	}
}
