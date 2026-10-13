package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// A worker's work lands on the run's branch under the live lead session (spike 25,
// docs/research/workers.md). When the run's branch has moved on, it is merged into the worker's branch
// in the worker's own worktree (Merge), and the checkout then fast-forwards to that branch with `git
// merge --ff-only`, which is all or nothing: it updates the files the branch changed, keeps every
// unrelated local change, and applies nothing when a changed path has local changes of its own. Only an
// ignored file in the way is overwritten without a word, so Overlap looks for those itself. No commit is
// ever rewritten: a rebase drops a merge commit, and with it a conflict an agent resolved, without a word
// when the replay goes through. The run's branch never moves without its working tree (update-ref or
// branch -f leave the index behind, and the lead's next commit reverts the work), and nothing is stashed
// (--autostash leaves conflict markers in the lead's files).

// The ways landing can fail that the merge queue acts on.
var (
	// ErrNotFastForward: the branch is not ahead of the checkout's HEAD, which moved since the merge.
	ErrNotFastForward = errors.New("not a fast-forward")
	// ErrOverwrite: the fast-forward would overwrite local changes; nothing was applied.
	ErrOverwrite = errors.New("local changes would be overwritten")
	// ErrLocked: another git command holds the checkout's index; nothing was applied.
	ErrLocked = errors.New("the index is locked")
	// ErrConflict: the merge stopped on conflicts, and was aborted.
	ErrConflict = errors.New("the merge conflicts")
)

// MergeError is a merge that did not go through. It was aborted, or never started, so the branch, its
// index and its files are as they were. Paths are the paths that conflicted (none when git refused to
// start, or stopped for another reason), and Msg is what git said.
type MergeError struct {
	Paths []string
	Msg   string
}

func (e *MergeError) Error() string {
	if len(e.Paths) > 0 {
		return fmt.Sprintf("the merge conflicts in %s", strings.Join(e.Paths, ", "))
	}
	return "the merge failed: " + e.Msg
}

func (e *MergeError) Unwrap() error {
	if len(e.Paths) > 0 {
		return ErrConflict
	}
	return nil
}

// landEnv is the environment of the git commands whose failures are told apart by their words: in
// English, and never waiting on an editor.
var landEnv = []string{"LC_ALL=C", "LANGUAGE=C", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"}

// IsAncestor reports whether commit a is an ancestor of commit b (or b itself), in the repository at root.
func IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	if !Usable(root) {
		return false, ErrNoGit
	}
	_, stderr, code, err := runCode(ctx, root, nil, "merge-base", "--is-ancestor", a, b)
	switch {
	case code == 0:
		return true, nil
	case code == 1:
		return false, nil
	}
	return false, fmt.Errorf("git merge-base: %v: %s", err, stderr)
}

// Merge merges commit onto into the branch checked out in the worktree dir, as the commit message msg. It
// rewrites nothing: every commit of the branch stays as it is. It starts only with no uncommitted change
// to a tracked file, since aborting a merge cannot always give such a change back; an untracked file in
// its way makes git refuse before it changes anything. A merge that stops (on a conflict, or a merge
// commit that could not be signed) is aborted, and its MergeError says why. Local changes are never
// stashed.
func Merge(ctx context.Context, dir, onto, msg string) error {
	if !Usable(dir) {
		return ErrNoGit
	}
	out, err := run(ctx, dir, nil, nil, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if err != nil {
		return err
	}
	if dirty := parseStatus(out); len(dirty) > 0 {
		return &MergeError{Msg: "uncommitted changes to " + strings.Join(dirty, ", ")}
	}
	_, stderr, _, err := runCode(ctx, dir, landEnv, "-c", "merge.autoStash=false", "merge", "-q", "--no-edit", "-m", msg, onto)
	if err == nil {
		return nil
	}
	e := &MergeError{Msg: firstLines(stderr, 3)}
	if !merging(ctx, dir) {
		return e // git refused to start, and changed nothing
	}
	if out, _, _, err := runCode(ctx, dir, nil, "diff", "--name-only", "-z", "--diff-filter=U"); err == nil {
		e.Paths = nulList(out)
	}
	if _, stderr, _, err := runCode(ctx, dir, landEnv, "merge", "--abort"); err != nil {
		return fmt.Errorf("%v, and aborting it failed: %s", e, firstLines(stderr, 3))
	}
	return e
}

// merging reports a merge stopped part way in the worktree dir.
func merging(ctx context.Context, dir string) bool {
	_, _, code, _ := runCode(ctx, dir, nil, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	return code == 0
}

// Change is a path a branch changes.
type Change struct {
	Path  string // relative to the repository's root, slash-separated
	Added bool   // the path is new on the branch
}

// Changes lists the paths that differ between commits from and to, renames as a deletion and an
// addition.
func Changes(ctx context.Context, root, from, to string) ([]Change, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	out, err := run(ctx, root, nil, nil, "diff", "--no-renames", "--name-status", "-z", from, to)
	if err != nil {
		return nil, err
	}
	f := nulList(out)
	var changes []Change
	for i := 0; i+1 < len(f); i += 2 {
		changes = append(changes, Change{Path: f[i+1], Added: f[i] == "A"})
	}
	return changes, nil
}

// Commits lists the commits on to that from does not have (every commit of to, for from ""), oldest
// first.
func Commits(ctx context.Context, root, from, to string) ([]string, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := run(ctx, root, nil, nil, "rev-list", "--reverse", rng)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// Overlap lists the paths among changes that the working tree at root has local changes to: changed,
// staged or untracked (which a fast-forward refuses), or, for a path the branch adds, any file in its
// way, ignored ones included (which a fast-forward overwrites). It never takes git's optional locks, so
// it cannot get in the way of the session's own git commands.
func Overlap(root string, changes []Change) ([]string, error) {
	dirty, err := Dirty(root)
	if err != nil {
		return nil, err
	}
	local := make(map[string]bool, len(dirty))
	for _, p := range dirty {
		local[p] = true
	}
	var out []string
	for _, c := range changes {
		if local[c.Path] || c.Added && inTheWay(root, c.Path) {
			out = append(out, c.Path)
		}
	}
	return out, nil
}

// inTheWay reports a file where the branch adds p: at p itself, or a file where one of its directories
// would go.
func inTheWay(root, p string) bool {
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
		return true
	}
	for dir := path.Dir(p); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dir))); err == nil {
			return !fi.IsDir()
		}
	}
	return false
}

// FastForward moves the branch checked out at root to commit, with its working tree, if commit is ahead
// of it: `git merge --ff-only`. It applies everything or nothing. ErrNotFastForward, ErrOverwrite and
// ErrLocked say why it applied nothing.
func FastForward(ctx context.Context, root, commit string) error {
	if !Usable(root) {
		return ErrNoGit
	}
	_, stderr, _, err := runCode(ctx, root, landEnv, "merge", "--ff-only", "-q", commit)
	if err == nil {
		return nil
	}
	msg := firstLines(stderr, 3)
	switch {
	case strings.Contains(stderr, "Not possible to fast-forward"):
		return fmt.Errorf("%w: %s", ErrNotFastForward, msg)
	case strings.Contains(stderr, "would be overwritten"):
		return fmt.Errorf("%w: %s", ErrOverwrite, msg)
	case strings.Contains(stderr, "index.lock"):
		return fmt.Errorf("%w: %s", ErrLocked, msg)
	}
	return fmt.Errorf("git merge: %v: %s", err, msg)
}

// runCode runs git like run, and returns its standard error and exit code too (-1 if it did not exit).
func runCode(ctx context.Context, root string, env []string, args ...string) ([]byte, string, int, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(baseEnv(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := 0
	if err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}
	return out, stderr.String(), code, err
}

// nulList splits NUL-separated output.
func nulList(out []byte) []string {
	var list []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			list = append(list, f)
		}
	}
	return list
}

// firstLines is git's message without its hints, at most n lines.
func firstLines(s string, n int) string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "hint:") {
			continue
		}
		if out = append(out, l); len(out) == n {
			break
		}
	}
	return strings.Join(out, " ")
}
