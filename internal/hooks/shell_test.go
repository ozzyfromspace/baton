package hooks

import "testing"

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
