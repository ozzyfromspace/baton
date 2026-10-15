package hooks

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/hostcheck"
)

// A session that only inherited baton's environment (Claude Code moved the conversation to its background
// service, under a new session id) is dormant: its hooks bind no run, claim none and move no host's link,
// and baton tells the human once that it does not host it.
func TestInheritedSessionIsDormant(t *testing.T) {
	f := newFixture(t, true)
	before := f.state()
	links := func() map[string]string {
		out := map[string]string{}
		for _, sub := range []string{"sessions", "hosts"} {
			entries, _ := os.ReadDir(filepath.Join(f.dir, sub))
			for _, e := range entries {
				b, _ := os.ReadFile(filepath.Join(f.dir, sub, e.Name()))
				out[sub+"/"+e.Name()] = string(b)
			}
		}
		return out
	}
	was := links()
	fire := func(event string, input map[string]any) map[string]any {
		t.Helper()
		input["hook_event_name"], input["session_id"] = event, "bg-sess"
		raw, _ := json.Marshal(input)
		var out, errb bytes.Buffer
		h := Handlers(Deps{Open: OpenRun(f.clock)})
		if code := DispatchAs(hostcheck.Inherited, event, bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, h); code != 0 || errb.Len() > 0 {
			t.Fatalf("%s exited %d: %s", event, code, errb.String())
		}
		var res map[string]any
		if out.Len() > 0 {
			json.Unmarshal(out.Bytes(), &res)
		}
		return res
	}
	steps := []struct {
		event string
		input map[string]any
		told  bool
	}{
		{"SessionStart", map[string]any{"source": "resume"}, true},
		{"UserPromptSubmit", map[string]any{"prompt": "go on"}, false}, // told once
		{"SessionStart", map[string]any{"source": "compact"}, false},
		{"Stop", map[string]any{}, false},
		{"PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}, false},
		{"PreCompact", map[string]any{"trigger": "auto"}, false},
	}
	for _, s := range steps {
		out := fire(s.event, s.input)
		if s.told {
			if msg, _ := out["systemMessage"].(string); msg != decide.Inherited("bg-sess") || !strings.Contains(msg, "baton --resume bg-sess") {
				t.Errorf("%s: %v, want the notice once", s.event, out)
			}
		} else if out != nil {
			t.Errorf("%s: %v, want nothing", s.event, out)
		}
	}
	if now := links(); !maps.Equal(now, was) {
		t.Errorf("links moved:\n%v\nwas\n%v", now, was)
	}
	after := f.state()
	if after.Owner == nil || before.Owner == nil || *after.Owner != *before.Owner || after.Current != before.Current {
		t.Errorf("the run changed: owner %+v, was %+v", after.Owner, before.Owner)
	}
	if n := strings.Count(f.eventLog(), `"inherited_notice"`); n != 1 {
		t.Errorf("logged the notice %d times, want once:\n%s", n, f.eventLog())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "runs", "bg-sess")); err == nil {
		t.Error("made a run for the inherited session")
	}
}

// Where no mark can be kept (no project to keep it in), the notice comes at each SessionStart, and at no
// prompt.
func TestInheritedWithNoProject(t *testing.T) {
	env := func(k string) string { return map[string]string{"BATON_HOST": "1", "CLAUDE_CODE_SESSION_ID": "bg"}[k] }
	for event, told := range map[string]bool{"SessionStart": true, "UserPromptSubmit": false, "Stop": false} {
		var out, errb bytes.Buffer
		code := DispatchAs(hostcheck.Inherited, event, strings.NewReader(`{}`), &out, &errb, env, func() time.Time { return time.Unix(0, 0) }, nil)
		if code != 0 || strings.Contains(out.String(), "baton --resume bg") != told {
			t.Errorf("%s: code %d, said %q", event, code, out.String())
		}
	}
}

// panicky is hook input whose reading panics.
type panicky struct{}

func (panicky) Read([]byte) (int, error) { panic("boom") }

// The inherited path fails open too: a panic exits 0 and says nothing.
func TestInheritedPanicFailsOpen(t *testing.T) {
	for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
		var out, errb bytes.Buffer
		code := DispatchAs(hostcheck.Inherited, event, panicky{}, &out, &errb, hosted, now, nil)
		if code != 0 || out.Len() > 0 || !strings.Contains(errb.String(), "failed open") {
			t.Errorf("%s: code %d stdout %q stderr %q", event, code, out.String(), errb.String())
		}
	}
}
