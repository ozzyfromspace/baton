package gitx

import (
	"bufio"
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The risk ledger (package risk) reads what a phase's commits changed: its own, never the merges that
// brought other work in. A phase agent's are the commits its landing brought (state.Landing.Commits), a lead
// phase's those its checkout made from where the phase started until it was done (CommitsUntil).

// FileStat is what a set of commits changed in one path: the lines they added and deleted (none for a file
// git counts as binary), and whether they created it, or deleted a file that was there before them.
type FileStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added,omitempty"`
	Deleted int    `json:"deleted,omitempty"`
	Binary  bool   `json:"binary,omitempty"`
	Created bool   `json:"created,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

// Stats sums what commits changed, path by path, in the repository at root: merges are left out (what they
// bring is other commits' work), and a rename counts as a deletion and an addition. commits are given oldest
// first, so a path created and then deleted by them is neither created nor removed. It is sorted by path.
func Stats(ctx context.Context, root string, commits []string) ([]FileStat, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	if len(commits) == 0 {
		return nil, nil
	}
	in := strings.Join(commits, "\n") + "\n"
	out, err := run(ctx, root, nil, strings.NewReader(in), "-c", "core.quotePath=false", "log", "--stdin", "--no-walk=unsorted", "--no-merges",
		"--no-renames", "--numstat", "--summary", "--format=%x01%H")
	if err != nil {
		return nil, err
	}
	return parseStats(out), nil
}

// parseStats reads the output of git log --numstat --summary, a commit's lines after a line that starts
// with \x01: "added\tdeleted\tpath" (- for a binary file's counts), then " create mode <mode> <path>" and
// " delete mode <mode> <path>".
func parseStats(out []byte) []FileStat {
	byPath := map[string]*FileStat{}
	created := map[string]bool{} // created by these commits, deleted or not since
	get := func(p string) *FileStat {
		f := byPath[p]
		if f == nil {
			f = &FileStat{Path: p}
			byPath[p] = f
		}
		return f
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "" || strings.HasPrefix(line, "\x01"):
		case strings.HasPrefix(line, " create mode "), strings.HasPrefix(line, " delete mode "):
			rest := strings.TrimPrefix(strings.TrimPrefix(line, " create mode "), " delete mode ")
			_, p, ok := strings.Cut(rest, " ")
			if !ok {
				continue
			}
			p = unquote(p)
			f := get(p)
			if strings.HasPrefix(line, " create mode ") {
				created[p], f.Created, f.Removed = true, true, false
			} else {
				f.Created, f.Removed = false, !created[p]
			}
		default:
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) != 3 {
				continue
			}
			f := get(unquote(parts[2]))
			a, errA := strconv.Atoi(parts[0])
			d, errD := strconv.Atoi(parts[1])
			if errA != nil || errD != nil {
				f.Binary = parts[0] == "-" || f.Binary
				continue
			}
			f.Added += a
			f.Deleted += d
		}
	}
	stats := make([]FileStat, 0, len(byPath))
	for _, f := range byPath {
		stats = append(stats, *f)
	}
	slices.SortFunc(stats, func(a, b FileStat) int { return strings.Compare(a.Path, b.Path) })
	return stats
}

// unquote is a path as git wrote it, less the C-style quotes it puts around one with unusual characters.
func unquote(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// CommitsUntil lists the commits of to that from does not have, parents before their children, merges left
// out, committed by until (any time, for zero).
func CommitsUntil(ctx context.Context, root, from, to string, until time.Time) ([]string, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	args := []string{"rev-list", "--reverse", "--topo-order", "--no-merges"}
	if !until.IsZero() {
		args = append(args, "--until="+until.UTC().Format("2006-01-02 15:04:05 +0000"))
	}
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := run(ctx, root, nil, nil, append(args, rng)...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}
