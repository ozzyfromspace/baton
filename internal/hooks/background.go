package hooks

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// bgStart is background work of the model's own that a tool call started: its task's id (the id a Stop's
// background_tasks gives it), its kind (state.TaskShell, TaskMonitor, TaskSubagent or TaskWorkflow), the
// tool, what names it to the model, and, for the record, the command and the description the call gave.
type bgStart struct {
	id, kind, tool, what string
	command, description string
}

// recordClip is how much of a command its record keeps (background_told, background_questioned): the
// command whole, its lines and a here-document's body too, up to this many characters, so that the record
// shows what it ran. In a real run, 200 characters on one line could not show it.
const recordClip = 4000

// startClip is how much of a command the run keeps of each piece of background work while it runs
// (state.BackgroundStart): enough to name it, and to find its shell among claude's processes by it.
const startClip = 1000

// describeClip is how much of a background task's description its record keeps, on one line.
const describeClip = 200

// backgroundStarted is the background work a tool call's result says it started
// (docs/research/parallel.md): a Monitor's task (tool_response.taskId), a command run in the background or
// moved there, at its timeout or by the human (Bash's or PowerShell's backgroundTaskId), or an agent or a
// workflow launched in the background (status async_launched, with its agentId or its taskId). ok is false
// for a call that started none.
func backgroundStarted(in map[string]any) (b bgStart, ok bool) {
	resp, _ := in["tool_response"].(map[string]any)
	b.tool = str(in, "tool_name")
	switch {
	case b.tool == "Monitor":
		b.id, b.kind = str(resp, "taskId"), state.TaskMonitor
	case str(resp, "backgroundTaskId") != "":
		b.id, b.kind = str(resp, "backgroundTaskId"), state.TaskShell
	case str(resp, "status") == "async_launched":
		if b.id, b.kind = str(resp, "agentId"), state.TaskSubagent; b.id == "" {
			b.id, b.kind = str(resp, "taskId"), state.TaskWorkflow
		}
	}
	if b.id == "" {
		return bgStart{}, false
	}
	ti, _ := in["tool_input"].(map[string]any)
	b.command, b.description = recorded(str(ti, "command")), clip(str(ti, "description"), describeClip)
	for _, k := range []string{"description", "command"} {
		if b.what = clipWhat(str(ti, k)); b.what != "" {
			break
		}
	}
	if b.what == "" {
		b.what = b.tool
	}
	return b, true
}

// told is the record of background work told at its start (background_told): in phase, its task, the tool
// that started it, the command (whole, up to recordClip characters) and description the call gave, and the
// file its output goes to.
func (b bgStart) told(phase, output string) map[string]any {
	f := map[string]any{"phase": phase, "task": b.id, "tool": b.tool, "via": "start"}
	if b.command != "" {
		f["command"] = b.command
	}
	if b.description != "" {
		f["description"] = b.description
	}
	if output != "" {
		f["output"] = output
	}
	return f
}

// started is the record of background work seen to start that the model was not told of
// (background_started): a subagent of its model's started it (agent), or baton was not driving the plan.
func (b bgStart) started(s state.BackgroundStart) map[string]any {
	f := b.told(s.Phase, s.Output)
	delete(f, "via")
	if s.Agent != "" {
		f["agent"] = s.Agent
	}
	return f
}

// startBackground records, in st's loop, background work that the tool call c reports it started
// (backgroundStarted), the loop's own agent's or a subagent's of its model (c.Agent), whatever the run's
// mode: what it is, the phase it started in while baton drove the plan, the plan's generation, and the file
// its output goes to (state.Runtime.Start). It also notes where the session's tasks write their output
// (tasksDir). ok is false for a call that started none, or a start already recorded.
func startBackground(st *state.State, c Context) (b bgStart, rec state.BackgroundStart, ok bool) {
	if b, ok = backgroundStarted(c.Input); !ok {
		return bgStart{}, state.BackgroundStart{}, false
	}
	if dir := tasksDir(c); dir != "" {
		st.Run.TasksDir = dir
	}
	rec = state.BackgroundStart{Type: b.kind, Tool: b.tool, Command: clipStart(b.command), Description: b.description,
		Generation: st.Generation, Agent: c.Agent, At: c.Now}
	if st.Mode == state.ModeRunning {
		rec.Phase = st.Current
	}
	if st.Run.TasksDir != "" {
		rec.Output = filepath.Join(st.Run.TasksDir, b.id+".output")
	}
	return b, rec, st.Run.Start(b.id, rec)
}

// tellBackground tells the model, with the result of the tool call that started it, of background work b of
// its own while its plan runs: what a stop on that work would be refused with (decide.BackgroundStarted),
// said before the stop, so the stop is allowed (decideStop's idle_on_background). The work is recorded as
// told at c.Now (state.Runtime.Tell); text is "" for work told already, or while baton does not drive the
// plan. A shell whose description declares no end (declaresEnd) is told so (decide.NoEndDeclared): its
// reviewer, and the human reading the audit, judge it by what it declares.
func tellBackground(st *state.State, c Context, w decide.Words, b bgStart) (text string) {
	if st.Mode != state.ModeRunning || !st.Run.Tell(b.id, c.Now) {
		return ""
	}
	text = w.BackgroundStarted(b.what, b.kind)
	if b.kind == state.TaskShell && !declaresEnd(b.description) {
		text += "\n" + decide.NoEndDeclared(clipWhat(b.description))
	}
	return text
}

// declaresEnd reports whether a background shell's description declares when it ends: a run time
// (state.DeclaredEnd: `~15m`, `2 hours`) or a condition, an `until` (`until P12's browser checks pass`).
func declaresEnd(description string) bool {
	return state.DeclaredEnd(description) > 0 || untilWord.MatchString(description)
}

// untilWord is the word until, in any case, standing alone.
var untilWord = regexp.MustCompile(`(?i)\buntil\b`)

// tasksDir is where Claude Code writes the output of the session's background tasks, each to <id>.output
// (spike 45): the tasks folder beside the session's scratchpad, which every hook of Claude Code 2.1.318
// names (scratchpad_dir), a phase agent's and a subagent's too, since their tasks are the session's. Where
// a hook names none, it is the same folder under Claude Code's temporary root ($CLAUDE_CODE_TMPDIR, else
// /tmp), in claude-<uid>, under the project folder the transcript is kept in and the session's id. "" where
// neither is known.
func tasksDir(c Context) string {
	if sp := str(c.Input, "scratchpad_dir"); sp != "" {
		return filepath.Join(filepath.Dir(sp), "tasks")
	}
	tp, session := str(c.Input, "transcript_path"), str(c.Input, "session_id")
	if tp == "" || session == "" || runtime.GOOS == "windows" {
		return ""
	}
	root := c.Env("CLAUDE_CODE_TMPDIR")
	if root == "" {
		root = "/tmp"
	}
	return filepath.Join(root, "claude-"+strconv.Itoa(os.Getuid()), filepath.Base(filepath.Dir(tp)), session, "tasks")
}

// backgroundEnded is the record of the told background work a stop found ended in phase (recordStop): no
// hook reports a task's end (spike 19), so a task gone from a Stop's background_tasks ended by then. Each is
// a background_ended with how long it ran from when the model was told of it, in whole seconds (none when
// that was not recorded).
func backgroundEnded(phase string, ended []state.TaskEnded, now time.Time) []event {
	var out []event
	for _, e := range ended {
		f := map[string]any{"phase": phase, "task": e.ID}
		if !e.Told.IsZero() {
			f["seconds"] = int(max(now.Sub(e.Told), 0).Seconds())
		}
		out = append(out, event{kind: "background_ended", fields: f})
	}
	return out
}

// clipWhat is s on one line, at most 80 characters: a command can be long.
func clipWhat(s string) string { return clip(s, 80) }

// recorded is command as its record keeps it: as given, on as many lines, with the blanks around it
// trimmed, and at most recordClip characters.
func recorded(command string) string { return clipLines(command, recordClip) }

// clipStart is command as the run keeps it while its work runs (state.BackgroundStart): as given, on as
// many lines, at most startClip characters.
func clipStart(command string) string { return clipLines(command, startClip) }

// clipLines is s with the blanks around it trimmed, its lines kept, at most n characters.
func clipLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

// clip is s on one line, at most n characters.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}
