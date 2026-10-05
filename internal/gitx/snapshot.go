package gitx

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A snapshot saves uncommitted work into a private ref without touching anything the human or the model
// works with: the working tree, the index and HEAD stay byte for byte as they were, nothing is signed and
// no hook runs (docs/research/escalation.md, S4). It takes a copy of the index, adds every change to the
// copy, and writes that as a commit no branch points to:
//
//	temp index ← index;  add -u; add <untracked>;  write-tree;  commit-tree --no-gpg-sign [-p HEAD]
//
// The commit is made by "baton <baton@localhost>", so a machine with no git identity still gets one.

// SnapshotRefs is where snapshots live: refs/baton/snapshots/<phase>-<unix time>.
const SnapshotRefs = "refs/baton/snapshots/"

// Snapshot limits. Untracked files are saved smallest first, up to MaxUntracked bytes in all; the rest
// are skipped and reported. Only the newest KeepSnapshots refs are kept.
var (
	MaxUntracked  int64 = 50 << 20
	KeepSnapshots       = 20
)

const snapshotTimeout = time.Minute

// Saved is one snapshot.
type Saved struct {
	Ref     string    `json:"ref"`
	Commit  string    `json:"commit"`
	At      time.Time `json:"at"`
	Subject string    `json:"subject"`
	Files   int       `json:"files"`             // paths that differ from HEAD
	Skipped []string  `json:"skipped,omitempty"` // untracked files too big to save this time
	// Unchanged: the newest snapshot already held exactly this work, so no new one was made.
	Unchanged bool `json:"unchanged,omitempty"`

	tree, parent string
}

// RestoreCommand is how a human puts a snapshot's work back into the working tree. It overwrites the
// files the snapshot holds, removes tracked files the snapshot had deleted, and leaves untracked files
// and the index alone. (Tested in snapshot_test.go: change the command only together with that test.)
func RestoreCommand(ref string) string {
	return "git restore --source=" + ref + " --worktree -- ."
}

// Snapshot saves the uncommitted work in root under a new ref named for phase, made at the time at.
// why goes into the commit message. A clean tree saves nothing: the result has no Ref. It never fails
// for want of a git identity, a gpg key or a first commit.
func Snapshot(root string, at time.Time, phase, why string) (Saved, error) {
	if !Usable(root) {
		return Saved{}, ErrNoGit
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	git := func(env []string, stdin string, args ...string) (string, error) {
		var in io.Reader
		if stdin != "" {
			in = strings.NewReader(stdin)
		}
		out, err := run(ctx, root, env, in, args...)
		return strings.TrimSpace(string(out)), err
	}

	index, err := git(nil, "", "rev-parse", "--git-path", "index")
	if err != nil {
		return Saved{}, err
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(root, index)
	}
	tmp, err := os.MkdirTemp("", "baton-snapshot-")
	if err != nil {
		return Saved{}, err
	}
	defer os.RemoveAll(tmp)
	tmpIndex := filepath.Join(tmp, "index")
	if b, err := os.ReadFile(index); err == nil {
		if err := os.WriteFile(tmpIndex, b, 0o600); err != nil {
			return Saved{}, err
		}
	}
	env := []string{"GIT_INDEX_FILE=" + tmpIndex}
	if _, err := git(env, "", "add", "--update"); err != nil {
		return Saved{}, err
	}
	untracked, err := git(nil, "", "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return Saved{}, err
	}
	include, skipped := pickUntracked(root, untracked)
	if len(include) > 0 {
		list := strings.Join(include, "\x00") + "\x00"
		if _, err := git(append(env, "GIT_LITERAL_PATHSPECS=1"), list, "add", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return Saved{}, err
		}
	}
	tree, err := git(env, "", "write-tree")
	if err != nil {
		return Saved{}, err
	}
	parent, _ := git(nil, "", "rev-parse", "--verify", "-q", "HEAD^{commit}")
	var changed string
	if parent != "" {
		changed, err = git(nil, "", "diff-tree", "-r", "-z", "--no-renames", "--name-only", parent+"^{tree}", tree)
	} else {
		changed, err = git(nil, "", "ls-tree", "-r", "-z", "--name-only", tree)
	}
	if err != nil {
		return Saved{}, err
	}
	files := 0
	for _, p := range strings.Split(changed, "\x00") {
		if p != "" {
			files++
		}
	}
	if files == 0 {
		return Saved{Skipped: skipped}, nil // nothing uncommitted (or nothing small enough to save)
	}

	existing, err := list(ctx, root)
	if err != nil {
		return Saved{}, err
	}
	if len(existing) > 0 && existing[0].tree == tree && existing[0].parent == parent {
		s := existing[0]
		s.Files, s.Skipped, s.Unchanged = files, skipped, true
		return s, nil
	}
	subject := fmt.Sprintf("baton snapshot: %s · %d %s", phase, files, plural(files, "file", "files"))
	msg := subject + "\n\nUncommitted work baton saved when the run halted"
	if why != "" {
		msg += ": " + why
	}
	msg += ".\n"
	date := fmt.Sprintf("%d +0000", at.Unix())
	identity := []string{
		"GIT_AUTHOR_NAME=baton", "GIT_AUTHOR_EMAIL=baton@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=baton", "GIT_COMMITTER_EMAIL=baton@localhost", "GIT_COMMITTER_DATE=" + date,
	}
	args := []string{"commit-tree", "--no-gpg-sign", "-F", "-"}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	commit, err := git(identity, msg, append(args, tree)...)
	if err != nil {
		return Saved{}, err
	}
	taken := map[string]bool{}
	for _, s := range existing {
		taken[s.Ref] = true
	}
	ref := SnapshotRefs + refPart(phase) + "-" + strconv.FormatInt(at.Unix(), 10)
	for n := 2; taken[ref]; n++ {
		ref = SnapshotRefs + refPart(phase) + "-" + strconv.FormatInt(at.Unix(), 10) + "-" + strconv.Itoa(n)
	}
	// The empty old value makes git refuse to move a ref that already exists.
	if _, err := git(nil, "", "update-ref", "-m", "baton snapshot", ref, commit, ""); err != nil {
		return Saved{}, err
	}
	saved := Saved{Ref: ref, Commit: commit, At: time.Unix(at.Unix(), 0), Subject: subject, Files: files, Skipped: skipped, tree: tree, parent: parent}
	prune(ctx, root, append([]Saved{saved}, existing...))
	return saved, nil
}

// Snapshots lists the snapshots in root, newest first (nil without git).
func Snapshots(root string) []Saved {
	if !Usable(root) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dirtyTimeout)
	defer cancel()
	s, _ := list(ctx, root)
	return s
}

// list reads the snapshot refs, newest first; snapshots made in the same second go by name (a second
// one in that second is "-2").
func list(ctx context.Context, root string) ([]Saved, error) {
	out, err := run(ctx, root, nil, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(committerdate:unix)%00%(tree)%00%(parent)%00%(subject)", SnapshotRefs)
	if err != nil {
		return nil, err
	}
	var all []Saved
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 6 {
			continue
		}
		unix, _ := strconv.ParseInt(f[2], 10, 64)
		all = append(all, Saved{Ref: f[0], Commit: f[1], At: time.Unix(unix, 0), tree: f[3], parent: f[4], Subject: f[5]})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].At.Equal(all[j].At) {
			return all[i].At.After(all[j].At)
		}
		return all[i].Ref > all[j].Ref
	})
	return all, nil
}

// prune deletes all but the newest KeepSnapshots snapshots (newest first in all).
func prune(ctx context.Context, root string, all []Saved) {
	if len(all) <= KeepSnapshots {
		return
	}
	var cmds strings.Builder
	for _, s := range all[KeepSnapshots:] {
		fmt.Fprintf(&cmds, "delete %s %s\n", s.Ref, s.Commit)
	}
	run(ctx, root, nil, strings.NewReader(cmds.String()), "update-ref", "--stdin")
}

// pickUntracked chooses the untracked files to save, smallest first, within MaxUntracked bytes in all.
// A directory in the list is a nested repository, whose work its own git holds; it is left out.
func pickUntracked(root, out string) (include, skipped []string) {
	type file struct {
		path string
		size int64
	}
	var files []file
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || strings.HasSuffix(p, "/") {
			continue
		}
		var size int64
		if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			size = fi.Size()
		}
		files = append(files, file{p, size})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].size != files[j].size {
			return files[i].size < files[j].size
		}
		return files[i].path < files[j].path
	})
	var total int64
	for _, f := range files {
		if total+f.size > MaxUntracked {
			skipped = append(skipped, f.path)
			continue
		}
		total += f.size
		include = append(include, f.path)
	}
	sort.Strings(skipped)
	return include, skipped
}

// refPart makes a phase id safe to use in a ref name.
func refPart(phase string) string {
	s := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '-'
	}, phase)
	if s = strings.Trim(s, "-"); s == "" {
		return "run"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
