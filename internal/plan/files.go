package plan

import (
	"regexp"
	"slices"
	"strings"
)

// Phases that run at once each work on a branch of their own, and their work lands on the run's branch one
// after the other: two that change the same file collide when the later one lands. A replan sees which
// phases not started may run at once and name the same files (Overlaps), from the paths their sections name
// (Paths), so it can weigh that as it orders them (shape rule 4, split by files).

// Paths returns the file paths section names, in the order it first names each: the inline code spans
// (`internal/plan/graph.go`, `CLAUDE.md`) whose whole text looks like a path. Code spans that hold other
// code (`go test ./...`, `plan.Shape`), URLs, flags, home paths and single words are left out, and so is
// fenced code. A path is named as written, less a leading ./, a trailing / and a line number
// (`stop.go:42`).
func Paths(section string) []string {
	var out []string
	seen := map[string]bool{}
	for _, para := range paragraphs(section) {
		for _, span := range codeSpans(para) {
			if p, ok := asPath(span); ok && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// paragraphs splits text into its paragraphs outside fenced code, so that a stray backtick pairs with
// nothing beyond its own paragraph.
func paragraphs(text string) []string {
	lines := strings.Split(text, "\n")
	code := fenced(lines)
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for i, l := range lines {
		if code[i] || strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return out
}

// codeSpans returns the text of each inline code span of text: a run of backticks closed by the next run
// of the same length. A run that nothing closes is plain text.
func codeSpans(text string) []string {
	var out []string
	run := func(i int) int {
		n := 0
		for i+n < len(text) && text[i+n] == '`' {
			n++
		}
		return n
	}
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		n := run(i)
		open, end := i+n, -1
		for j := open; j < len(text); {
			k := strings.IndexByte(text[j:], '`')
			if k < 0 {
				break
			}
			k += j
			m := run(k)
			if m == n {
				end = k
				break
			}
			j = k + m
		}
		if end < 0 {
			i = open
			continue
		}
		out = append(out, text[open:end])
		i = end + n
	}
	return out
}

// lineRef is a line number after a path: "stop.go:42", "stop.go:42-50", "stop.go:42:7", "stop.go#L42".
var lineRef = regexp.MustCompile(`(?::\d+(?:-\d+)?(?::\d+)?|#L\d+(?:-L?\d+)?)$`)

// notPath holds characters a path baton counts never has: they mark code, a URL, a placeholder or a list.
const notPath = " \t\n()[]{}<>\"'=,;:|&$!?\\`^%#"

// asPath reads a code span's text as a path: ok when the whole of it looks like one. One with a slash
// counts unless it is a URL or a Go import path (its first part a domain, github.com/…), a slash command
// (/baton) or a home path (~/.baton); one without a slash only with an extension files have (fileExt), so
// identifiers such as plan.Shape are left out.
func asPath(span string) (string, bool) {
	p := strings.TrimSpace(span)
	p = lineRef.ReplaceAllString(p, "")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	switch {
	case p == "" || p == "/" || strings.ContainsAny(p, notPath):
		return "", false
	case strings.HasPrefix(p, "~"), strings.HasPrefix(p, "-"), strings.HasPrefix(p, "@"), strings.HasPrefix(strings.ToLower(p), "www."):
		return "", false
	case !strings.ContainsFunc(p, isLetter): // 1/2
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if slices.Contains(parts, "") {
		return "", false
	}
	if len(parts) == 1 {
		return p, hasFileExt(parts[0]) // a word, or a slash command (/baton), has none
	}
	if first := parts[0]; !strings.HasPrefix(p, "/") && first != ".." && strings.Contains(strings.TrimPrefix(first, "."), ".") {
		return "", false // a domain: github.com/ozzyfromspace/baton, golang.org/x/term
	}
	return p, true
}

func isLetter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

// hasFileExt reports a file name with an extension files have (fileExt): words.go, CLAUDE.md, go.mod. A
// dotfile alone (.catch, .gitignore) is left out, since a method reads the same.
func hasFileExt(name string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || !strings.ContainsFunc(name[:dot], func(r rune) bool { return isLetter(r) || r >= '0' && r <= '9' }) {
		return false
	}
	return fileExt[name[dot+1:]]
}

// fileExt holds the extensions of the files plans name. Lower case only, as plan.Shape and state.Proof are
// identifiers; and none that reads as a common field or method too (console.log, process.env).
var fileExt = func() map[string]bool {
	m := map[string]bool{}
	for _, e := range strings.Fields(`go mod sum work md mdx markdown txt rst adoc json jsonc jsonl ndjson yaml yml toml ini cfg conf
		lock js mjs cjs ts mts cts tsx jsx vue svelte astro css scss sass less html htm xml svg py pyi ipynb rb erb rs c h cc cpp cxx hpp
		hh java kt kts groovy gradle scala swift m mm cs fsx vb php pl pm lua sh bash zsh fish ps1 psm1 psd1 bat cmd sql proto
		graphql gql tf tfvars hcl nix zig dart ex exs erl hrl hs elm ml mli clj cljs edn jl csv tsv pdf png jpg jpeg gif webp
		ico wasm patch diff tmpl tpl gotmpl mk cmake plist xcconfig entitlements storyboard xib pbxproj sln csproj fsproj vcxproj
		dockerfile containerfile`) {
		m[e] = true
	}
	return m
}()

// Overlap is two phases that may run at once, neither waiting for the other directly or through others,
// whose sections name the same paths.
type Overlap struct {
	A, B  string   // the phases, in document order
	Paths []string // the paths both name, in the order A's section names them
}

// Overlaps returns the pairs of the phases ids names that may run at once and whose sections in doc name
// the same paths (Paths), in document order. A phase whose section doc does not have is passed over.
func (p Plan) Overlaps(doc []byte, ids []string) []Overlap {
	text := string(doc)
	type named struct {
		id    string
		paths []string
		has   map[string]bool
		after map[string]bool // every phase it waits for, directly or through others
	}
	var phs []named
	for i, ph := range p.Phases {
		if !slices.Contains(ids, ph.ID) {
			continue
		}
		sec, err := p.section(text, i)
		if err != nil {
			continue
		}
		n := named{id: ph.ID, paths: Paths(sec), has: map[string]bool{}, after: map[string]bool{}}
		if len(n.paths) == 0 {
			continue
		}
		for _, path := range n.paths {
			n.has[path] = true
		}
		for _, a := range p.Ancestors(ph.ID) {
			n.after[a] = true
		}
		phs = append(phs, n)
	}
	var out []Overlap
	for i, a := range phs {
		for _, b := range phs[i+1:] {
			if a.after[b.id] || b.after[a.id] {
				continue
			}
			var shared []string
			for _, path := range a.paths {
				if b.has[path] {
					shared = append(shared, path)
				}
			}
			if len(shared) > 0 {
				out = append(out, Overlap{A: a.id, B: b.id, Paths: shared})
			}
		}
	}
	return out
}
