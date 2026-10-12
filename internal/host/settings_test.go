package host

import "testing"

// The rewake runs after every compaction, whatever started it and whoever's it is: it is where baton finds
// the proof that the lead compacted, and gives the brief (docs/research/visible-workers.md).
func TestTheRewakeRunsAfterEveryCompaction(t *testing.T) {
	hooks := Settings("/b/baton")["hooks"].(map[string]any)
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
	hooks := Settings("/b/baton")["hooks"].(map[string]any)
	entries, _ := hooks["WorktreeCreate"].([]any)
	if len(entries) != 1 {
		t.Fatalf("WorktreeCreate: %v", hooks["WorktreeCreate"])
	}
	h := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if args := h["args"].([]any); args[1] != "WorktreeCreate" || h["timeout"] != 180 {
		t.Errorf("the hook %v", h)
	}
}
