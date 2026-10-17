// Package lane runs a command on a commit in an isolated lane, detached from the session, with a capped
// log. A lane is a worktree of the project's repository kept under baton's home, outside the working
// tree, so file watchers, hot reload and git clean never reach it:
//
//	$BATON_HOME/lanes/<key>/root     the working tree these lanes belong to
//	$BATON_HOME/lanes/<key>/<n>/     lane n: a worktree, reused from one job to the next
//	$BATON_HOME/lanes/<key>/<n>.lock held (flock) by the job running in lane n
//	$BATON_HOME/lanes/<key>/<n>.up   the host that ran the lane's up, while the lane is up
//	$BATON_HOME/lanes/<key>/waits/   a file for each job waiting for a lane, held (flock) while it waits
//
// <key> is derived from the working tree's path, so every run in one checkout shares its lanes.
//
// A project may commit an executable .baton-lane at its root to set its lanes up (see Script). Without
// one, a command runs with `sh -c` in the bare worktree, one lane at a time.
package lane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
	"github.com/ozzyfromspace/baton/internal/gitx"
)

// Script is the name of the lane script a project commits at its root. It is run from the job's commit,
// in the lane, as
//
//	.baton-lane up <dir>          once per lane per baton host, before its first command
//	.baton-lane run <dir> <cmd>   for each command; its exit code is the command's
//	.baton-lane down <dir>        when baton is done with the lane (exit, drop, a finished plan)
//	.baton-lane slots             prints how many lanes may run at once (1 to MaxSlots)
//
// with BATON_LANE (the lane's number), BATON_LANE_DIR, BATON_ROOT (the project's working tree) and
// BATON_COMMIT set, the directory of the baton running the job first on the PATH, and every other GIT_*,
// BATON_* and CLAUDE_* variable removed. A run also gets the job's variables of the gate protocol
// (BATON_JOB, BATON_STAGES, BATON_RERUN), to pass on to the command.
const Script = ".baton-lane"

// MaxSlots is the most lanes a project may run at once.
const MaxSlots = 4

// Pool is the lanes of one working tree.
type Pool struct {
	Home string // baton's home ($BATON_HOME, or ~/.baton)
	Root string // the project's working tree
	// Bin is the directory of the baton binary that runs the pool's jobs, put first on the PATH of what it
	// runs for them, so that a gate finds `baton stage` in every lane; "" leaves PATH as it is.
	Bin string
}

// Dir is where the pool's lanes live.
func (p Pool) Dir() string { return filepath.Join(p.Home, "lanes", Key(p.Root)) }

// LaneDir is lane n's worktree.
func (p Pool) LaneDir(n int) string { return filepath.Join(p.Dir(), strconv.Itoa(n)) }

func (p Pool) lockPath(n int) string { return filepath.Join(p.Dir(), fmt.Sprintf("%d.lock", n)) }
func (p Pool) upPath(n int) string   { return filepath.Join(p.Dir(), fmt.Sprintf("%d.up", n)) }

// Key names a working tree's lanes after its path, resolved, so two spellings of one path agree. A
// worker's worktrees are kept under the same name (package loop).
func Key(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:8])
}

// runnerArgs reports whether a command line, split on whitespace, is a job's runner's:
// `<baton> lane-run <ref> <job>`, wherever the binary is.
func runnerArgs(args []string) bool { return len(args) >= 4 && args[len(args)-3] == "lane-run" }

// scriptTimeout bounds `.baton-lane slots`, which only prints a number.
const scriptTimeout = 10 * time.Second

// Slots asks the project's lane script how many lanes it may run at once. Without a script, or when the
// answer is not a number from 1 to MaxSlots, it is 1 (an answer over MaxSlots is MaxSlots).
func (p Pool) Slots(ctx context.Context) int {
	script := filepath.Join(p.Root, Script)
	if !isFile(script) {
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, script, "slots")
	cmd.Dir, cmd.Env = p.Root, p.env(0, "", gitx.Head(p.Root))
	out, err := cmd.Output()
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	switch {
	case err != nil || n < 1:
		return 1
	case n > MaxSlots:
		return MaxSlots
	}
	return n
}

// A Slot is a lane held for one job. The lock is released when the job's process ends, however it ends.
type Slot struct {
	N    int
	Dir  string
	lock *flock.Flock
}

// Release lets another job have the lane.
func (s *Slot) Release() error { return s.lock.Unlock() }

// TryAcquire holds the first free lane of n, or returns nil when all n are in use.
func (p Pool) TryAcquire(n int) (*Slot, error) {
	if err := p.prepare(); err != nil {
		return nil, err
	}
	for i := 1; i <= n; i++ {
		if s, err := p.tryLane(i); s != nil || err != nil {
			return s, err
		}
	}
	return nil, nil
}

// acquirePoll is how often Acquire looks for a free lane.
const acquirePoll = 250 * time.Millisecond

// Acquire holds the first lane of n to come free, waiting until one does or ctx ends.
func (p Pool) Acquire(ctx context.Context, n int) (*Slot, error) {
	for {
		if s, err := p.TryAcquire(n); s != nil || err != nil {
			return s, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(acquirePoll):
		}
	}
}

// A Waiter is a job's place among the jobs waiting for a lane of the pool: a file in its waits directory,
// named for when the job began to wait, and held (flock) while it waits, so that a waiter whose process
// ended counts no more.
type Waiter struct{ lock *flock.Flock }

// waits numbers this process's waiters, so that two begun at the same instant have files of their own.
var waits atomic.Int64

// Wait records that a job began to wait for a lane at since, and returns its place, with how many jobs
// that began to wait before it wait still. A lane goes to whichever waiter looks first once it comes free,
// so the count says how long the queue was, not when the job's turn comes. A file no live waiter holds is
// removed as it is found. With no place to record it (nil), the job waits all the same.
func (p Pool) Wait(since time.Time) (*Waiter, int) {
	dir := filepath.Join(p.Dir(), "waits")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0
	}
	name := fmt.Sprintf("%020d-%010d-%010d", since.UnixNano(), os.Getpid(), waits.Add(1)) // in the order they began
	l := flock.New(filepath.Join(dir, name))
	if ok, err := l.TryLock(); err != nil || !ok {
		return nil, 0
	}
	entries, _ := os.ReadDir(dir)
	ahead := 0
	for _, e := range entries {
		if e.Name() >= name {
			continue
		}
		other := flock.New(filepath.Join(dir, e.Name()))
		if ok, err := other.TryLock(); err == nil && ok {
			other.Unlock() // its job waits no more
			os.Remove(other.Path())
			continue
		}
		ahead++
	}
	return &Waiter{l}, ahead
}

// Done gives up the waiter's place: its job holds a lane, or waits no more.
func (w *Waiter) Done() {
	if w == nil {
		return
	}
	w.lock.Unlock()
	os.Remove(w.lock.Path())
}

// tryLane holds lane n if it is free.
func (p Pool) tryLane(n int) (*Slot, error) {
	l := flock.New(p.lockPath(n))
	ok, err := l.TryLock()
	if err != nil {
		return nil, fmt.Errorf("locking lane %d: %w", n, err)
	}
	if !ok {
		return nil, nil
	}
	return &Slot{N: n, Dir: p.LaneDir(n), lock: l}, nil
}

// prepare makes the pool's directory, recording which working tree it serves for whoever looks inside.
func (p Pool) prepare() error {
	if err := os.MkdirAll(p.Dir(), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(p.Dir(), "root")); errors.Is(err, os.ErrNotExist) {
		os.WriteFile(filepath.Join(p.Dir(), "root"), []byte(p.Root+"\n"), 0o644)
	}
	return nil
}

// checkout puts commit in the slot's lane. A lane already there is reused, keeping its ignored caches;
// one that is missing or broken is made afresh, and has not been brought up.
func (p Pool) checkout(ctx context.Context, s *Slot, commit string) error {
	if isFile(filepath.Join(s.Dir, ".git")) {
		if err := gitx.CheckoutDetach(ctx, s.Dir, commit); err == nil {
			return nil
		}
	}
	gitx.WorktreeRemove(ctx, p.Root, s.Dir)
	os.RemoveAll(s.Dir)
	os.Remove(p.upPath(s.N))
	gitx.PruneUnder(ctx, p.Root, p.Dir())
	return gitx.WorktreeAdd(ctx, p.Root, s.Dir, commit)
}

// upBy returns the host that brought lane n up, or "" when it is not up.
func (p Pool) upBy(n int) string {
	b, err := os.ReadFile(p.upPath(n))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// HasScript reports whether the working tree dir has a lane script.
func HasScript(dir string) bool { return script(dir) != "" }

// script returns the lane script in dir, or "" when the commit there has none.
func script(dir string) string {
	s := filepath.Join(dir, Script)
	if !isFile(s) {
		return ""
	}
	return s
}

// env is the environment of every command run for a lane (n 0 is no lane: the working tree itself).
// Nothing of git's, baton's or Claude Code's leaks in, so a gate sees the lane and nothing else; the PATH
// starts with Bin. A job's command also gets its own variables (Job.env).
func (p Pool) env(n int, dir, commit string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "BATON_") || strings.HasPrefix(k, "CLAUDE_") || k == "CLAUDECODE" {
			continue
		}
		env = append(env, kv)
	}
	if p.Bin != "" {
		env = firstOnPath(env, p.Bin)
	}
	env = append(env, "BATON_ROOT="+p.Root)
	if commit != "" {
		env = append(env, "BATON_COMMIT="+commit)
	}
	if n > 0 {
		env = append(env, "BATON_LANE="+strconv.Itoa(n), "BATON_LANE_DIR="+dir)
	}
	return env
}

// firstOnPath puts dir first on env's PATH (Windows spells the name as it likes), unless it is first
// already.
func firstOnPath(env []string, dir string) []string {
	sep := string(os.PathListSeparator)
	for i, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k != "PATH" && (runtime.GOOS != "windows" || !strings.EqualFold(k, "PATH")) {
			continue
		}
		switch {
		case v == dir || strings.HasPrefix(v, dir+sep):
		case v == "":
			env[i] = k + "=" + dir
		default:
			env[i] = k + "=" + dir + sep + v
		}
		return env
	}
	return append(env, "PATH="+dir)
}

// downTimeout bounds a lane's down, which runs when nothing else is left to stop it.
const downTimeout = 5 * time.Minute

// down runs lane n's down, if the lane is up and its commit has a lane script, and forgets that it was
// up. Its output goes to w.
func (p Pool) down(n int, w io.Writer) error {
	if p.upBy(n) == "" {
		return nil
	}
	defer os.Remove(p.upPath(n))
	dir := p.LaneDir(n)
	s := script(dir)
	if s == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), downTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s, "down", dir)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, p.env(n, dir, gitx.Head(dir)), w, w
	setGroup(cmd)
	cmd.Cancel = func() error { return signalGroup(cmd, false) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	killGroup(cmd)
	if err != nil {
		return fmt.Errorf("lane %d's down: %w", n, err)
	}
	return nil
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
