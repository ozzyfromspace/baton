package state

import (
	"strings"

	"github.com/ozzyfromspace/baton/internal/gitx"
)

// Dirt is the uncommitted work in a repository at one moment.
type Dirt struct {
	// Files maps each uncommitted path to a fingerprint of its content (gitx.Fingerprint).
	Files map[string]string `json:"files,omitempty"`
	// Over: more than gitx.MaxDirty paths, too many to track one by one; the gate only warns.
	Over bool `json:"over,omitempty"`
}

// Origin is the repository as a phase found it. It is empty in a project without git.
type Origin struct {
	Head  string
	Dirty *Dirt
}

// OriginOf reads the origin of a phase starting now, in the working tree root.
func OriginOf(root string) Origin {
	if !gitx.Usable(root) {
		return Origin{}
	}
	o := Origin{Head: gitx.Head(root)}
	if paths, err := Uncommitted(root); err == nil {
		o.Dirty = &Dirt{}
		if len(paths) > gitx.MaxDirty {
			o.Dirty.Over = true
		} else {
			o.Dirty.Files = gitx.Fingerprint(root, paths)
		}
	}
	return o
}

// Uncommitted lists the uncommitted work in the working tree root: every path gitx.Dirty finds except
// baton's own files, which change all the time and are nobody's work (they are normally excluded from
// git, but not when git arrived after baton did, or the user tracks them). Without git it returns
// gitx.ErrNoGit.
func Uncommitted(root string) ([]string, error) {
	paths, err := gitx.Dirty(root)
	if err != nil {
		return nil, err
	}
	own := DirName + "/"
	work := paths[:0]
	for _, p := range paths {
		if !strings.HasPrefix(p, own) {
			work = append(work, p)
		}
	}
	return work, nil
}

// Work is the uncommitted work in a checkout, sorted by whether a phase made it.
type Work struct {
	Paths []string // every uncommitted path
	Fresh []string // new or changed since the phase started: the phase's own
	Kept  []string // already there when the phase started, and unchanged
	// Others are new or changed since the phase started, while another session works in the same
	// checkout, and this session's edit tools never wrote them: they may be that session's.
	Others []string
	// Why baton could not sort Paths: git status failed (Err), there is no record of what was
	// uncommitted when the phase started (NoStart), or there are too many paths to check one by one.
	Err     error
	NoStart bool
	TooMany bool
}

// Clean reports that the phase has left nothing uncommitted, as far as baton can tell.
func (w Work) Clean() bool { return w.Err == nil && !w.NoStart && !w.TooMany && len(w.Fresh) == 0 }

// PhaseWork sorts the uncommitted work in the run's working tree by whether phase id made it: what is
// uncommitted now against what was when the phase started. Without git there is none.
func (s *Store) PhaseWork(id string) Work {
	if !gitx.Usable(s.Root) {
		return Work{}
	}
	paths, err := Uncommitted(s.Root)
	if err != nil {
		return Work{Err: err}
	}
	if len(paths) == 0 {
		return Work{}
	}
	w := Work{Paths: paths}
	var start *Dirt
	var edited []string
	if st, err := s.Load(); err == nil && st.Phases[id] != nil {
		start, edited = st.Phases[id].StartDirty, st.Phases[id].Edited
	}
	switch {
	case start == nil:
		w.NoStart = true
		return w
	case start.Over || len(paths) > gitx.MaxDirty:
		w.TooMany = true
		return w
	}
	w.Fresh, w.Kept = gitx.Since(start.Files, gitx.Fingerprint(s.Root, paths))
	if s.Worker() != "" {
		return w // a worker has its worktree to itself
	}
	if p, err := OpenProject(s.Project, s.Now); err == nil && len(p.Others(s.Ref)) > 0 {
		mine := map[string]bool{}
		for _, e := range edited {
			mine[e] = true
		}
		fresh := w.Fresh
		w.Fresh = nil
		for _, f := range fresh {
			if mine[f] {
				w.Fresh = append(w.Fresh, f)
			} else {
				w.Others = append(w.Others, f)
			}
		}
	}
	return w
}
