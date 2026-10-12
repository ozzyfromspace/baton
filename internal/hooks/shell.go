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

// shellWords splits cmd, a plain command (shellSimple), into its words as sh would: quotes removed, a
// backslash keeping the character after it (inside double quotes, only one sh gives a meaning to). ok is
// false for quotes left open.
func shellWords(cmd string) (words []string, ok bool) {
	var w strings.Builder
	in, single, double := false, false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case single:
			if c == '\'' {
				single = false
			} else {
				w.WriteByte(c)
			}
		case double && c == '\\' && i+1 < len(cmd) && strings.IndexByte("$`\"\\\n", cmd[i+1]) >= 0:
			i++
			w.WriteByte(cmd[i])
		case double:
			if c == '"' {
				double = false
			} else {
				w.WriteByte(c)
			}
		case c == '\\' && i+1 < len(cmd):
			i++
			w.WriteByte(cmd[i])
			in = true
		case c == '\'':
			single, in = true, true
		case c == '"':
			double, in = true, true
		case c == ' ' || c == '\t':
			if in {
				words, in = append(words, w.String()), false
				w.Reset()
			}
		default:
			w.WriteByte(c)
			in = true
		}
	}
	if single || double {
		return nil, false
	}
	if in {
		words = append(words, w.String())
	}
	return words, true
}

// asWords rewrites cmd, a phase agent's `baton verify` or `baton await` that gives its gate as one quoted
// text, with the gate as words after -- (the CLI quotes them again for the lane's shell, lane.Join): in an
// agent isolated in a worktree, Claude Code's guard refuses a command that hands a program text starting
// with a shell or git, and accepts the same gate as words (P7), so nothing depends on how the agent writes
// it. A gate of plain words becomes those words; one with shell syntax becomes sh -c with the gate, which
// runs it as before. It reports false for a command that needs no rewriting, or that it cannot read.
func asWords(cmd string) (string, bool) {
	if baton, verdict := batonShell(cmd); !baton || verdict != shellSimple {
		return "", false
	}
	words, ok := shellWords(cmd)
	if !ok || len(words) < 3 || words[0] != "baton" || words[1] != "verify" && words[1] != "await" {
		return "", false
	}
	var flags, gate []string
	for i := 2; i < len(words); i++ {
		switch a := words[i]; {
		case a == "--":
			return "", false // the gate is given as words already
		case a == "--notes" && i+1 < len(words):
			flags = append(flags, a, words[i+1])
			i++
		case strings.HasPrefix(a, "--notes="), a == "--here" && words[1] == "verify":
			flags = append(flags, a)
		case strings.HasPrefix(a, "--"):
			return "", false // a flag the CLI would refuse: it says so itself
		default:
			gate = append(gate, a)
		}
	}
	text := strings.TrimSpace(strings.Join(gate, " "))
	split := strings.Fields(text)
	if len(split) < 2 {
		return "", false // one word is never refused
	}
	if strings.ContainsAny(text, "|&;<>()$`\\\"'*?[]{}~#!\n") {
		split = []string{"sh", "-c", text}
	}
	out := []string{"baton", words[1]}
	for _, f := range flags {
		out = append(out, shQuote(f))
	}
	out = append(out, "--")
	for _, g := range split {
		out = append(out, shQuote(g))
	}
	return strings.Join(out, " "), true
}

// shQuote quotes s for sh where it would read it otherwise.
func shQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
