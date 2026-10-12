//go:build windows

package lane

import "testing"

// The tests that use these skip on Windows (needSh).
func running(int) bool { return false }
func pgid(int) int     { return 0 }

// A command given as words keeps each word whole for cmd: a word with a space or a character cmd reads
// otherwise is double-quoted.
func TestJoin(t *testing.T) {
	for _, c := range []struct {
		words []string
		want  string
	}{
		{[]string{"go", "test", "./..."}, "go test ./..."},
		{[]string{"sh", "-c", "a && b"}, `sh -c "a && b"`},
		{[]string{"echo", `say "hi"`, ""}, `echo "say \"hi\"" ""`},
	} {
		if got := Join(c.words); got != c.want {
			t.Errorf("%q: %q, want %q", c.words, got, c.want)
		}
	}
}
