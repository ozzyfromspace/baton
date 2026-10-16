package state

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TempAge is how long the temporary file of a write (WriteFile: ".<name>.tmp-…", beside the file it
// replaces) goes unchanged before it counts as one an interrupted write left: a write fills its temporary
// file and renames it within moments.
const TempAge = time.Minute

// StaleTemps lists the temporary files that interrupted writes left under dir, a project's .baton
// directory (its runs, workers' sub-runs, the mod's bridges and the project's own records), unchanged for
// TempAge at now.
func StaleTemps(dir string, now time.Time) []string {
	var temps []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isTemp(d.Name()) {
			return nil
		}
		if fi, err := d.Info(); err == nil && now.Sub(fi.ModTime()) >= TempAge {
			temps = append(temps, path)
		}
		return nil
	})
	return temps
}

// isTemp reports the name of a write's temporary file (writeAtomic, and a job log's trim).
func isTemp(name string) bool { return strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-") }

// SweepTemps removes the temporary files that interrupted writes left in the project (StaleTemps), and
// returns those it removed.
func (p *Project) SweepTemps() []string {
	var removed []string
	for _, t := range StaleTemps(p.Dir, p.Now()) {
		if os.Remove(t) == nil {
			removed = append(removed, t)
		}
	}
	return removed
}
