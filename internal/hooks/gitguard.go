package hooks

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Two baton sessions can work in one checkout at once, each running its own plan. They share one working
// tree and one git index, so a git command that takes in or throws away everything it finds takes the
// other session's work with it: `git add -A` sweeps its half-done files into this session's commit, and
// `git stash` or `git reset --hard` makes them disappear from under it. While another session is live
// in the checkout, baton refuses those commands, and the model stages its own files by path.

// shellCommands splits a command line into its simple commands, each a list of words with the quoting
// removed. Like batonShell it reads only as much shell as it needs: quotes, backslashes, and the
// operators that separate commands. Nothing is expanded.
func shellCommands(line string) [][]string {
	var cmds [][]string
	var words []string
	var word strings.Builder
	inWord, single, double := false, false, false
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case single:
			if c == '\'' {
				single = false
			} else {
				word.WriteByte(c)
			}
		case double:
			switch {
			case c == '"':
				double = false
			case c == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0:
				i++
				word.WriteByte(line[i])
			default:
				word.WriteByte(c)
			}
		case c == '\\' && i+1 < len(line):
			i++
			if line[i] != '\n' {
				word.WriteByte(line[i])
				inWord = true
			}
		case c == '\'':
			single, inWord = true, true
		case c == '"':
			double, inWord = true, true
		case c == ' ' || c == '\t':
			endWord()
		case strings.IndexByte(";&|\n()", c) >= 0:
			endCmd()
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	endCmd()
	return cmds
}

// sweepingGit returns the first git command in line that takes in or throws away more than the files it
// names, written out for a message, or "" if there is none:
//
//	git add -A | --all | -u | --update | . | :/ | *      stages everything
//	git commit -a | --all (or -am …)                     commits every change to tracked files
//	git stash [push | save | -u …]                       takes every change out of the working tree
//	git reset --hard                                     throws every change away
//	git checkout|restore . | :/  ·  git checkout -f      throws every change away
//	git clean -f …                                       deletes every untracked file
func sweepingGit(line string) string {
	for _, words := range shellCommands(line) {
		sub, args := gitSubcommand(words)
		if sub == "" {
			continue
		}
		if sweeps(sub, args) {
			shown := "git " + strings.Join(append([]string{sub}, args...), " ")
			if len(shown) > 60 {
				shown = shown[:57] + "…"
			}
			return shown
		}
	}
	return ""
}

// gitSubcommand finds a git invocation in a simple command: its subcommand and that subcommand's
// arguments, past any environment assignments, wrappers and git's own options.
func gitSubcommand(words []string) (string, []string) {
	i := 0
	for ; i < len(words); i++ {
		w := words[i]
		if w == "git" || strings.HasSuffix(w, "/git") {
			break
		}
		switch {
		case w == "env" || w == "command" || w == "exec" || w == "nohup" || w == "time" || w == "sudo":
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-"):
		default:
			return "", nil
		}
	}
	for i++; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") {
			return w, words[i+1:]
		}
		if w == "-C" || w == "-c" || w == "--git-dir" || w == "--work-tree" || w == "--namespace" {
			i++
		}
	}
	return "", nil
}

func sweeps(sub string, args []string) bool {
	// flag reports a long option, or a cluster of short ones containing one of short (-am, -fd).
	flag := func(a, long, short string) bool {
		if a == long {
			return true
		}
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			return false
		}
		for _, r := range a[1:] {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return false
			}
		}
		return strings.ContainsAny(a[1:], short)
	}
	whole := func(a string) bool { return a == "." || a == ":/" || a == "*" || a == "./" || a == ":/*" }
	takesValue := map[string]bool{"-m": true, "--message": true, "-F": true, "--file": true, "-C": true, "-c": true, "--author": true, "--date": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if takesValue[a] {
			i++
			continue
		}
		switch sub {
		case "add":
			if flag(a, "--all", "A") || flag(a, "--update", "u") || whole(a) {
				return true
			}
		case "commit":
			if flag(a, "--all", "a") {
				return true
			}
		case "reset":
			if a == "--hard" {
				return true
			}
		case "checkout":
			if whole(a) || flag(a, "--force", "f") {
				return true
			}
		case "restore":
			if whole(a) {
				return true
			}
		case "clean":
			if flag(a, "--force", "f") {
				return true
			}
		}
	}
	if sub == "stash" {
		if len(args) == 0 {
			return true
		}
		switch args[0] {
		case "list", "show", "apply", "pop", "drop", "branch", "clear", "create", "store":
			return false
		}
		return true
	}
	return false
}

// sharedCheckout returns the other runs a host drives in s's working tree right now.
func sharedCheckout(s *state.Store) []state.RunInfo {
	p, err := state.OpenProject(filepath.Join(s.Root, state.DirName), s.Now)
	if err != nil {
		return nil
	}
	return p.Others(filepath.Base(s.Dir))
}

// gitRefusal is why a sweeping git command is refused while others work in the same checkout.
func gitRefusal(cmd string, others []state.RunInfo) string {
	who := "another baton session"
	if len(others) > 1 {
		who = fmt.Sprintf("%d other baton sessions", len(others))
	}
	var names []string
	for _, r := range others {
		if r.HasPlan() {
			names = append(names, fmt.Sprintf("%q", r.Title))
		}
	}
	running := ""
	if len(names) > 0 {
		running = " (running " + strings.Join(names, ", ") + ")"
	}
	return fmt.Sprintf("%s Not run: %s is working in this checkout right now%s, and `%s` would take in or throw away its work too. "+
		"Stage and commit only your own files, by path (git add <file> …, then git commit), and leave the rest of the working tree alone. "+
		"While another session works here, baton refuses git commands that take every change (add -A or ., commit -a, stash, reset --hard, checkout or restore ., clean -f).",
		NudgePrefix, who, running, cmd)
}

// editedPath returns the file a main-agent or subagent edit tool wrote, relative to the working tree root
// and slash-separated the way git reports it, or "" if the call wrote no file inside it.
func editedPath(in map[string]any, root string) string {
	switch str(in, "tool_name") {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
	default:
		return ""
	}
	ti, _ := in["tool_input"].(map[string]any)
	p := str(ti, "file_path")
	if p == "" {
		p = str(ti, "notebook_path")
	}
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if cwd := str(in, "cwd"); cwd != "" {
			p = filepath.Join(cwd, p)
		}
	}
	real := func(path string) string {
		if r, err := filepath.EvalSymlinks(path); err == nil {
			return r
		}
		if r, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			return filepath.Join(r, filepath.Base(path))
		}
		return path
	}
	rel, err := filepath.Rel(real(root), real(p))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}
