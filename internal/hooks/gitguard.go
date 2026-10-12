package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
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

// sharedCheckout returns the other runs a host drives in s's working tree right now. A worker has its
// worktree to itself.
func sharedCheckout(s *state.Store) []state.RunInfo {
	if s.Worker() != "" {
		return nil
	}
	p, err := state.OpenProject(s.Project, s.Now)
	if err != nil {
		return nil
	}
	return p.Others(s.Ref)
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
	rel, err := filepath.Rel(realPath(root), realPath(p))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// realPath is path with its symlinks resolved, through its deepest directory that exists: a new file may
// be in new directories, and a file a rewind left behind may be gone.
func realPath(path string) string {
	for dir, rest := path, ""; ; dir, rest = filepath.Dir(dir), filepath.Join(filepath.Base(dir), rest) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(dir) == dir {
			return path
		}
	}
}

// A worker's work lands on the run's branch underneath the lead session (the merge queue). Its commits
// are not the lead's, yet they sit on the branch the lead commits to, and a lead told to amend "the commit
// it just made" amended a landed one instead (spike 25). While the run has landed work, baton refuses a
// lead's git command that would rewrite a landed commit: an amend, a reset to before it, or a rebase.

// rewrite is a git command that rewrites the branch checked out. The commits it rewrites or drops are
// HEAD alone (amend), every commit of HEAD (all), or those of base..HEAD.
type rewrite struct {
	shown string // the command, for messages
	amend bool
	all   bool
	base  string // as the command names it
}

// rewritingGit returns the first git command in line that rewrites the branch checked out, or nil: `git
// commit --amend`, `git reset <commit>`, and `git rebase` (not one under way, which --continue and the
// like carry on).
func rewritingGit(line string) *rewrite {
	for _, words := range shellCommands(line) {
		sub, args := gitSubcommand(words)
		shown := "git " + strings.Join(append([]string{sub}, args...), " ")
		if len(shown) > 60 {
			shown = shown[:57] + "…"
		}
		switch sub {
		case "commit":
			if slices.Contains(args, "--amend") {
				return &rewrite{shown: shown, amend: true}
			}
		case "reset":
			if pos, paths := positional(args, nil); !paths && len(pos) == 1 {
				return &rewrite{shown: shown, base: pos[0]}
			}
		case "rebase":
			for _, a := range args {
				switch a {
				case "--continue", "--abort", "--skip", "--quit", "--edit-todo", "--show-current-patch":
					return nil
				case "--root":
					return &rewrite{shown: shown, all: true}
				}
			}
			pos, _ := positional(args, map[string]bool{"--onto": true, "-x": true, "--exec": true, "-s": true, "--strategy": true, "-X": true, "--strategy-option": true})
			base := "@{upstream}"
			if len(pos) > 0 {
				base = pos[0]
			}
			return &rewrite{shown: shown, base: base}
		}
	}
	return nil
}

// positional splits a git command's arguments: those that are not options (nor the values of the options
// in takesValue), up to a "--", and whether paths follow one.
func positional(args []string, takesValue map[string]bool) (pos []string, paths bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return pos, true
		case takesValue[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			pos = append(pos, a)
		}
	}
	return pos, false
}

// rewrites lists the commits the command would rewrite or drop in the working tree at root, oldest first.
// A base git cannot find rewrites nothing: the command fails by itself.
func (rw *rewrite) rewrites(ctx context.Context, root string) []string {
	switch {
	case rw.amend:
		if head := gitx.Head(root); head != "" {
			return []string{head}
		}
		return nil
	case rw.all:
		out, _ := gitx.Commits(ctx, root, "", "HEAD")
		return out
	}
	base := gitx.RevParse(root, rw.base)
	if base == "" {
		return nil
	}
	out, _ := gitx.Commits(ctx, root, base, "HEAD")
	return out
}

// landedRewrite says why the lead may not run rw in the working tree at root, given the run's landings,
// or "" if it may: it would rewrite a commit one of them landed. It names the landed commits it would
// rewrite, and their phases.
func landedRewrite(rw *rewrite, root string, landed []state.Landing) (why string, commits []string) {
	ctx, cancel := context.WithTimeout(context.Background(), guardTimeout)
	defer cancel()
	of := map[string]string{}
	for _, l := range landed {
		for _, c := range l.Commits {
			of[c] = l.Phase
		}
	}
	var phases []string
	for _, c := range rw.rewrites(ctx, root) {
		if phase, ok := of[c]; ok {
			commits = append(commits, c)
			if !slices.Contains(phases, phase) {
				phases = append(phases, phase)
			}
		}
	}
	if len(commits) == 0 {
		return "", nil
	}
	return decide.LandedRewriteRefused(rw.shown, phases, commits), commits
}

// guardTimeout bounds the git a guarded command waits on.
const guardTimeout = 5 * time.Second
