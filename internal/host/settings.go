package host

import (
	"encoding/json"
	"strings"
)

// hookEvents are the Claude Code events baton listens to in the sessions it hosts. Hooks call the baton
// binary directly (exec form, no shell), so a hook costs ~4ms and always matches the running version.
var hookEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest",
	"PermissionDenied", "Notification", "Stop", "StopFailure", "SubagentStart", "SubagentStop",
	"PreCompact", "PostCompact", "WorktreeCreate",
}

// hookTimeouts are the hooks that may take longer than a hook's usual 30 seconds: WorktreeCreate checks the
// project out.
var hookTimeouts = map[string]int{"WorktreeCreate": 180}

// rewakeHooks run in the background ("asyncRewake") and may wake the idle model by exiting 2. They are
// separate from the synchronous hooks for the same events.
var rewakeHooks = []struct{ event, matcher, name string }{
	// Every compaction's, whoever's: it waits for proof that the lead compacted, and then gives the brief.
	{"PostCompact", "", "PostCompactRewake"},
	{"SessionStart", "resume", "SessionStartRewake"},
}

// Settings is the JSON baton passes to claude with --settings. It only exists for this session: no
// settings file is edited. It adds baton's hooks, a status line that wraps the user's own, and an allow
// rule for baton's own CLI so auto mode does not block the model from reporting progress.
//
// Its status line runs again every StatusRefresh seconds (refreshInterval), or every refresh seconds when
// the user's own settings give an interval: Claude Code otherwise runs it only as the conversation changes,
// so an idle lead's line kept saying what baton's state said at its last turn. Claude Code merges statusLine
// across settings key by key, and this flag's value wins over every file's, so the user's own interval is
// passed on as it is, never made faster or slower. Text that has not changed is not drawn again, so a line
// that says the same keeps the screen still for serial mode's quiet gate (spike 44).
//
// It also turns off idle compaction: Claude Code compacts a long conversation (200k tokens or more) on
// its own when the session has been idle for most of the prompt cache's hour. baton decides when a
// hosted session compacts, and a session may idle on purpose mid-phase (docs/research/parallel.md).
func Settings(batonBin string, refresh float64) map[string]any {
	if refresh < 1 {
		refresh = StatusRefresh
	}
	hooks := map[string]any{}
	for _, ev := range hookEvents {
		h := execHook(batonBin, ev)
		if t := hookTimeouts[ev]; t > 0 {
			h["timeout"] = t
		}
		hooks[ev] = []any{map[string]any{"hooks": []any{h}}}
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
		"hooks":          hooks,
		"statusLine":     map[string]any{"type": "command", "command": shellQuote(batonBin) + " statusline", "padding": 0, "refreshInterval": refresh},
		"permissions":    map[string]any{"allow": []any{"Bash(baton:*)"}},
		"idleCompaction": false,
	}
}

// StatusRefresh is how often, in seconds, a hosted session's status line runs again while nothing else asks
// for it, unless the user's settings give their own interval (spike 44).
const StatusRefresh = 10

func execHook(batonBin, event string) map[string]any {
	return map[string]any{"type": "command", "command": batonBin, "args": []any{"hook", event}, "timeout": 30}
}

// SettingsJSON is Settings encoded for the command line.
func SettingsJSON(batonBin string, refresh float64) string {
	b, _ := json.Marshal(Settings(batonBin, refresh))
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
