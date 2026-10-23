package hooks

import (
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// bgStart is background work of the model's own that a tool call started: its task's id (the id a Stop's
// background_tasks gives it), the tool, what names it to the model, and, for the record, the command and
// the description the call gave.
type bgStart struct {
	id, tool, what       string
	command, description string
	monitor              bool // a Monitor, which wakes the model with what it reports
}

// recordClip is how much of a command its record keeps (background_told, background_questioned): the
// command whole, its lines and a here-document's body too, up to this many characters, so that the record
// shows what it ran. In a real run, 200 characters on one line could not show it.
const recordClip = 4000

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
		b.id, b.monitor = str(resp, "taskId"), true
	case str(resp, "backgroundTaskId") != "":
		b.id = str(resp, "backgroundTaskId")
	case str(resp, "status") == "async_launched":
		if b.id = str(resp, "agentId"); b.id == "" {
			b.id = str(resp, "taskId")
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
// that started it, and the command (whole, up to recordClip characters) and description the call gave.
func (b bgStart) told(phase string) map[string]any {
	f := map[string]any{"phase": phase, "task": b.id, "tool": b.tool, "via": "start"}
	if b.command != "" {
		f["command"] = b.command
	}
	if b.description != "" {
		f["description"] = b.description
	}
	return f
}

// tellBackground tells the model, with the result of the tool call that started it, of background work of
// its own while its plan runs: what a stop on that work would be refused with (decide.BackgroundStarted),
// said before the stop, so the stop is allowed (decideStop's idle_on_background). The work is recorded as
// told at c.Now (state.Runtime.Tell); text is "" for a call that started none, or work told already.
func tellBackground(st *state.State, c Context, w decide.Words) (text string, b bgStart) {
	b, ok := backgroundStarted(c.Input)
	if !ok || st.Mode != state.ModeRunning || !st.Run.Tell(b.id, c.Now) {
		return "", bgStart{}
	}
	return w.BackgroundStarted(b.what, b.monitor), b
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
func recorded(command string) string {
	command = strings.TrimSpace(command)
	if r := []rune(command); len(r) > recordClip {
		command = string(r[:recordClip-1]) + "…"
	}
	return command
}

// clip is s on one line, at most n characters.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}
