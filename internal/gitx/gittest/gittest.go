// Package gittest makes throwaway git repositories for tests, isolated from the developer's own git
// configuration (which may sign every commit, or run hooks) and gpg.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Isolate points git's global configuration, HOME and gpg at throwaway places and forgets any identity
// from the environment. It returns the throwaway HOME. Git commands in the test then see no identity
// and no signing, unless the test sets them up.
func Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GNUPGHOME", filepath.Join(home, "gnupg"))
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return home
}

// Git runs git in dir and returns its trimmed output; it fails the test on an error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Init makes dir a repository (call Isolate first).
func Init(t *testing.T, dir string) {
	t.Helper()
	Git(t, dir, "init", "-q")
}

// Commit commits paths (everything, if none are given) in dir with a throwaway identity, unsigned. The
// repository itself keeps no identity, so code under test sees a machine with none.
func Commit(t *testing.T, dir, msg string, paths ...string) {
	t.Helper()
	if len(paths) == 0 {
		paths = []string{"-A"}
	}
	Git(t, dir, append([]string{"add"}, paths...)...)
	Git(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)
}

// Write writes a file under dir, making its directories.
func Write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// NoGit hides git from the PATH, as on a machine without it.
func NoGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
}
