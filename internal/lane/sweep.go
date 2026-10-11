package lane

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ozzyfromspace/baton/internal/gitx"
)

// lanes lists the lanes the pool has anything of: a worktree, a lock or an up marker.
func (p Pool) lanes() []int {
	entries, _ := os.ReadDir(p.Dir())
	seen := map[int]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".lock"), ".up")
		if n, err := strconv.Atoi(name); err == nil && n > 0 {
			seen[n] = true
		}
	}
	ns := make([]int, 0, len(seen))
	for n := range seen {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	return ns
}

// Lane is one lane as it stands, for baton status.
type Lane struct {
	N    int
	Dir  string
	Busy bool   // a job holds it
	UpBy string // the host that brought it up; "" when it is not up
}

// Lanes lists the pool's lanes as they stand.
func (p Pool) Lanes() []Lane {
	var out []Lane
	for _, n := range p.lanes() {
		l := Lane{N: n, Dir: p.LaneDir(n), UpBy: p.upBy(n)}
		switch s, err := p.tryLane(n); {
		case s != nil:
			s.Release()
		case err == nil:
			l.Busy = true
		}
		out = append(out, l)
	}
	return out
}

// Sweep removes the lanes nobody owns: no job holds them, and no live host brought them up. Each gets
// its down (if it is up), and its worktree is removed; then git forgets the lanes' worktrees that are
// gone, and only theirs (gitx.PruneUnder): never the human's, such as one on a drive not mounted. live
// reports whether a host is still running (nil: none is). Output goes to w. It returns the lanes it
// removed.
func (p Pool) Sweep(ctx context.Context, live func(host string) bool, w io.Writer) []int {
	if _, err := os.Stat(p.Dir()); err != nil {
		return nil
	}
	var swept []int
	for _, n := range p.lanes() {
		s, err := p.tryLane(n)
		if s == nil || err != nil {
			continue
		}
		if by := p.upBy(n); by != "" && live != nil && live(by) {
			s.Release()
			continue
		}
		if err := p.down(n, w); err != nil {
			fmt.Fprintf(w, "[baton] %v\n", err)
		}
		if _, err := os.Stat(s.Dir); err == nil {
			gitx.WorktreeRemove(ctx, p.Root, s.Dir)
			os.RemoveAll(s.Dir)
			swept = append(swept, n)
		}
		s.Release() // the lock file stays: removing it would let two jobs lock two different files
	}
	gitx.PruneUnder(ctx, p.Root, p.Dir())
	return swept
}

// Down brings down the lanes host brought up that no job holds, at the end of the host's run (it exits,
// or its plan is dropped or finished). Their worktrees stay, for the next host's sweep. Output goes to w.
// It returns the lanes it brought down.
func (p Pool) Down(host string, w io.Writer) []int {
	var downed []int
	for _, n := range p.lanes() {
		if by := p.upBy(n); by == "" || by != host {
			continue
		}
		s, err := p.tryLane(n)
		if s == nil || err != nil {
			continue
		}
		if err := p.down(n, w); err != nil {
			fmt.Fprintf(w, "[baton] %v\n", err)
		}
		downed = append(downed, n)
		s.Release()
	}
	return downed
}

// Sweepable lists the lanes Sweep would remove now, with live as Sweep takes it, and touches none.
func (p Pool) Sweepable(live func(host string) bool) []int {
	var out []int
	for _, l := range p.Lanes() {
		if fi, err := os.Stat(l.Dir); err == nil && fi.IsDir() && !l.Busy && (l.UpBy == "" || live == nil || !live(l.UpBy)) {
			out = append(out, l.N)
		}
	}
	return out
}
