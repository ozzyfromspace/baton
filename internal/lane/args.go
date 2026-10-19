package lane

import (
	"regexp"
	"strings"
	"unicode"
)

// A runner's own arguments for only the tests a stage failed in (Preset.Args), which baton sets as
// EnvFailedArgs on a rerun narrowed to them. Vitest, jest and Playwright read no variable that selects tests,
// so a stage of theirs runs fewer only when its command passes these on: `playwright test $BATON_FAILED_ARGS`.
// The words go unquoted through a shell, so none may hold a space: a test name's spaces become \s in a
// pattern, and an id with one gives no arguments at all.

// goArgs runs only the top-level tests ids: go test's -run, as goNarrow sets it through GOFLAGS.
func goArgs(ids []string) string { return words([]string{"-run=" + FailedRE(ids)}) }

// plainWords passes the ids as they are: pytest's node ids, Playwright's file:line locations.
func plainWords(ids []string) string { return words(ids) }

// jsArgs runs the files of vitest's or jest's tests ids, only the tests by their names (-t, a pattern matched
// within each test's full name); an id with no test in it (a file) runs its files whole.
func jsArgs(ids []string) string {
	var files, names []string
	whole := false
	for _, id := range ids {
		file, _, ok := strings.Cut(id, jsSep)
		files = appendNew(files, file)
		if !ok {
			whole = true
			continue
		}
		names = appendNew(names, spaceless(regexp.QuoteMeta(jsName(id))))
	}
	if whole || len(names) == 0 {
		return words(files)
	}
	return words(append(files, "-t", strings.Join(names, "|")))
}

// spaceless writes each run of spaces in pattern as \s+, which matches it.
func spaceless(pattern string) string {
	return strings.Join(strings.FieldsFunc(pattern, unicode.IsSpace), `\s+`)
}

// words joins ws by spaces, "" when one is empty or holds a space.
func words(ws []string) string {
	for _, w := range ws {
		if w == "" || strings.IndexFunc(w, unicode.IsSpace) >= 0 {
			return ""
		}
	}
	return strings.Join(ws, " ")
}
