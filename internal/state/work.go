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
