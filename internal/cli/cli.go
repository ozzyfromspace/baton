// Package cli routes baton's command line.
//
//	baton [claude args…]        host an interactive claude session (the default)
//	baton <subcommand> [args…]  everything else (see `baton help`)
package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/version"
)

// IO is the process environment a command runs against; tests substitute their own.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
	Env      func(string) string
	Now      func() time.Time
	// Machine reads the machine (nil: machine.Read): with workers: auto, it decides how many workers run
	// at once, and no worker starts while it has no room for another session.
	Machine func() machine.Reading
	// Dir is the directory the command runs in ("": the process's working directory). In a phase agent, it
	// says which phase's the command is (inPhase).
	Dir string
}

// StdIO is the real process environment.
func StdIO() IO {
	return IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Env: os.Getenv, Now: time.Now, Machine: machine.Read}
}

// wd is the directory the command runs in (IO.Dir).
func (io IO) wd() (string, error) {
	if io.Dir != "" {
		return io.Dir, nil
	}
	return os.Getwd()
}

// reading reads the machine now (IO.Machine).
func (io IO) reading() machine.Reading {
	if io.Machine == nil {
		return machine.Read()
	}
	return io.Machine()
}

type command struct {
	summary string
	run     func(args []string, io IO) int
	hidden  bool // run by baton itself, never by hand: left out of help
}

// commands is filled in by each subcommand's file via register, so this file stays a router.
var commands = map[string]command{}

func register(name, summary string, run func([]string, IO) int) {
	commands[name] = command{summary: summary, run: run}
}

// leadOnly is a command for the lead session of a run alone. A worker's session, which works on one phase
// of the run in a worktree of its own, is refused it, and told to carry on with its phase.
func leadOnly(name string, run func([]string, IO) int) func([]string, IO) int {
	return func(args []string, io IO) int {
		if !worker(io) {
			return run(args, io)
		}
		phase := io.Env("BATON_PHASE")
		if s, err := store(io); err == nil && s.Worker() != "" {
			phase = s.Worker()
			s.Event("refused", map[string]any{"command": name, "why": "worker"})
		}
		if phase == "" {
			phase = "its phase"
		}
		return fail(io, "%s", decide.WorkerRefused(name, phase))
	}
}

// registerHidden adds a command baton runs itself, which help leaves out.
func registerHidden(name string, run func([]string, IO) int) {
	commands[name] = command{run: run, hidden: true}
}

func init() {
	register("hook", "handle a Claude Code hook event (called by the hooks baton installs)", runHook)
	register("version", "print baton's version, the session's and the installed one (also -v, --version)", cmdVersion)
	register("help", "show this help (also -h, --help)", func(_ []string, io IO) int { usage(io.Out); return 0 })
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
	fmt.Fprintf(w, "baton %s — run multi-phase Claude Code plans unattended\n", version.Version)
	fmt.Fprintf(w, "by %s · %s\n\n", version.Author, version.Homepage)
	fmt.Fprintln(w, "usage:\n  baton [claude args…]\n  baton <command> [args…]")
	fmt.Fprintln(w, "\nflags:\n  -h, --help     show this help\n  -v, --version  print baton's version and who made it")
	fmt.Fprintln(w, "  (these two are baton's own; for claude's, run claude --help or claude --version)\n\ncommands:")
	names := make([]string, 0, len(commands))
	width := 0
	for n, c := range commands {
		if c.hidden {
			continue
		}
		names = append(names, n)
		width = max(width, len(n))
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-*s  %s\n", width, n, commands[n].summary)
	}
	fmt.Fprintf(w, "\n%s\n", version.Credit())
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
	if p, err := state.OpenProject(dir, io.Now); err == nil {
		if s, ok := p.Lookup(io.Env("CLAUDE_CODE_SESSION_ID"), io.Env("BATON_INSTANCE")); ok {
			s.Event("hook_failed", map[string]any{"event": event, "error": msg})
		}
	}
	fileLogger(filepath.Join(dir, "baton.log"), io.Env("BATON_INSTANCE"), io.Now)("hook %s failed open: %s", event, msg)
}

// hookHandlers maps event names to handlers. Hooks find the project through BATON_DIR, which the host
// sets for everything inside the session it hosts, and their run through the session id.
var hookHandlers = hooks.Handlers(hooks.Deps{Open: hooks.OpenRun(time.Now)})
