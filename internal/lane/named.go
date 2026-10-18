package lane

import (
	"slices"
	"strings"
)

// Unnamed lists the tests among fs that command does not name, in their order. A command names a test when
// one of its words holds one of the test's names (Preset.Names; a ::baton fail line's id for a test no
// runner read) whole, not as part of a longer name: as the word is, its quotes taken off, or with a
// pattern's escapes taken off too (`-t 'adds 1 \+ 2'` names `adds 1 + 2`).
func Unnamed(command string, fs []FailedTest) []FailedTest {
	var words []string
	for _, c := range simpleCommands(command) {
		for _, w := range c {
			words = append(words, w)
			if u := unescaped(w); u != w {
				words = append(words, u)
			}
		}
	}
	var out []FailedTest
	for _, f := range fs {
		named := slices.ContainsFunc(namesOf(f), func(n string) bool {
			return slices.ContainsFunc(words, func(w string) bool { return holds(w, n) })
		})
		if !named {
			out = append(out, f)
		}
	}
	return out
}

// namesOf are the ways a command can name test f: its runner's (Preset.Names), or its id.
func namesOf(f FailedTest) []string {
	if p, ok := PresetFor(f.Runner); ok {
		return p.Names(f)
	}
	return []string{f.ID}
}

// holds reports whether word holds name whole: TestParse in `^(TestParse|TestLex)$`, not in TestParseAll.
func holds(word, name string) bool {
	if name == "" {
		return false
	}
	for from := 0; from < len(word); {
		at := strings.Index(word[from:], name)
		if at < 0 {
			return false
		}
		at += from
		end := at + len(name)
		before := at == 0 || !nameByte(word[at-1]) || !nameByte(name[0])
		after := end == len(word) || !nameByte(word[end]) || !nameByte(name[len(name)-1])
		if before && after {
			return true
		}
		from = at + 1
	}
	return false
}

// nameByte reports a byte that goes on a name: a letter, a digit, an underscore, or part of a character
// outside ASCII.
func nameByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

// unescaped is word with each backslash before a character that goes on no name taken off, as a pattern
// escapes it (regexp.QuoteMeta).
func unescaped(word string) string {
	if !strings.Contains(word, `\`) {
		return word
	}
	var b strings.Builder
	for i := 0; i < len(word); i++ {
		if word[i] == '\\' && i+1 < len(word) && !nameByte(word[i+1]) {
			i++
		}
		b.WriteByte(word[i])
	}
	return b.String()
}
