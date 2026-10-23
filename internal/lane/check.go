package lane

import (
	"path/filepath"
	"slices"
	"strings"
)

// IsCheck reports whether command, a shell command line, runs a check: a test suite, a lint, a type-check
// or a measurement (go test, pytest, vitest, a make target named test, `pnpm lint`, and so on). It is the
// one table of them baton keeps, read word by word with no shell: each simple command of the line (split at
// &&, ||, ;, | and &) is looked at past its variable assignments and wrappers (env, time, timeout, npx,
// uv run, python -m and the like), and a `sh -c` script is looked into. A command it cannot read is no check.
func IsCheck(command string) bool {
	for _, words := range simpleCommands(command) {
		if checkWords(words, 0) {
			return true
		}
	}
	return false
}

// checkWords reports whether one simple command, as words, runs a check; depth bounds the sh -c scripts
// looked into.
func checkWords(w []string, depth int) bool {
	w = unwrap(w)
	if len(w) == 0 {
		return false
	}
	head, args := filepath.Base(w[0]), w[1:]
	switch head {
	case "sh", "bash", "zsh", "dash":
		if depth < 2 {
			if i := slices.Index(args, "-c"); i >= 0 && i+1 < len(args) {
				for _, words := range simpleCommands(args[i+1]) {
					if checkWords(words, depth+1) {
						return true
					}
				}
			}
		}
		return false
	case "go":
		return slices.Contains([]string{"test", "vet"}, firstArg(args, "-C"))
	case "cargo":
		return slices.Contains([]string{"test", "nextest", "clippy", "check", "bench"}, firstArg(args))
	case "make", "gmake", "just", "task", "mage":
		return slices.ContainsFunc(targets(args, makeFlags...), checkName)
	case "npm", "pnpm", "yarn", "bun":
		return scriptCheck(head, targets(args, pmFlags...))
	case "turbo", "nx":
		return slices.ContainsFunc(targets(args, pmFlags...), checkName)
	case "tsc", "vue-tsc":
		return slices.Contains(args, "--noEmit")
	case "biome":
		return slices.Contains([]string{"check", "lint", "ci"}, firstArg(args))
	case "prettier":
		return slices.Contains(args, "--check") || slices.Contains(args, "-c")
	case "ruff":
		return firstArg(args) == "check" || firstArg(args) == "format" && slices.Contains(args, "--check")
	case "playwright":
		return firstArg(args) == "test"
	case "gradle", "gradlew", "mvn", "mvnw":
		return slices.ContainsFunc(targets(args, "-p", "-f", "--file"), func(t string) bool { return checkName(t) || t == "verify" })
	case "dotnet", "mix", "deno", "swift":
		return firstArg(args) == "test"
	}
	return slices.Contains(checkTools, head)
}

// checkTools are commands that are a check whatever their arguments.
var checkTools = []string{
	"pytest", "py.test", "unittest", "tox", "nox", "mypy", "pyright", "flake8", "pylint",
	"vitest", "jest", "mocha", "ava", "eslint", "stylelint", "svelte-check",
	"golangci-lint", "staticcheck", "govulncheck", "gotestsum",
	"rspec", "phpunit", "ctest", "shellcheck", "hadolint", "lighthouse", "k6",
}

// checkParts are the words a make target or a package script named for a check has as one of its parts
// (test, test:unit, mod-test, type-check, e2e).
var checkParts = []string{"test", "tests", "lint", "check", "typecheck", "e2e", "drive", "perf", "budget", "bench", "vet"}

// checkName reports whether a target or script name names a check: one of its parts, split at : - _ . and
// /, is a check word.
func checkName(name string) bool {
	return slices.ContainsFunc(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == ':' || r == '-' || r == '_' || r == '.' || r == '/'
	}), func(p string) bool { return slices.Contains(checkParts, p) })
}

// scriptCheck reports whether a package manager's command, as its targets, runs a check: its test command
// (npm test, bun test), or a script named for one (pnpm lint, npm run test:e2e, yarn workspace web
// typecheck). npm runs no script by its bare name.
func scriptCheck(pm string, t []string) bool {
	if len(t) == 0 {
		return false
	}
	switch t[0] {
	case "test", "t":
		return true
	case "run", "run-script":
		return len(t) > 1 && checkName(t[1])
	case "exec", "dlx", "x":
		return len(t) > 1 && checkWords(t[1:], 1)
	case "workspace":
		return len(t) > 2 && checkName(t[2])
	}
	return pm != "npm" && checkName(t[0])
}

// The options whose value is the next word, of make and its kin, and of package managers.
var (
	makeFlags = []string{"-C", "-f", "--file", "--makefile", "-I", "-o", "-W", "--directory"}
	pmFlags   = []string{"-C", "--dir", "--filter", "-F", "--prefix", "-w", "--workspace", "--cwd", "--project"}
)

// targets are a command's arguments that are not options, the value of one of valueFlags, variable
// assignments or redirections.
func targets(args []string, valueFlags ...string) []string {
	var t []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case slices.Contains(valueFlags, a):
			i++
		case strings.HasPrefix(a, "-"), assignment(a), strings.ContainsAny(a, "<>"):
		default:
			t = append(t, a)
		}
	}
	return t
}

// firstArg is a command's first argument that is not an option (nor the value of one of valueFlags), or "".
func firstArg(args []string, valueFlags ...string) string {
	if t := targets(args, valueFlags...); len(t) > 0 {
		return t[0]
	}
	return ""
}

// unwrap drops a simple command's leading variable assignments and the wrappers that run the command after
// them: env, time, timeout 600, nice, npx, uv run, python -m and the like.
func unwrap(w []string) []string {
	for len(w) > 0 {
		head := filepath.Base(w[0])
		switch {
		case assignment(w[0]):
			w = w[1:]
		case head == "env" || head == "time" || head == "nice" || head == "nohup" || head == "command" ||
			head == "caffeinate" || head == "npx" || head == "bunx" || head == "pnpx" || head == "exec":
			w = w[1:]
			for len(w) > 0 && (strings.HasPrefix(w[0], "-") || assignment(w[0])) {
				w = w[1:]
			}
		case head == "timeout" || head == "gtimeout":
			w = w[1:]
			for len(w) > 0 && strings.HasPrefix(w[0], "-") {
				w = w[1:]
			}
			if len(w) > 0 {
				w = w[1:] // the duration
			}
		case (head == "uv" || head == "poetry" || head == "pipenv" || head == "hatch" || head == "pdm" || head == "rye") && len(w) > 1 && w[1] == "run",
			head == "bundle" && len(w) > 1 && w[1] == "exec":
			w = w[2:]
			for len(w) > 0 && strings.HasPrefix(w[0], "-") {
				w = w[1:]
			}
		case strings.HasPrefix(head, "python") && len(w) > 2 && w[1] == "-m":
			w = w[2:]
		default:
			return w
		}
	}
	return w
}

// assignment reports whether word is a shell variable assignment (NAME=value).
func assignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if !(r == '_' || 'A' <= r && r <= 'Z' || 'a' <= r && r <= 'z' || i > 0 && '0' <= r && r <= '9') {
			return false
		}
	}
	return true
}

// simpleCommands splits a shell command line into its simple commands, each as words: quotes are taken off
// and a backslash escapes the next character, and the line is cut at &&, ||, ;, |, &, a newline and
// parentheses. A here-document's body, the lines after the one that opened it up to its closing line, is
// no command (openHereDoc, pastHereDocs). It reads no expansion, which is enough to tell what each command
// runs.
func simpleCommands(line string) [][]string {
	var (
		cmds  [][]string
		words []string
		word  strings.Builder
		inW   bool
		docs  []hereDoc // opened on this line, their bodies starting after it
	)
	endWord := func() {
		if inW {
			words = append(words, word.String())
			word.Reset()
			inW = false
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
		case c == '\\' && i+1 < len(line):
			i++
			if line[i] != '\n' {
				word.WriteByte(line[i])
				inW = true
			}
		case c == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				end = len(line) - i - 1
			}
			word.WriteString(line[i+1 : i+1+end])
			inW, i = true, i+1+end
		case c == '"':
			i++
			for ; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0 {
					i++
				}
				word.WriteByte(line[i])
			}
			inW = true
		case c == ' ' || c == '\t':
			endWord()
		case c == '<' && strings.HasPrefix(line[i:], "<<<"): // a here-string
			word.WriteString("<<<")
			inW, i = true, i+2
		case c == '<' && strings.HasPrefix(line[i:], "<<"):
			d, kept, end, ok := openHereDoc(line, i)
			if !ok {
				word.WriteString("<<")
				inW, i = true, i+1
				continue
			}
			endWord()
			words, docs, i = append(words, kept), append(docs, d), end-1
		case c == ';' || c == '&' || c == '|' || c == '\n' || c == '(' || c == ')':
			if c == '&' && i > 0 && line[i-1] == '>' {
				word.WriteByte(c) // 2>&1
				inW = true
				continue
			}
			endCmd()
			if c == '\n' && len(docs) > 0 { // the here-documents' bodies are no commands
				i, docs = pastHereDocs(line, i+1, docs)-1, nil
			}
		default:
			word.WriteByte(c)
			inW = true
		}
	}
	endCmd()
	return cmds
}
