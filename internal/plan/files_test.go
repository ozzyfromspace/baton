package plan

import (
	"reflect"
	"testing"
)

// A section names a file path in a code span whose whole text is one: with a slash, or a file name with an
// extension files have. Other code, URLs, flags, home paths, words and fenced code are left out; a path is
// named as written, less a leading ./, a trailing / and a line number, and once.
func TestPaths(t *testing.T) {
	for _, c := range []struct {
		name, section string
		want          []string
	}{
		{"a path", "Change `internal/plan/graph.go`.", []string{"internal/plan/graph.go"}},
		{"a file name", "Edit `CLAUDE.md`, `go.mod` and `words.go`.", []string{"CLAUDE.md", "go.mod", "words.go"}},
		{"a directory, a dotted one, a glob", "In `internal/plan/`, `.baton/runs` and `internal/hooks/*_test.go`.",
			[]string{"internal/plan", ".baton/runs", "internal/hooks/*_test.go"}},
		{"tidied", "See `./cmd/baton/main.go`, `internal/hooks/stop.go:42`, `stop.go:42-50` and `a/b.go#L7`.",
			[]string{"cmd/baton/main.go", "internal/hooks/stop.go", "stop.go", "a/b.go"}},
		{"an absolute path", "Write `/etc/baton/config.json`.", []string{"/etc/baton/config.json"}},
		{"named twice, once", "`a/b.go`, then `a/b.go` again, and `./a/b.go`.", []string{"a/b.go"}},
		{"a code span of two backticks", "Run ``internal/x.go`` here.", []string{"internal/x.go"}},
		{"in the heading", "## P3 — Fix `internal/hooks/stop.go`\n\nWork.", []string{"internal/hooks/stop.go"}},
		{"code spans of code", "Run `go test ./internal/plan/...`, call `plan.Shape` and `decide.ReplanBrief`, `make test`, `f(a/b)`, `x = a/b.go`.", nil},
		{"identifiers that end in a word", "`Loop.lapse`, `console.log`, `process.env`, `state.Proof.At`.", nil},
		{"URLs and import paths", "`https://example.com/a/b.go`, `www.example.com/x`, `github.com/ozzyfromspace/baton`, `golang.org/x/term`.", nil},
		{"words, flags, commands", "`baton`, `After: none`, `--qa`, `-v`, `/baton`, `/compact`, `P3`, `1/2`, `none`.", nil},
		{"home paths and placeholders", "`~/.baton/bin`, `$HOME/x.go`, `.baton/runs/<run>/workers`, `{a,b}.go`.", nil},
		{"dotfiles and odd names", "`.gitignore`, `.catch`, `.go`, `a//b`, `/`.", nil},
		{"fenced code", "Run:\n\n```sh\ncat `internal/x.go`\n```\n\n~~~\n`internal/y.go`\n~~~\n", nil},
		{"text that is not code", "Change internal/plan/graph.go and CLAUDE.md.", nil},
		{"a stray backtick pairs nothing past its paragraph", "A ` alone.\n\nThen `internal/z.go`.", []string{"internal/z.go"}},
		{"an unclosed run of two", "A `` alone, then `internal/z.go`.", []string{"internal/z.go"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Paths(c.section); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Paths(%q) = %q, want %q", c.section, got, c.want)
			}
		})
	}
}

// overlapDoc is a plan whose phases name files: P0 and P1 need no other phase and share a file; P2 waits
// for P0, so it may not run beside it, but may beside P1; P3 waits for P2, and P0 through it; P4 names
// nothing; P5 names a file no other does.
const overlapDoc = "# Files\n\n## Standing rules\n\nKeep `internal/decide/words.go` tidy.\n\n" +
	"## P0 — Schema\n\nAfter: none\n\nChange `internal/state/state.go` and `internal/decide/words.go`.\n\n" +
	"## P1 — API\n\nAfter: none\n\nChange `internal/decide/words.go`, `internal/state/state.go` and `cmd/baton/main.go`.\n\n" +
	"## P2 — CLI\n\nAfter: P0\n\nChange `cmd/baton/main.go` and `internal/state/state.go`.\n\n" +
	"## P3 — Docs\n\nAfter: P2\n\nChange `internal/decide/words.go` and `docs/design.md`.\n\n" +
	"## P4 — Rest\n\nAfter: none\n\nNothing named.\n\n" +
	"## P5 — Web\n\nAfter: none\n\nChange `web/app.ts`.\n\n" +
	"## Verification\n\nRead `docs/design.md`.\n"

// The phases that may run at once and whose sections name the same paths are found in pairs, in document
// order; a pair where one phase waits for the other, directly or through others, is not one, nor are
// phases left out of ids, the standing rules or the end section.
func TestOverlaps(t *testing.T) {
	doc := []byte(overlapDoc)
	p, err := Build("files.md", doc, Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	all := []string{"P0", "P1", "P2", "P3", "P4", "P5"}
	for _, c := range []struct {
		name string
		ids  []string
		want []Overlap
	}{
		{"every phase", all, []Overlap{
			{A: "P0", B: "P1", Paths: []string{"internal/state/state.go", "internal/decide/words.go"}},
			{A: "P1", B: "P2", Paths: []string{"internal/state/state.go", "cmd/baton/main.go"}},
			{A: "P1", B: "P3", Paths: []string{"internal/decide/words.go"}},
		}},
		{"the phases not started", []string{"P2", "P3", "P4", "P5"}, nil},
		{"P1 left out", []string{"P0", "P2", "P3"}, nil},
		{"two that share", []string{"P1", "P3"}, []Overlap{{A: "P1", B: "P3", Paths: []string{"internal/decide/words.go"}}}},
		{"none", nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := p.Overlaps(doc, c.ids); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Overlaps(%v) = %+v, want %+v", c.ids, got, c.want)
			}
		})
	}
	if got := p.Overlaps([]byte("# Files\n\nAnother text.\n"), all); got != nil {
		t.Errorf("a text without the phases' sections: %+v", got)
	}
}
