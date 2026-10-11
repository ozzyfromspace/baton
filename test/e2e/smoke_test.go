//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const twoPhasePlan = `# Smoke plan

## P0 — Say hello
Run echo hello.

## P1 — Say goodbye
Run echo goodbye.
`

// Attach writes plan.md into dir and attaches it with the suggested spec, as the /baton skill would.
func Attach(t *testing.T, dir, doc string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "plan.md"), []byte(doc), 0o644)
	suggest := exec.Command(batonBin, "attach", "plan.md", "--suggest")
	suggest.Dir = dir
	spec, err := suggest.Output()
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	attach := exec.Command(batonBin, "attach", "plan.md", "--spec", "-")
	attach.Dir = dir
	attach.Stdin = strings.NewReader(string(spec))
	if out, err := attach.CombinedOutput(); err != nil {
		t.Fatalf("attach: %v\n%s", err, out)
	}
	// The plan is part of the project, as it would be in real use: a run starts from a clean tree.
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		Git(t, dir, "add", "plan.md")
		Git(t, dir, "commit", "-q", "-m", "docs: the plan")
	}
}

// The model, inside a hosted session, can run baton's CLI without a permission prompt (the allow rule
// baton passes via --settings), and the CLI sees the host's environment.
func TestHostedSessionSmoke(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := Start(t, dir, "--model", "haiku",
		`Run exactly this one command with the Bash tool: baton note "smoke test"`+"\nThen reply DONE.")
	s.Trust()
	started := s.WaitEvent("host_started", 30*time.Second)
	note := s.WaitEvent("note", 120*time.Second)
	if started == nil || note == nil {
		t.Fatalf("events: %v", s.Events())
	}
	if note["instance"] != started["instance"] || note["instance"] == nil {
		t.Fatalf("the CLI ran without the host's instance: host %v, cli %v", started["instance"], note["instance"])
	}
	if note["what"] != "smoke test" {
		t.Fatalf("note event: %v", note)
	}
}
