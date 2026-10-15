// Package hooks handles the Claude Code hook events that baton subscribes to in the sessions it hosts.
//
// For a command hook, the exit code is a control channel: 2 means "block" (for an asyncRewake hook,
// "wake the model"). A Go panic and a flag parse error both exit with status 2, so a bug in a hook would
// silently block the model. Dispatch therefore recovers every panic and fails open with exit 0; the only
// path that exits 2 is an explicit Result.Rewake. The host's watchdog is the backstop for a hook that
// failed open.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ozzyfromspace/baton/internal/hostcheck"
	"github.com/ozzyfromspace/baton/internal/machine"
)

// maxInput bounds how much hook input is read; hook payloads are small JSON objects.
const maxInput = 16 << 20

// Context is what a handler sees for one hook invocation.
type Context struct {
	Event string
	Input map[string]any
	Raw   []byte
	Env   func(string) string
	Now   time.Time
	// Machine reads the machine (Deps.Machine; nil: machine.Read).
	Machine func() machine.Reading
	// Agent is the subagent the hook comes from, within its loop (Handlers routes it): "" for the loop's
	// own agent (the lead's main agent, a worker session's, or a phase agent in its sub-run), otherwise
	// the id of a subagent that loop's model started itself.
	Agent string
}

// Result is what a handler asks Dispatch to send back to Claude Code.
type Result struct {
	// Output is marshalled to stdout as the hook's JSON response (decision, systemMessage, hookSpecificOutput).
	Output map[string]any
	// Rewake, when non-empty, is written to stderr and the process exits 2. Only meaningful for hooks
	// registered with "asyncRewake": true, where it wakes the idle model with this message.
	Rewake string
	// Stdout is written to stdout as it is, for a hook whose answer is not JSON: WorktreeCreate's is the
	// worktree's path.
	Stdout string
	// Fail, when non-empty, is written to stderr and the process exits 1: for WorktreeCreate, the worktree
	// is not made, and the agent that asked for it does not start. No other hook uses it.
	Fail string
}

// Handler handles one event.
type Handler func(Context) (Result, error)

// Dispatch runs the handler for event and returns the process exit code. It never returns 2 unless the
// handler deliberately asked for a rewake, and it is a no-op unless the session is hosted by baton, as
// BATON_HOST alone says (DispatchAs takes hostcheck's answer), but for the worktrees Claude Code asks for.
func Dispatch(event string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string, now func() time.Time, handlers map[string]Handler) int {
	return DispatchAs(hostcheck.Trust(env), event, stdin, stdout, stderr, env, now, handlers)
}

// DispatchAs is Dispatch in a session whose standing with baton is status (hostcheck.Check). A session
// that only inherited baton's environment (hostcheck.Inherited) is dormant: no handler runs, so nothing
// binds or claims a run or moves a host's link; baton only tells the human, once, that it does not host
// the session and how to give it back (inherited). Whatever the status, a worktree Claude Code asks for is
// made (dormantWorktree): baton's WorktreeCreate hook takes the place of its own making of one.
func DispatchAs(status hostcheck.Status, event string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string, now func() time.Time, handlers map[string]Handler) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "baton: hook %s failed open after a panic: %v\n", event, r)
			code = 0
		}
	}()
	h, ok := handlers[event]
	switch {
	case status != hostcheck.Hosted && event == "WorktreeCreate":
		h, ok = dormantWorktree, true
	case status == hostcheck.Plain:
		return 0
	case status == hostcheck.Inherited:
		inherited(event, stdin, stdout, stderr, env, now)
		return 0
	}
	if !ok {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxInput))
	if err != nil {
		fmt.Fprintf(stderr, "baton: hook %s could not read its input: %v\n", event, err)
		return 0
	}
	var input map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			fmt.Fprintf(stderr, "baton: hook %s got invalid JSON input: %v\n", event, err)
			return 0
		}
	}
	res, err := h(Context{Event: event, Input: input, Raw: raw, Env: env, Now: now()})
	if err != nil {
		fmt.Fprintf(stderr, "baton: hook %s failed open: %v\n", event, err)
		return 0
	}
	if res.Output != nil {
		out, err := json.Marshal(res.Output)
		if err != nil {
			fmt.Fprintf(stderr, "baton: hook %s produced unencodable output: %v\n", event, err)
			return 0
		}
		stdout.Write(out)
	}
	if res.Stdout != "" {
		fmt.Fprintln(stdout, res.Stdout)
	}
	if res.Fail != "" {
		fmt.Fprintln(stderr, "baton: "+res.Fail)
		return 1
	}
	if res.Rewake != "" {
		fmt.Fprintln(stderr, res.Rewake)
		return 2
	}
	return 0
}
