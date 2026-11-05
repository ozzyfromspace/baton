package host

import (
	"encoding/json"
	"testing"
)

// An idle session's status line runs again on a timer, so it catches up with baton's state between turns:
// every 10 seconds, or at the user's own interval, which this flag would otherwise override (spike 44).
func TestTheStatusLineRunsAgainWhileIdle(t *testing.T) {
	for _, c := range []struct {
		name string
		user float64
		want float64
	}{
		{"none set", 0, StatusRefresh},
		{"below Claude Code's least", 0.5, StatusRefresh},
		{"the user's slower one", 30, 30},
		{"the user's faster one", 2, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			var s struct {
				StatusLine struct {
					Type            string  `json:"type"`
					Command         string  `json:"command"`
					Padding         *int    `json:"padding"`
					RefreshInterval float64 `json:"refreshInterval"`
				} `json:"statusLine"`
			}
			if err := json.Unmarshal([]byte(SettingsJSON("/b/baton", c.user)), &s); err != nil {
				t.Fatal(err)
			}
			l := s.StatusLine
			if l.Type != "command" || l.Command != "/b/baton statusline" || l.Padding == nil || *l.Padding != 0 {
				t.Errorf("the status line %+v", l)
			}
			if l.RefreshInterval != c.want {
				t.Errorf("refreshInterval %v, want %v", l.RefreshInterval, c.want)
			}
		})
	}
}

// The rewake runs after every compaction, whatever started it and whoever's it is: it is where baton finds
// the proof that the lead compacted, and gives the brief (docs/research/visible-workers.md).
func TestTheRewakeRunsAfterEveryCompaction(t *testing.T) {
	hooks := Settings("/b/baton", 0)["hooks"].(map[string]any)
	found := 0
	for _, e := range hooks["PostCompact"].([]any) {
		entry := e.(map[string]any)
		h := entry["hooks"].([]any)[0].(map[string]any)
		if args := h["args"].([]any); args[1] != "PostCompactRewake" {
			continue
		}
		found++
		if m, ok := entry["matcher"]; ok || h["asyncRewake"] != true {
			t.Errorf("the rewake's matcher %v, async %v", m, h["asyncRewake"])
		}
	}
	if found != 1 {
		t.Fatalf("%d rewakes after PostCompact", found)
	}
}

// Every worktree the hosted session asks for is made by baton, which places phase agents in their phases'
// worktrees (docs/research/visible-workers.md); checking a project out takes longer than a hook's usual
// 30 seconds.
func TestBatonMakesTheSessionsWorktrees(t *testing.T) {
	hooks := Settings("/b/baton", 0)["hooks"].(map[string]any)
	entries, _ := hooks["WorktreeCreate"].([]any)
	if len(entries) != 1 {
		t.Fatalf("WorktreeCreate: %v", hooks["WorktreeCreate"])
	}
	h := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if args := h["args"].([]any); args[1] != "WorktreeCreate" || h["timeout"] != 180 {
		t.Errorf("the hook %v", h)
	}
}
