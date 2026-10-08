package hooks

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/skill"
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

// skillRefresh hands the model the current /baton instructions on the first prompt after the session
// restarted on a newer baton (from), if the conversation still holds an earlier copy: an update cannot
// change what is already in a conversation, and the model would go on following the old one. A prompt
// that is itself a /baton loads the current instructions anyway.
func skillRefresh(c Context, from string) string {
	if from == "" || strings.HasPrefix(strings.TrimSpace(str(c.Input, "prompt")), "/baton") {
		return ""
	}
	if held, err := skill.InConversation(str(c.Input, "transcript_path")); err != nil || !held {
		return ""
	}
	return fmt.Sprintf("%s baton was updated from %s to %s since /baton's instructions were loaded in this conversation. "+
		"That earlier copy is out of date: for anything /baton, follow these current instructions instead.\n\n%s",
		NudgePrefix, from, c.Env("BATON_VERSION"), skill.Text)
}
