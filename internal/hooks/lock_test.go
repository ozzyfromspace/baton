package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/transcript"
)

// lockFree reports whether nobody holds the lock of s's run right now: a hook reading git, the machine or a
// transcript must find it so.
func lockFree(s *state.Store) bool {
	l := flock.New(filepath.Join(s.Dir, "lock"))
	ok, _ := l.TryLock()
	if ok {
		l.Unlock()
	}
	return ok
}

// fireWith is fire, with deps of the test's own.
func (f *fixture) fireWith(d Deps, event string, input map[string]any) map[string]any {
	f.t.Helper()
	input["hook_event_name"] = event
	if _, ok := input["session_id"]; !ok {
		input["session_id"] = f.sid
	}
	if _, ok := input["transcript_path"]; !ok {
		input["transcript_path"] = f.leadTranscript()
	}
	raw, _ := json.Marshal(input)
	var out, errb bytes.Buffer
	if code := Dispatch(event, bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(d)); code != 0 || strings.Contains(errb.String(), "failed open") {
		f.t.Fatalf("%s exited %d: %s", event, code, errb.String())
	}
	var res map[string]any
	if out.Len() > 0 {
		json.Unmarshal(out.Bytes(), &res)
	}
	return res
}

// A rewind is looked for in the transcript before the run's lock is taken, and acted on under it.
func TestARewindIsReadBeforeTheLock(t *testing.T) {
	f := newFixture(t, true)
	f.now = rewindAt("12:03:00")
	f.set(func(st *state.State) { state.AddNote(st, "wrote a.txt unsigned", "rm a.txt", rewindAt("12:01:04.500")) })
	ss := f.session("rewound.jsonl", "p3")
	f.fire("UserPromptSubmit", ss.in("prompt", "Run echo four."))
	ss.write("")
	fork := readFork
	defer func() { readFork = fork }()
	var reads, locked int
	readFork = func(path string, m transcript.Mark) (transcript.Fork, error) {
		if reads++; !lockFree(f.store) {
			locked++
		}
		return fork(path, m)
	}
	f.fire("PreToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{"file_path": "x"}))
	if reads == 0 || locked != 0 {
		t.Fatalf("read the transcript %d times, %d under the lock", reads, locked)
	}
	if st := f.state(); st.Run.Mark != nil || len(st.Inbox) != 1 || st.Inbox[0].Kind != state.MessageRewind {
		t.Fatalf("mark %+v, inbox %+v", st.Run.Mark, st.Inbox)
	}
}

// What a hook read of the transcript before the lock stands only for the mark it read at: a mark set since
// is left for a later hook of the turn, and one taken in another conversation goes.
func TestARewindLookStandsForItsMark(t *testing.T) {
	mark := state.Mark{Path: "t.jsonl", Offset: 10, Leaf: "s4"}
	notYet := &rewindLook{mark: mark, fork: transcript.Fork{Prompt: false}}
	for _, c := range []struct {
		name     string
		event    string
		path     string // the hook's transcript
		look     *rewindLook
		wantMark bool
	}{
		{"no look", "PostToolUse", "t.jsonl", nil, true},
		{"a look at another mark", "PostToolUse", "t.jsonl", &rewindLook{mark: state.Mark{Path: "t.jsonl", Offset: 3, Leaf: "s2"}}, true},
		{"the prompt not written yet", "PostToolUse", "t.jsonl", notYet, true},
		{"the turn ended before it was", "Stop", "t.jsonl", notYet, false},
		{"another conversation", "PostToolUse", "other.jsonl", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := state.New()
			st.Mode = state.ModeRunning
			m := mark
			st.Run.Mark = &m
			ctx := Context{Event: c.event, Input: map[string]any{"transcript_path": c.path}, Env: func(string) string { return "" }}
			if ev := noticeRewind(ctx, &st, nil, c.look); ev != nil {
				t.Errorf("events %v", ev)
			}
			if (st.Run.Mark != nil) != c.wantMark {
				t.Errorf("mark %+v, want kept %v", st.Run.Mark, c.wantMark)
			}
		})
	}
	// lookRewind reads only for a hook that looks at its turn's prompt, in the conversation marked.
	peek := state.New()
	peek.Run.Mark = &mark
	for _, c := range []struct {
		event, path, agent string
		want               bool
	}{
		{"PostToolUse", "t.jsonl", "", true},
		{"Stop", "t.jsonl", "", true},
		{"UserPromptSubmit", "t.jsonl", "", false},
		{"PostToolUse", "other.jsonl", "", false},
		{"PostToolUse", "t.jsonl", "agent-1", false},
	} {
		in := map[string]any{"transcript_path": c.path}
		if c.agent != "" {
			in["agent_id"] = c.agent
		}
		if got := lookRewind(Context{Event: c.event, Input: in}, peek) != nil; got != c.want {
			t.Errorf("%s in %s (agent %q): looked %v", c.event, c.path, c.agent, got)
		}
	}
}

// An approval reads git and the machine before the run's lock is taken, and keeps the record of the plan
// it replaces once the lock is let go.
func TestAnApprovalReadsBeforeTheLockAndKeepsAfter(t *testing.T) {
	for _, c := range []struct {
		name  string
		clear bool // approved with "clear context": the first phase starts at once
	}{{"approved", false}, {"approved with clear context", true}} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true) // a plan attached already: the approved one replaces it
			f.set(func(st *state.State) { st.Mode = state.ModeComplete })
			f.vars = map[string]string{"HOME": t.TempDir(), "BATON_HOME": t.TempDir(), "BATON_WORKERS": "auto"}
			var reads, locked int
			d := Deps{Open: OpenRun(f.clock), Machine: func() machine.Reading {
				if reads++; !lockFree(f.store) {
					locked++
				}
				return roomy
			}}
			file := f.writePlan("next.md", approvedDoc)
			var out map[string]any
			if c.clear {
				f.fireWith(d, "PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "…", "planFilePath": file}})
				f.fireWith(d, "SessionEnd", map[string]any{"reason": "clear"})
				out = f.fireWith(d, "PostToolUse", map[string]any{"permission_mode": "acceptEdits", "tool_name": "Bash",
					"tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
			} else {
				out = f.fireWith(d, "PostToolUse", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{},
					"tool_response": map[string]any{"plan": "…", "filePath": file}, "permission_mode": "acceptEdits"})
			}
			if !strings.Contains(say(out), `attached "The next campaign"`) {
				t.Fatalf("said %q", say(out))
			}
			if reads == 0 || locked != 0 {
				t.Errorf("read the machine %d times, %d under the lock", reads, locked)
			}
			got := f.eventsOf("plan_archived")
			if len(got) != 1 {
				t.Fatalf("plan_archived: %v", got)
			}
			b, err := os.ReadFile(filepath.Join(got[0]["dir"].(string), "plan.json"))
			if err != nil || !strings.Contains(string(b), `"Demo"`) {
				t.Errorf("kept %s (%v)", b, err)
			}
		})
	}
}

// A record that cannot be written is logged as such, and the plan stays attached.
func TestARecordNotKeptIsLogged(t *testing.T) {
	f := newFixture(t, true)
	rec, err := f.store.Record(f.state())
	if err != nil || rec == nil {
		t.Fatalf("record %v %v", rec, err)
	}
	v := valveAction{event: "attached", also: []event{{"phase_started", nil}}, record: rec}
	v.keep()
	if len(v.also) != 2 || v.also[0].kind != "plan_archived" || v.also[1].kind != "phase_started" || v.record != nil {
		t.Fatalf("events %+v", v.also)
	}
	os.RemoveAll(f.store.Dir)
	v = valveAction{event: "attached", record: rec}
	v.keep()
	if len(v.also) != 1 || v.also[0].kind != "plan_not_archived" || v.also[0].fields["why"] == "" {
		t.Fatalf("events %+v", v.also)
	}
	(&valveAction{}).keep() // nothing replaced: nothing to keep
}

// Only an answer that starts an approved plan over attaches it, so only then is its ground read.
func TestMayStartOver(t *testing.T) {
	for answer, want := range map[string]bool{
		"Start the new plan from P0":     true,
		" Start the new plan from P0 ":   true,
		"Continue with the revised plan": false,
		"Wait for me":                    false,
		"":                               false,
	} {
		c := Context{Input: map[string]any{"tool_response": map[string]any{"answers": map[string]any{"q?": answer}}}}
		if got := mayStartOver(c); got != want {
			t.Errorf("%q: %v", answer, got)
		}
	}
}
