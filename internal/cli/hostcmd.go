package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/notify"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/version"
)

// defaultAutocompact caps context growth even if a boundary compaction never happens: auto-compaction
// at this size continues the task by itself. Override with BATON_AUTOCOMPACT (e.g. "600k", or "off").
const defaultAutocompact = "400k"

func init() {
	register("statusline", "print the status line segment (called by Claude Code's status line)", cmdStatusline)
}

// runHost starts claude inside baton's pseudo-terminal in the current directory.
func runHost(args []string, io IO) int {
	if io.Env("BATON_HOST") == "1" {
		return fail(io, "this is already a baton-hosted session; run claude's own commands here instead")
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(io, "cannot find the baton executable: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(io, "%v", err)
	}
	// An inherited BATON_DIR belongs to some other session; locate this directory's own state.
	dir := state.Locate(cwd, func(k string) string {
		if k == "BATON_DIR" {
			return ""
		}
		return io.Env(k)
	})
	instance := newInstanceID()
	st, err := state.Open(dir, instance, io.Now)
	if err != nil {
		return fail(io, "cannot create %s: %v", dir, err)
	}
	state.ExcludeFromGit(dir)
	logf := fileLogger(filepath.Join(dir, "baton.log"), instance, io.Now)

	claude := io.Env("BATON_CLAUDE")
	if claude == "" {
		claude = "claude"
	}
	autocompact := io.Env("BATON_AUTOCOMPACT")
	switch autocompact {
	case "":
		autocompact = defaultAutocompact
	case "off":
		autocompact = ""
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BATON_") {
			env = append(env, kv)
		}
	}
	home, _ := os.UserHomeDir()
	controller := &loop.Loop{
		Store: st, Notify: notify.New(notify.LoadConfig(home, io.Env)), Project: filepath.Base(filepath.Dir(dir)),
		Timing: loop.DefaultTiming, Logf: logf,
	}
	code, err := host.Run(host.Config{
		Claude: claude, Args: args, BatonBin: exe, Store: st, Instance: instance, Version: version.Version,
		Autocompact: autocompact, Stdin: os.Stdin, Stdout: os.Stdout, Env: env, Now: io.Now, Logf: logf,
		Controller: controller,
	})
	if err != nil {
		return fail(io, "%v", err)
	}
	return code
}

func cmdStatusline(_ []string, io IO) int {
	fmt.Fprint(io.Out, "◆ baton")
	return 0
}

func newInstanceID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fileLogger appends timestamped lines to path; logging must never break the session, so errors are dropped.
func fileLogger(path, instance string, now func() time.Time) func(string, ...any) {
	return func(format string, a ...any) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		fmt.Fprintf(f, "%s [%s] %s\n", now().Format("2006-01-02 15:04:05.000"), instance, fmt.Sprintf(format, a...))
		f.Close()
	}
}
