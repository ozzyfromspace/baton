package state

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The temporary files interrupted writes left anywhere in a project's .baton directory (a run, a worker's
// sub-run, the mod's bridge, the project's own records) go when the host starts (Sweep), once a minute
// old; one written now stays, and so does every file that is not a write's temporary file.
func TestSweepRemovesOldTemporaryFiles(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p, err := OpenProject(filepath.Join(t.TempDir(), DirName), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	file := func(rel string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		rel   string
		age   time.Duration
		swept bool
	}{
		{"runs/r1/.state.json.tmp-1", time.Hour, true},
		{"runs/r1/workers/P1/.state.json.tmp-2", 2 * time.Minute, true},
		{"runs/r1/mod/.bridge.json.tmp-3", TempAge, true},
		{"runs/r1/mod/out/.000004.json.tmp-4", 90 * time.Second, true},
		{"runs/r1/jobs/.j1.log.tmp-5", 24 * time.Hour, true},
		{"sessions/.s1.tmp-6", time.Hour, true},
		{"runs/r1/.state.json.tmp-7", 30 * time.Second, false},       // being written
		{"runs/r1/workers/P2/.state.json.tmp-8", time.Second, false}, // being written
		{"runs/r1/state.json", time.Hour, false},
		{"runs/r1/.state.json", time.Hour, false},
		{"runs/r1/notes.tmp-9", time.Hour, false},
	}
	file("runs/r1/plan.json", time.Hour) // a run with a plan, which the sweep keeps
	var want []string
	for _, c := range cases {
		if path := file(c.rel, c.age); c.swept {
			want = append(want, path)
		}
	}
	slices.Sort(want)
	stale := StaleTemps(p.Dir, now)
	slices.Sort(stale)
	if !slices.Equal(stale, want) {
		t.Errorf("stale:\n%v\nwant\n%v", stale, want)
	}
	p.Sweep()
	for _, c := range cases {
		_, err := os.Stat(filepath.Join(p.Dir, filepath.FromSlash(c.rel)))
		if gone := err != nil; gone != c.swept {
			t.Errorf("%s: swept %v, want %v", c.rel, gone, c.swept)
		}
	}
	if fresh := StaleTemps(p.Dir, now.Add(time.Minute)); !slices.Equal(fresh, []string{
		filepath.Join(p.Dir, "runs", "r1", ".state.json.tmp-7"),
		filepath.Join(p.Dir, "runs", "r1", "workers", "P2", ".state.json.tmp-8"),
	}) {
		t.Errorf("a minute on: %v", fresh)
	}
	if got := StaleTemps(filepath.Join(p.Dir, "nowhere"), now); got != nil {
		t.Errorf("a directory that is not there: %v", got)
	}
}
