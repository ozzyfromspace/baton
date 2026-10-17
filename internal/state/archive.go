package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// same one), with the log of every job of it that failed in jobs/. It returns the folder, "" for a run
// that keeps nothing (Record). It writes the folder at once: from inside an Update, take the Record there
// and Save it once the lock is let go.
func (s *Store) Archive(st State) (string, error) {
	r, err := s.Record(st)
	if err != nil {
		return "", err
	}
	return r.Save()
}

// A Record is what Archive keeps of a plan, read into memory: an Update that replaces the plan takes it
// (Store.Record) with the state it holds before changing it, and writes it (Save) once its lock is let go,
// so that nothing is copied under the lock.
type Record struct {
	Generation int
	dir        string            // the run's directory
	files      map[string][]byte // what its folder holds, by name
	logs       []string          // the logs and stages files of the plan's red jobs, copied into its jobs/ as Save writes it
}

// recordFiles are the files of a Record, in the order Save writes them.
var recordFiles = []string{"state.json", "plan.json", "handoff.md"}

// Record reads what Archive keeps of the plan st runs: st, and the run's plan.json and handoff.md as they
// are now, and names the logs of its red jobs, on record or dropped from it (KeptLogs), which Save copies.
// It takes no lock, so an Update may call it. A run with no plan attached, or a worker's sub-run, keeps
// nothing (nil).
func (s *Store) Record(st State) (*Record, error) {
	if s.Parent != "" || st.Mode == ModeIdle {
		return nil, nil
	}
	if _, err := os.Stat(s.PlanPath()); err != nil {
		return nil, nil
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	r := &Record{Generation: st.Generation, dir: s.Dir, files: map[string][]byte{"state.json": append(b, '\n')}}
	for _, f := range []string{s.PlanPath(), s.HandoffPath()} {
		data, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		r.files[filepath.Base(f)] = data
	}
	ids := slices.Clone(st.KeptLogs)
	for _, j := range st.Jobs {
		if j.Red() {
			ids = append(ids, j.ID)
		}
	}
	for _, id := range ids {
		r.logs = append(r.logs, s.JobLog(id), s.JobStages(id))
	}
	return r, nil
}

// Save writes r into its folder, gen-<N>/ in the run's directory (gen-<N>.2 and on when that one is there
// already), and returns the folder; a nil Record keeps nothing (""). Two processes saving records of one
// generation at once each get a folder of their own.
func (r *Record) Save() (string, error) {
	if r == nil {
		return "", nil
	}
	name := archivePrefix + strconv.Itoa(r.Generation)
	dir := filepath.Join(r.dir, name)
	for n := 2; ; n++ {
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		dir = filepath.Join(r.dir, fmt.Sprintf("%s.%d", name, n))
	}
	for _, f := range recordFiles {
		if data, ok := r.files[f]; ok {
			if err := writeAtomic(filepath.Join(dir, f), data); err != nil {
				return dir, err
			}
		}
	}
	for _, l := range r.logs {
		copyLog(l, filepath.Join(dir, "jobs", filepath.Base(l)))
	}
	return dir, nil
}

// copyLog copies the job log at from to to, making its folder; best effort, as a log that is gone (one
// pruned before baton kept them) has nothing to keep.
func copyLog(from, to string) {
	in, err := os.Open(from)
	if err != nil {
		return
	}
	defer in.Close()
	if os.MkdirAll(filepath.Dir(to), 0o755) != nil {
		return
	}
	out, err := os.Create(to)
	if err != nil {
		return
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(to)
		return
	}
	out.Close()
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
