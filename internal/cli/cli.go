// Package cli routes baton's command line.
//
//	baton [claude args…]        host an interactive claude session (the default)
//	baton run [-- claude args…] the same, explicitly
//	baton <subcommand> [args…]  everything else (see `baton help`)
package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/version"
)

// IO is the process environment a command runs against; tests substitute their own.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
	Env      func(string) string
	Now      func() time.Time
}

// StdIO is the real process environment.
func StdIO() IO {
	return IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Env: os.Getenv, Now: time.Now}
}

type command struct {
	summary string
	run     func(args []string, io IO) int
}

// commands is filled in by each subcommand's file via register, so this file stays a router.
var commands = map[string]command{}

func register(name, summary string, run func([]string, IO) int) {
	commands[name] = command{summary: summary, run: run}
}

func init() {
	register("run", "host an interactive claude session (pass claude args after --)", func(args []string, io IO) int {
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		return runHost(args, io)
	})
	register("hook", "handle a Claude Code hook event (called by the hooks baton installs)", runHook)
	register("version", "print baton's version", func(_ []string, io IO) int {
		fmt.Fprintln(io.Out, "baton", version.Version)
		return 0
	})
	register("help", "show this help", func(_ []string, io IO) int { usage(io.Out); return 0 })
}

// Main runs baton with args (without the program name) and returns the exit code.
func Main(args []string, io IO) int {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			usage(io.Out)
			return 0
		case "-v", "--version":
			return commands["version"].run(nil, io)
		}
		if c, ok := commands[args[0]]; ok {
			return c.run(args[1:], io)
		}
	}
	// No subcommand: everything is for claude (flags such as --resume, or an initial prompt).
	return runHost(args, io)
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "baton %s — run multi-phase Claude Code plans unattended\n\n", version.Version)
	fmt.Fprintln(w, "usage:\n  baton [claude args…]\n  baton <command> [args…]\n\ncommands:")
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-11s %s\n", n, commands[n].summary)
	}
}

// runHook is the entry point for every hook. It must never exit 2 by accident: argument problems
// and internal failures fail open (exit 0), exactly like hooks.Dispatch.
func runHook(args []string, io IO) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(io.Err, "baton: hook failed open after a panic: %v\n", r)
			code = 0
		}
	}()
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(io.Err, "baton: hook needs an event name; ignoring")
		return 0
	}
	var errs bytes.Buffer
	code = hooks.Dispatch(args[0], io.In, io.Out, multiWriter(io.Err, &errs), io.Env, io.Now, hookHandlers)
	if code == 0 && errs.Len() > 0 {
		recordHookFailure(args[0], errs.String(), io)
	}
	return code
}

// multiWriter is io.MultiWriter, reachable where an IO parameter named io shadows the package.
var multiWriter = io.MultiWriter

// recordHookFailure puts a hook that failed open on the record. Claude Code keeps hook stderr out of
// sight, and a hook that silently did nothing (a Stop that queued no compaction) is otherwise
// invisible; the event log is where a stall gets diagnosed.
func recordHookFailure(event, msg string, io IO) {
	dir := io.Env("BATON_DIR")
	if dir == "" || io.Env("BATON_HOST") != "1" {
		return
	}
	msg = strings.TrimSpace(strings.SplitN(msg, "\n", 2)[0])
	if s, err := state.Open(dir, io.Env("BATON_INSTANCE"), io.Now); err == nil {
		s.Event("hook_failed", map[string]any{"event": event, "error": msg})
	}
	fileLogger(filepath.Join(dir, "baton.log"), io.Env("BATON_INSTANCE"), io.Now)("hook %s failed open: %s", event, msg)
}

// hookHandlers maps event names to handlers. Hooks find the project through BATON_DIR, which the host
// sets for everything inside the session it hosts.
var hookHandlers = hooks.Handlers(hooks.Deps{Open: func(env func(string) string) (*state.Store, error) {
	dir := env("BATON_DIR")
	if dir == "" {
		return nil, errors.New("BATON_DIR is not set")
	}
	return state.Open(dir, env("BATON_INSTANCE"), time.Now)
}})
