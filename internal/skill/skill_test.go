package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// line builds one transcript entry as Claude Code writes it.
func line(t *testing.T, v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInConversation(t *testing.T) {
	load := line(t, map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "Base directory for this skill: /Users/x/.claude/plugins/cache/baton/baton/0.1.1/skills/baton\n\n# /baton\n..."},
	}}})
	loadString := line(t, map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"role": "user",
		"content": "Base directory for this skill: /repo/plugin/skills/baton\n\n# /baton"}})
	other := line(t, map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"role": "user",
		"content": "Base directory for this skill: /Users/x/.claude/plugins/cache/stripe/stripe/1.0/skills/test-cards\n"}})
	quoted := line(t, map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "input": map[string]any{"command": "grep 'Base directory for this skill: /a/skills/baton' x"}},
	}}})
	boundary := line(t, map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted"})
	prompt := line(t, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hello"}})

	for name, c := range map[string]struct {
		lines []string
		want  bool
	}{
		"never loaded":               {[]string{prompt, prompt}, false},
		"loaded":                     {[]string{prompt, load, prompt}, true},
		"loaded, as a string":        {[]string{loadString}, true},
		"compacted since":            {[]string{load, prompt, boundary, prompt}, false},
		"loaded after a compaction":  {[]string{load, boundary, loadString, prompt}, true},
		"another plugin's skill":     {[]string{other}, false},
		"the phrase only quoted":     {[]string{quoted}, false},
		"a last line with no ending": {[]string{prompt, load}, true},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			body := strings.Join(c.lines, "\n")
			if name != "a last line with no ending" {
				body += "\n"
			}
			os.WriteFile(path, []byte(body), 0o644)
			if got, err := InConversation(path); err != nil || got != c.want {
				t.Fatalf("InConversation = %v, %v; want %v", got, err, c.want)
			}
		})
	}
	if _, err := InConversation(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Error("a missing transcript is an error")
	}
}

// The instructions name every subcommand's section; the stub's checks are in package cli.
func TestText(t *testing.T) {
	if !strings.Contains(Text, "## start") || !strings.Contains(Text, "## drop") || strings.Contains(Text, "$ARGUMENTS") {
		t.Fatalf("instructions:\n%s", Text[:200])
	}
}
