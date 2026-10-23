package lane

import "strings"

// File is the file test f is in, as its id names it: a pytest node id's path, a vitest or jest id's file (or
// the file that failed whole), a Playwright test's file (its id is `file:line`). "" for a go test, whose
// package is its Scope (or, failed whole, its id), and for an id a fail line gave, which names no runner.
func (f FailedTest) File() string {
	switch f.Runner {
	case RunPytest:
		file, _, _ := strings.Cut(f.ID, "::")
		return file
	case RunVitest, RunJest:
		file, _, _ := strings.Cut(f.ID, jsSep)
		return file
	case RunPlaywright:
		if i := strings.LastIndexByte(f.ID, ':'); i > 0 {
			return f.ID[:i]
		}
		return f.ID
	}
	return ""
}
