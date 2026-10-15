package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// archivePrefix names the folders a run keeps its earlier plans in: gen-<N>, after the plan's generation.
const archivePrefix = "gen-"

// Archive keeps the record of the plan st runs before something replaces it (an attach over it, a
// revision, a drop): st as state.json, and the run's plan.json and handoff.md as they are now, copied into
// gen-<N>/ in the run's directory, N being st's generation (gen-<N>.2 and on for another record of the
// same one). It takes no lock, so an Update may call it with the state it holds before changing it. A run
// with no plan attached, or a worker's sub-run, keeps nothing (""). It returns the folder.
func (s *Store) Archive(st State) (string, error) {
	if s.Parent != "" || st.Mode == ModeIdle {
		return "", nil
	}
	if _, err := os.Stat(s.PlanPath()); err != nil {
		return "", nil
	}
	name := archivePrefix + strconv.Itoa(st.Generation)
	dir := s.path(name)
	for n := 2; exists(dir); n++ {
		dir = s.path(fmt.Sprintf("%s.%d", name, n))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(dir, "state.json"), append(b, '\n')); err != nil {
		return "", err
	}
	for _, f := range []string{s.PlanPath(), s.HandoffPath()} {
		data, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return dir, err
		}
		if err := writeAtomic(filepath.Join(dir, filepath.Base(f)), data); err != nil {
			return dir, err
		}
	}
	return dir, nil
}

// Archived is an earlier plan of a run, kept by Archive.
type Archived struct {
	Dir        string // its folder
	Generation int
	Title      string
	Phases     int
	Done       int // its phases done when it was replaced
}

// Archives lists the run's earlier plans (Archive), oldest first. A folder baton cannot read is listed
// with what it could read of it.
func (s *Store) Archives() []Archived {
	entries, _ := os.ReadDir(s.Dir)
	var out []Archived
	for _, e := range entries {
		gen, ok := strings.CutPrefix(e.Name(), archivePrefix)
		if !e.IsDir() || !ok {
			continue
		}
		gen, _, _ = strings.Cut(gen, ".")
		n, err := strconv.Atoi(gen)
		if err != nil {
			continue
		}
		a := Archived{Dir: filepath.Join(s.Dir, e.Name()), Generation: n}
		if p, err := plan.Load(filepath.Join(a.Dir, "plan.json")); err == nil {
			a.Title, a.Phases = p.Title, len(p.Phases)
		}
		var st State
		if b, err := os.ReadFile(filepath.Join(a.Dir, "state.json")); err == nil && json.Unmarshal(b, &st) == nil {
			for _, ps := range st.Phases {
				if ps != nil && ps.Status == PhaseDone {
					a.Done++
				}
			}
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Generation != out[j].Generation {
			return out[i].Generation < out[j].Generation
		}
		return len(out[i].Dir) < len(out[j].Dir) || len(out[i].Dir) == len(out[j].Dir) && out[i].Dir < out[j].Dir
	})
	return out
}
