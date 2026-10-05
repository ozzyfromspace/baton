// Package gitx is everything baton does with git. git is optional: a plan runs the same in a plain
// folder, or on a machine without git. Usable decides which, and every function here checks it first and
// degrades to a defined "not a repository" result, so no caller can forget to.
package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNoGit is the "not a repository" result: the project has no .git, or git is not installed.
var ErrNoGit = errors.New("not a git repository, or git is not installed")

// Usable reports whether baton uses git for the project at root: root holds a .git (a directory, or the
// file a worktree or submodule has) and git is on the PATH. This is the one test for everything
// git-related in baton. It is checked on every use, so a repository created mid-run is picked up.
func Usable(root string) bool {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return false
	}
	_, err := exec.LookPath("git")
	return err == nil
}

// Head returns the commit at HEAD, or "" (no git, or no commit yet).
func Head(root string) string {
	if !Usable(root) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := run(ctx, root, nil, nil, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// MaxDirty is how many uncommitted paths baton keeps track of. Past it the gate on uncommitted work
// only warns: a tree that dirty is not one baton can reason about path by path.
const MaxDirty = 2000

// dirtyTimeout bounds git status, which the model's commands wait on.
const dirtyTimeout = 10 * time.Second

// Dirty lists every path with uncommitted work, relative to root: staged and unstaged changes, deletions,
// both sides of a rename, and each untracked file (ignored files are left out). It is sorted. It never
// takes git's optional locks, so it cannot get in the way of the model's own git commands.
func Dirty(root string) ([]string, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	ctx, cancel := context.WithTimeout(context.Background(), dirtyTimeout)
	defer cancel()
	out, err := run(ctx, root, nil, nil, "--no-optional-locks", "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

// parseStatus reads `git status --porcelain=v1 -z`: "XY path\0", where a rename is followed by its
// original path, "R  new\0old\0".
func parseStatus(out []byte) []string {
	seen := map[string]bool{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		seen[f[3:]] = true
		if f[0] == 'R' || f[0] == 'C' || f[1] == 'R' || f[1] == 'C' {
			i++
			if f[0] == 'R' || f[1] == 'R' {
				if i < len(fields) && fields[i] != "" {
					seen[fields[i]] = true // renamed away: gone from where it was
				}
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// maxHashed is the largest file Fingerprint reads. A bigger one is told apart by size and modification
// time, so recording a phase's starting point never stalls on a huge build artifact.
const maxHashed = 8 << 20

// Fingerprint identifies the current content of each path (relative to root), so the same path can be
// compared at two moments: a phase's start and its end. A missing path is "gone". It reads the files
// itself, so it needs no git.
func Fingerprint(root string, paths []string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		out[p] = fingerprint(filepath.Join(root, filepath.FromSlash(p)))
	}
	return out
}

func fingerprint(path string) string {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "gone"
	case err != nil:
		return "unreadable"
	case fi.Mode()&fs.ModeSymlink != 0:
		target, _ := os.Readlink(path)
		return "link:" + digest(strings.NewReader(target))
	case fi.IsDir():
		return "dir" // a submodule or nested repository: its own git tracks its content
	case fi.Size() > maxHashed:
		return fmt.Sprintf("size:%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("size:%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	defer f.Close()
	return digest(f)
}

func digest(r io.Reader) string {
	h := sha256.New()
	io.Copy(h, r)
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// Since splits the uncommitted paths now into the work that appeared or changed since start (fresh) and
// the work that was already there, unchanged (kept). Both are sorted.
func Since(start, now map[string]string) (fresh, kept []string) {
	for p, fp := range now {
		if old, ok := start[p]; ok && old == fp {
			kept = append(kept, p)
		} else {
			fresh = append(fresh, p)
		}
	}
	sort.Strings(fresh)
	sort.Strings(kept)
	return fresh, kept
}

// run runs git in root with a deadline and returns its standard output. The environment drops anything
// that would point git at another repository or index, and git never prompts.
func run(ctx context.Context, root string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(baseEnv(), env...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		name := args[0]
		if strings.HasPrefix(name, "-") && len(args) > 1 {
			name = args[1]
		}
		return out, fmt.Errorf("git %s: %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func baseEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR", "GIT_PREFIX":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
}
