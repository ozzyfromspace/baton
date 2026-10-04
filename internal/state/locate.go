package state

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Locate returns the .baton directory for a process running in cwd. BATON_DIR (set by the host for
// everything inside a hosted session) wins; otherwise the nearest existing .baton above cwd; otherwise
// a new one at the repository root (the directory holding .git), or in cwd outside a repository.
func Locate(cwd string, env func(string) string) string {
	if d := env("BATON_DIR"); d != "" {
		return d
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if fi, err := os.Stat(filepath.Join(dir, ".baton")); err == nil && fi.IsDir() {
			return filepath.Join(dir, ".baton")
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if root := gitRoot(cwd); root != "" {
		return filepath.Join(root, ".baton")
	}
	return filepath.Join(cwd, ".baton")
}

// gitRoot is the nearest directory at or above dir that contains .git (a directory or a worktree file).
func gitRoot(dir string) string {
	for ; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

// ExcludeFromGit adds ".baton/" to the repository's info/exclude so baton's state never shows up in
// `git status` and the user's .gitignore is never touched. Outside a repository it does nothing.
func ExcludeFromGit(batonDir string) error {
	root := filepath.Dir(batonDir)
	gitPath := filepath.Join(root, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return nil
	}
	gitDir := gitPath
	if !fi.IsDir() { // worktree or submodule: ".git" is a file "gitdir: <path>"
		b, err := os.ReadFile(gitPath)
		if err != nil {
			return nil
		}
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		if !filepath.IsAbs(line) {
			line = filepath.Join(root, line)
		}
		gitDir = line
		if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			common := strings.TrimSpace(string(c))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			gitDir = common
		}
	}
	exclude := filepath.Join(gitDir, "info", "exclude")
	if f, err := os.Open(exclude); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t == ".baton/" || t == ".baton" || t == "/.baton/" {
				f.Close()
				return nil
			}
		}
		f.Close()
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(exclude, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n# baton's per-project state (added by baton)\n.baton/\n")
	return err
}
