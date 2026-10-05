package hooks

import "strings"

// Shell verdicts on a Bash command that runs baton's own CLI.
const (
	shellSimple       = "simple"       // one plain command: words, quoted or not
	shellSubstitution = "substitution" // the shell would replace part of it: $(…), `…`, $VAR
	shellComplex      = "complex"      // operators or redirections: more than one command, or a heredoc
)

// batonShell says whether cmd runs baton's CLI and how plain it is. It reads the command the way a POSIX
// shell would, just far enough to tell: single quotes protect everything, double quotes protect all
// but $ and backticks, a backslash protects the next character.
func batonShell(cmd string) (baton bool, verdict string) {
	cmd = strings.TrimSpace(cmd)
	if cmd != "baton" && !strings.HasPrefix(cmd, "baton ") && !strings.HasPrefix(cmd, "baton\t") {
		return false, ""
	}
	verdict = shellSimple
	worse := func(v string) {
		if v == shellComplex || verdict == shellSimple {
			verdict = v
		}
	}
	single, double := false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case single:
			single = c != '\''
		case c == '\\':
			i++ // escapes the next character (inside double quotes too, for the ones that matter)
		case double:
			switch c {
			case '"':
				double = false
			case '`', '$':
				worse(shellSubstitution)
			}
		case c == '\'':
			single = true
		case c == '"':
			double = true
		case c == '`', c == '$':
			worse(shellSubstitution)
		case strings.IndexByte(";&|<>()\n", c) >= 0:
			worse(shellComplex)
		}
	}
	if single || double {
		worse(shellComplex) // unbalanced quotes: let Claude Code's own checks handle it
	}
	return true, verdict
}
