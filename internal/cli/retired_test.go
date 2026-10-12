package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baton waiting is gone: long commands move to the background by themselves, and baton await waits on
// what is outside the project. Nothing may still mention it, or the model would be told to run a command
// that no longer exists. Only the record keeps it: the changelog and the research (spikes, docs/research).
func TestBatonWaitingIsGone(t *testing.T) {
	root := filepath.Join("..", "..")
	traces := []string{"baton waiting", "`waiting`", "|waiting|", `"waiting"`, "SetWaiting", "MaxWait", "WaitGrace", "WAIT_GRACE", ".Waiting"}
	record := map[string]bool{"CHANGELOG.md": true, "spikes": true, "dist": true, filepath.Join("docs", "research"): true,
		filepath.Join("internal", "cli", "retired_test.go"): true}
	text := map[string]bool{".go": true, ".md": true, ".sh": true, ".ps1": true, ".json": true, ".golden": true, ".yml": true, ".yaml": true, ".txt": true}
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case record[rel] && d.IsDir(), d.IsDir() && rel != "." && strings.HasPrefix(d.Name(), "."): // .git, and a run's own .baton
			return filepath.SkipDir
		case record[rel] || d.IsDir() || !text[filepath.Ext(path)] && d.Name() != "Makefile":
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for i, line := range strings.Split(string(b), "\n") {
			for _, trace := range traces {
				if strings.Contains(line, trace) {
					t.Errorf("%s:%d still mentions baton waiting (%q): %s", rel, i+1, trace, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil || checked < 100 {
		t.Fatalf("checked %d files: %v", checked, err)
	}
}
