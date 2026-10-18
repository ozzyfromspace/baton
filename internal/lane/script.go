package lane

import (
	"path/filepath"
	"slices"
	"strings"
)

// Scripts lists the files command, a shell command line, runs by their path: each simple command's program
// when a path names it (./gate.sh, /tmp/check.sh), and the file it hands an interpreter (sh x.sh, python3
// x.py, node x.js, go run x.go), read as IsCheck reads a line, with sh -c scripts looked into. Each path is
// as written: ~ and variables are left to the caller.
func Scripts(command string) []string {
	var out []string
	for _, words := range simpleCommands(command) {
		out = append(out, scriptWords(words, 0)...)
	}
	return out
}

// scriptWords lists the files one simple command, as words, runs by their path; depth bounds the sh -c
// scripts looked into.
func scriptWords(w []string, depth int) []string {
	w = unwrap(w)
	if len(w) == 0 {
		return nil
	}
	var out []string
	if strings.Contains(w[0], "/") {
		out = append(out, w[0])
	}
	head, args := filepath.Base(w[0]), w[1:]
	given := ""
	switch {
	case head == "sh" || head == "bash" || head == "zsh" || head == "dash":
		if i := slices.Index(args, "-c"); i >= 0 {
			if depth < 2 && i+1 < len(args) {
				for _, words := range simpleCommands(args[i+1]) {
					out = append(out, scriptWords(words, depth+1)...)
				}
			}
			return out
		}
		given = firstArg(args, "-o", "+o")
	case strings.HasPrefix(head, "python") || head == "ruby" || head == "perl":
		given = firstArg(args, "-c", "-e", "-W", "-X")
	case head == "node" || head == "tsx" || head == "ts-node":
		given = firstArg(args, "-e", "--eval", "-p", "--print", "-r", "--require", "--import", "--loader")
	case head == "go" && firstArg(args, "-C") == "run":
		if t := targets(args, "-C", "-exec", "-tags", "-ldflags", "-gcflags"); len(t) > 1 {
			given = t[1]
		}
	}
	if given != "" {
		out = append(out, given)
	}
	return out
}

// IsSuite reports whether command runs a whole suite: every test of a project or a tree of it (go test
// ./..., make test, npm test, a bare pytest or cargo test), an end-to-end run, or a lint or type-check of
// the whole project. A check narrowed to some tests or paths (go test -run, pytest tests/test_x.py, npm test
// -- parse) is none. Like IsCheck, it reads the line word by word with no shell.
func IsSuite(command string) bool {
	for _, words := range simpleCommands(command) {
		if suiteWords(words, 0) {
			return true
		}
	}
	return false
}

// suiteWords reports whether one simple command, as words, runs a whole suite; depth bounds the sh -c
// scripts looked into.
func suiteWords(w []string, depth int) bool {
	w = unwrap(w)
	if len(w) == 0 {
		return false
	}
	head, args := filepath.Base(w[0]), w[1:]
	switch head {
	case "sh", "bash", "zsh", "dash":
		if i := slices.Index(args, "-c"); depth < 2 && i >= 0 && i+1 < len(args) {
			for _, words := range simpleCommands(args[i+1]) {
				if suiteWords(words, depth+1) {
					return true
				}
			}
		}
		return false
	case "go":
		if sub := firstArg(args, "-C"); sub != "test" && sub != "vet" {
			return false
		}
		if slices.ContainsFunc(args, func(a string) bool { return a == "-run" || strings.HasPrefix(a, "-run=") }) {
			return false
		}
		return slices.ContainsFunc(args, func(a string) bool {
			return strings.HasSuffix(a, "...") || slices.Contains(strings.Split(a, "/"), "e2e")
		})
	case "make", "gmake", "just", "task", "mage", "turbo", "nx":
		return slices.ContainsFunc(targets(args, append(makeFlags, pmFlags...)...), checkName)
	case "npm", "pnpm", "yarn", "bun":
		// The project's test or check script, as it is: no tests or paths passed to it.
		t := targets(args, pmFlags...)
		if len(t) > 0 && head != "npm" && slices.Contains(checkTools, t[0]) {
			return suiteWords(t, depth+1) // pnpm vitest run: the tool itself
		}
		if !scriptCheck(head, t) {
			return false
		}
		switch t[0] {
		case "run", "run-script":
			return len(t) == 2 && !slices.Contains(args, "--")
		case "workspace":
			return len(t) == 3 && !slices.Contains(args, "--")
		case "exec", "dlx", "x":
			return len(t) > 1 && suiteWords(t[1:], depth+1)
		}
		return len(t) == 1 && !slices.Contains(args, "--")
	case "cargo":
		switch t := targets(args); {
		case len(t) == 0:
			return false
		case t[0] == "clippy" || t[0] == "check":
			return true
		case t[0] == "test" || t[0] == "nextest":
			return len(t) == 1 || t[0] == "nextest" && len(t) == 2 && t[1] == "run"
		}
		return false
	case "tsc", "vue-tsc":
		return slices.Contains(args, "--noEmit")
	case "playwright":
		t := targets(args)
		return len(t) == 1 && t[0] == "test"
	}
	if !slices.Contains(checkTools, head) && head != "biome" && head != "prettier" && head != "ruff" {
		return false
	}
	// A check tool given no tests or paths, or the whole project: pytest, vitest run, eslint ., ruff check.
	return !slices.ContainsFunc(targets(args), func(t string) bool {
		return !slices.Contains([]string{".", "./...", "run", "check", "lint", "ci", "format"}, t)
	})
}
