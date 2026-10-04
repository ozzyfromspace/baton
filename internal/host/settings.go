package host

import (
	"encoding/json"
	"strings"
)

// hookEvents are the Claude Code events baton listens to in the sessions it hosts. Hooks call the baton
// binary directly (exec form, no shell), so a hook costs ~4ms and always matches the running version.
var hookEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
	"PermissionDenied", "Notification", "Stop", "StopFailure", "SubagentStart", "SubagentStop",
	"PreCompact", "PostCompact",
}

// rewakeHooks run in the background ("asyncRewake") and may wake the idle model by exiting 2. They are
// separate from the synchronous hooks for the same events.
var rewakeHooks = []struct{ event, matcher, name string }{
	{"PostCompact", "manual", "PostCompactRewake"}, // auto-compaction happens mid-turn and continues by itself
	{"SessionStart", "resume", "SessionStartRewake"},
	{"Stop", "", "StopRewake"},
}

// Settings is the JSON baton passes to claude with --settings. It only exists for this session: no
// settings file is edited. It adds baton's hooks, a status line that wraps the user's own, and an allow
// rule for baton's own CLI so auto mode does not block the model from reporting progress.
func Settings(batonBin string) map[string]any {
	hooks := map[string]any{}
	for _, ev := range hookEvents {
		hooks[ev] = []any{map[string]any{"hooks": []any{execHook(batonBin, ev)}}}
	}
	for _, r := range rewakeHooks {
		h := execHook(batonBin, r.name)
		h["asyncRewake"] = true
		h["timeout"] = 600
		entry := map[string]any{"hooks": []any{h}}
		if r.matcher != "" {
			entry["matcher"] = r.matcher
		}
		hooks[r.event] = append(hooks[r.event].([]any), entry)
	}
	return map[string]any{
		"hooks":       hooks,
		"statusLine":  map[string]any{"type": "command", "command": shellQuote(batonBin) + " statusline", "padding": 0},
		"permissions": map[string]any{"allow": []any{"Bash(baton:*)"}},
	}
}

func execHook(batonBin, event string) map[string]any {
	return map[string]any{"type": "command", "command": batonBin, "args": []any{"hook", event}, "timeout": 30}
}

// SettingsJSON is Settings encoded for the command line.
func SettingsJSON(batonBin string) string {
	b, _ := json.Marshal(Settings(batonBin))
	return string(b)
}

// shellQuote quotes s for a POSIX shell (the status line command runs through one).
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
