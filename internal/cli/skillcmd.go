package cli

import (
	_ "embed"
	"fmt"
)

// skillText is what the /baton skill does, for every subcommand. The plugin's SKILL.md is a stub that
// loads it when /baton is invoked (an inline command that runs `baton skill`), so a session follows the
// instructions of the baton it runs, from the moment it is updated, instead of the text its plugin had
// when the session started.
//
// The stub must never pass $ARGUMENTS into that command: Claude Code substitutes them into the command
// line before the shell runs it, so the human's text would run as shell (spikes/18-skill-inject).
//
//go:embed skill.md
var skillText string

func init() {
	register("skill", "(internal) print the /baton skill's instructions (the plugin's skill loads them from here)", func(args []string, io IO) int {
		if len(args) != 0 {
			return fail(io, "usage: baton skill")
		}
		fmt.Fprint(io.Out, skillText)
		return 0
	})
}
