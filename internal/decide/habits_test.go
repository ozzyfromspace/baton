package decide

import (
	"strings"
	"testing"
)

// The background habits name baton's jobs for checks and outside waits (await alone without git), the
// declaration a background shell's description makes, its output as it goes, and its stop; each form stays
// under 450 characters.
func TestBackgroundHabits(t *testing.T) {
	common := []string{
		"Describe each background shell by what it is for, its phase and when it ends: `~15m`, or `until P12's browser checks pass`.",
		"Have it print a line every few minutes at least (a verbose runner, an `echo` per step), so silence means stuck.",
		"Stop it once its job is done, and before `baton done`.",
	}
	for _, c := range []struct {
		name      string
		git       bool
		want, not string
	}{
		{"with git", true, "run a check with `baton verify` and a wait on something outside the project with `baton await`, never in a background shell.", ""},
		{"without git", false, "run a check, or a wait on something outside the project, with `baton await`, never in a background shell.", "verify"},
	} {
		got := BackgroundHabits(c.git)
		for _, w := range append([]string{c.want}, common...) {
			if !strings.Contains(got, w) {
				t.Errorf("%s: lacks %q:\n%s", c.name, w, got)
			}
		}
		if c.not != "" && strings.Contains(got, c.not) {
			t.Errorf("%s: says %q:\n%s", c.name, c.not, got)
		}
		if n := len([]rune(got)); n > 450 {
			t.Errorf("%s: runs to %d characters:\n%s", c.name, n, got)
		}
	}
}

// A shell that declared no end is told so: with no description, and with one quoted back.
func TestNoEndDeclared(t *testing.T) {
	if got := NoEndDeclared(""); !strings.HasPrefix(got, "[baton] This shell declared no end: it has no description. ") ||
		!strings.Contains(got, "(`~15m`, or `until <a condition>`)") {
		t.Errorf("no description: %s", got)
	}
	if got := NoEndDeclared("dev server"); !strings.HasPrefix(got, "[baton] This shell declared no end: its description (dev server) names neither a run time (`~15m`) nor a condition (`until <a condition>`).") {
		t.Errorf("a description: %s", got)
	}
}
