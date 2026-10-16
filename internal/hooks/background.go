package hooks

import (
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// bgStart is background work of the model's own that a tool call started: its task's id (the id a Stop's
// background_tasks gives it), the tool, and what names it.
type bgStart struct {
	id, tool, what string
	monitor        bool // a Monitor, which wakes the model with what it reports
}

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

// tellBackground tells the model, with the result of the tool call that started it, of background work of
// its own while its plan runs: what a stop on that work would be refused with (decide.BackgroundStarted),
// said before the stop, so the stop is allowed (decideStop's idle_on_background). The work is recorded as
// told (state.Runtime.Tell); text is "" for a call that started none, or work told already.
func tellBackground(st *state.State, in map[string]any, w decide.Words) (text string, b bgStart) {
	b, ok := backgroundStarted(in)
	if !ok || st.Mode != state.ModeRunning || !st.Run.Tell(b.id) {
		return "", bgStart{}
	}
	return w.BackgroundStarted(b.what, b.monitor), b
}

// clipWhat is s on one line, at most 80 characters: a command can be long.
func clipWhat(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimSpace(string(r[:79])) + "…"
	}
	return s
}
