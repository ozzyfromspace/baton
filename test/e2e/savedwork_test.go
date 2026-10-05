//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The run halts on a hard block with work nobody committed. The model may keep it uncommitted only by
// saying why (--keep-dirty); either way baton saves it into a private ref as the halt begins, and leaves
// the project exactly as it was.
func TestBlockedSavesUncommittedWork(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	head := Git(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "draft.txt"), []byte("half a greeting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Start(t, dir, "--model", "haiku",
		`This phase needs a decision only the human can make. Run exactly: baton blocked "need the human to pick a greeting" `+
			`--tried "the plan does not say which greeting" --keep-dirty "draft.txt stays a draft until the human picks" `+
			`— then end your turn and follow baton's instructions.`)
	s.Trust()
	waitSequence(t, s, 3*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "escalated" && e["type"] == "blocked" },
		func(e map[string]any) bool { return e["kind"] == "snapshot" && e["ref"] != "" },
	)
	var ref string
	for _, e := range s.Events() {
		if e["kind"] == "snapshot" {
			ref, _ = e["ref"].(string)
			break
		}
	}
	if !strings.HasPrefix(ref, "refs/baton/snapshots/P0-") {
		t.Fatalf("snapshot ref %q", ref)
	}
	if got := Git(t, dir, "show", ref+":draft.txt"); got != "half a greeting\n" {
		t.Fatalf("the snapshot holds draft.txt as %q", got)
	}
	if got := Git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved from %s to %s", head, got)
	}
	if got := Git(t, dir, "status", "--porcelain"); got != "?? draft.txt\n" {
		t.Fatalf("the project changed: git status %q", got)
	}
}

const plainPlan = `# Plain plan

## P0 — Say hello
Write the word hello into hello.txt with the Write tool.

## P1 — Say goodbye
Write the word goodbye into goodbye.txt with the Write tool.
`

// A folder that is not a git repository runs a plan the same way: nothing asks for a commit, nothing
// is refused for files left as they are, nothing is snapshotted, and no repository appears.
func TestPlainFolderRunsThePlan(t *testing.T) {
	dir := NewPlainProject(t)
	Attach(t, dir, plainPlan)
	s := Start(t, dir, "--model", "haiku", "--permission-mode", "acceptEdits",
		"Start the attached plan, and follow baton's instructions.")
	s.Trust()
	waitSequence(t, s, 8*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "attached" && e["git"] == false },
		func(e map[string]any) bool { return e["kind"] == "host_started" && e["git"] == false },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P1" },
		kind("plan_complete"),
	)
	for _, f := range []string{"hello.txt", "goodbye.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("the plan's work: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("a git repository was created in a plain folder")
	}
	for _, e := range s.Events() {
		switch e["kind"] {
		case "refused", "snapshot", "snapshot_failed", "escalated":
			t.Errorf("unexpected in a plain folder: %v", e)
		}
	}
}
