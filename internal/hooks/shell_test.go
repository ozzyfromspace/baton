package hooks

import (
	"slices"
	"testing"
)

func TestBatonShell(t *testing.T) {
	for _, c := range []struct {
		cmd     string
		baton   bool
		verdict string
	}{
		{`baton status`, true, shellSimple},
		{`baton done P3 --notes "Added the parser; tests pass."`, true, shellSimple},
		{`baton done P3 --notes 'Added parse() in cmd/x.go — it's done'`, true, shellComplex}, // unbalanced: an apostrophe ends the quote
		{`baton done P3 --notes 'Added parse() in cmd/x.go; done'`, true, shellSimple},
		{"baton done P3 --notes \"multi\nline\"", true, shellSimple},
		{"baton done P3 --notes \"Added `parse()` in x.go\"", true, shellSubstitution},
		{`baton done P3 --notes "costs $5"`, true, shellSubstitution},
		{`baton done P3 --notes "see $(cat x)"`, true, shellSubstitution},
		{`baton done P3 --notes "escaped \$5 and \` + "`" + `x\` + "`" + `"`, true, shellSimple},
		{`baton attach plan.md --spec '{"title": "x", "phases": [{"id": "P0", "anchor": "## P0 $ (x)"}]}'`, true, shellSimple},
		{`baton done P3 && git push`, true, shellComplex},
		{`baton attach plan.md --spec - <<'EOF'`, true, shellComplex},
		{`baton status | head`, true, shellComplex},
		{`batonx status`, false, ""},
		{`echo baton`, false, ""},
		{`cd x && baton status`, false, ""},
	} {
		baton, verdict := batonShell(c.cmd)
		if baton != c.baton || verdict != c.verdict {
			t.Errorf("%q: %v %q, want %v %q", c.cmd, baton, verdict, c.baton, c.verdict)
		}
	}
}

func TestBatonCommandsAreAllowedOrFixed(t *testing.T) {
	f := newFixture(t, true)
	decision := func(cmd string) string {
		out := f.fire("PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})
		hso, _ := out["hookSpecificOutput"].(map[string]any)
		d, _ := hso["permissionDecision"].(string)
		return d
	}
	if d := decision(`baton done P0 --notes "first phase done"`); d != "allow" {
		t.Errorf("plain: %q", d)
	}
	if d := decision("baton done P0 --notes \"added `x()`\""); d != "deny" {
		t.Errorf("substitution: %q", d)
	}
	if d := decision(`baton status | head`); d != "" {
		t.Errorf("complex: %q", d)
	}
	if d := decision(`ls`); d != "" {
		t.Errorf("not baton: %q", d)
	}
}

// A phase agent's gate given as one quoted text is rewritten as words after --, which Claude Code's
// isolation guard accepts; one with shell syntax runs through sh -c as it did. A command that needs no
// rewriting, or that baton cannot read, is left as it is.
func TestAGateIsGivenAsWords(t *testing.T) {
	for _, c := range []struct{ cmd, want string }{
		{`baton verify 'sh hold.sh' --notes 'n'`, `baton verify --notes n -- sh hold.sh`},
		{`baton await "sh wait.sh A" --notes "the wait"`, `baton await --notes 'the wait' -- sh wait.sh A`},
		{`baton verify --here 'bash ci.sh' --notes='marker committed'`, `baton verify --here '--notes=marker committed' -- bash ci.sh`},
		{`baton verify 'go test ./... -run=TestX' --notes n`, `baton verify --notes n -- go test ./... -run=TestX`},
		{`baton verify 'go test ./... && go vet ./...' --notes n`, `baton verify --notes n -- sh -c 'go test ./... && go vet ./...'`},
		{`baton verify "pytest -k 'a or b'" --notes n`, `baton verify --notes n -- sh -c 'pytest -k '\''a or b'\'''`},
		{`baton verify 'echo $HOME' --notes n`, `baton verify --notes n -- sh -c 'echo $HOME'`},
		{`baton verify sh hold.sh --notes n`, `baton verify --notes n -- sh hold.sh`},
		{`baton verify --notes n -- sh hold.sh`, ""}, // words already
		{`baton verify make --notes n`, ""},    // one word is never refused
		{`baton verify 'sh x.sh' --bogus`, ""}, // the CLI says what is wrong itself
		{`baton await 'sh x.sh' --here`, ""},   // await has no --here
		{`baton done P1 --notes 'sh x.sh'`, ""},
		{`baton verify "$(cat gate)"`, ""}, // substitution: refused as it stands
		{`baton verify 'sh x.sh' && echo`, ""},
		{`baton verify 'sh x.sh`, ""},
	} {
		got, ok := asWords(c.cmd)
		if ok != (c.want != "") || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.cmd, got, ok, c.want)
		}
	}
}

// The words of a plain command are the shell's: quotes go, a backslash keeps what follows.
func TestShellWords(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want []string
	}{
		{`a 'b c' "d e"`, []string{"a", "b c", "d e"}},
		{`a  b	c`, []string{"a", "b", "c"}},
		{`a\ b "x\"y" "x\y" 'x\y'`, []string{"a b", `x"y`, `x\y`, `x\y`}},
		{`a '' ""`, []string{"a", "", ""}},
		{`'it'\''s'`, []string{"it's"}},
	} {
		if got, ok := shellWords(c.cmd); !ok || !slices.Equal(got, c.want) {
			t.Errorf("%s: %q %v, want %q", c.cmd, got, ok, c.want)
		}
	}
	if _, ok := shellWords(`a 'b`); ok {
		t.Error("an open quote read")
	}
}
