//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/skill"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/upgrade"
)

// installBaton makes version v the installed baton under a throwaway baton home, as the launcher would.
func (f *fixture) installBaton(v string) {
	f.t.Helper()
	root := f.vars["BATON_HOME"]
	p := upgrade.Bin(root, v)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("#!/bin/sh\necho baton "+v+"\n"), 0o755)
	link := filepath.Join(root, "bin", "baton")
	os.Remove(link)
	if err := os.Symlink(filepath.Join(v, "baton"), link); err != nil {
		f.t.Fatal(err)
	}
}

func upgradeFixture(t *testing.T, withPlan bool) *fixture {
	f := newFixture(t, withPlan)
	f.vars = map[string]string{"BATON_HOME": t.TempDir(), "BATON_VERSION": "v0.3.1"}
	return f
}

func (f *fixture) stopMessage() string {
	f.t.Helper()
	out := f.fire("Stop", map[string]any{"stop_hook_active": false})
	msg, _ := out["systemMessage"].(string)
	return msg
}

// The human hears once per version that a newer baton is installed, and what becomes of the session.
func TestANewerBatonIsAnnouncedOnce(t *testing.T) {
	f := upgradeFixture(t, false)
	if msg := f.stopMessage(); strings.Contains(msg, "is installed") {
		t.Fatalf("nothing newer is installed: %q", msg)
	}
	f.installBaton("v0.3.2")
	if msg := f.stopMessage(); !strings.Contains(msg, "baton v0.3.2 is installed. baton restarts this session on it once the session is idle") {
		t.Fatalf("first stop: %q", msg)
	}
	if msg := f.stopMessage(); strings.Contains(msg, "is installed") {
		t.Fatalf("said twice: %q", msg)
	}
	if !strings.Contains(f.eventLog(), `"kind":"newer_baton"`) {
		t.Fatalf("events: %s", f.eventLog())
	}
	f.installBaton("v0.3.3")
	if msg := f.stopMessage(); !strings.Contains(msg, "baton v0.3.3 is installed") {
		t.Fatalf("a newer one again: %q", msg)
	}
}

func TestNewerBatonWording(t *testing.T) {
	for _, c := range []struct {
		name, installed string
		plan            bool
		vars            map[string]string
		want            string
	}{
		{"running plan", "v0.3.2", true, nil, "restarts this session on it at the next phase boundary"},
		{"major", "v0.4.0", false, nil, "this session stays on v0.3.1 until you restart it (exit, then run `baton --continue`). It is a major update: read what changed first"},
		{"auto_restart off", "v0.3.2", false, map[string]string{"BATON_AUTO_RESTART": "0"}, "this session stays on v0.3.1 until you restart it"},
		{"older", "v0.3.0", false, nil, ""},
		{"a build from a checkout", "v0.3.2", false, map[string]string{"BATON_VERSION": "v0.3.1-4-gabcdef0"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := upgradeFixture(t, c.plan)
			for k, v := range c.vars {
				f.vars[k] = v
			}
			if c.plan {
				f.store.Update(func(st *state.State) error {
					st.Phases["P0"].Status, st.Run.TurnOpen = state.PhaseDone, true
					return nil
				})
			}
			f.installBaton(c.installed)
			msg := f.stopMessage()
			if c.want == "" && strings.Contains(msg, "is installed") || c.want != "" && !strings.Contains(msg, c.want) {
				t.Fatalf("got %q, want %q", msg, c.want)
			}
		})
	}
}

// The restarted session says it was restarted, once; the session that ended for it raises no alarm.
func TestARestartedSessionSaysSo(t *testing.T) {
	f := upgradeFixture(t, true)
	f.store.Update(func(st *state.State) error {
		st.Run.Restart = &state.Restart{From: "v0.3.1", To: "v0.3.2", At: f.now}
		return nil
	})
	f.fire("SessionEnd", map[string]any{"reason": "other"})
	if st := f.state(); len(st.Run.Notices) != 0 {
		t.Fatalf("a restart is not a session ending mid-plan: %+v", st.Run.Notices)
	}
	f.vars["BATON_VERSION"] = "v0.3.2"
	out := f.fire("SessionStart", map[string]any{"source": "resume"})
	if msg, _ := out["systemMessage"].(string); !strings.HasPrefix(msg, "baton: restarted this session on baton v0.3.2 (was v0.3.1) · baton: hosting") {
		t.Fatalf("SessionStart: %q", msg)
	}
	if st := f.state(); st.Run.Restart != nil {
		t.Fatalf("restart record kept: %+v", st.Run.Restart)
	}
	if !strings.Contains(f.eventLog(), `"kind":"restarted"`) {
		t.Fatalf("events: %s", f.eventLog())
	}
	out = f.fire("SessionStart", map[string]any{"source": "resume"})
	if msg, _ := out["systemMessage"].(string); strings.Contains(msg, "restarted") {
		t.Fatalf("said twice: %q", msg)
	}
}

func TestARestartThatFellBackSaysSo(t *testing.T) {
	f := upgradeFixture(t, false)
	f.store.Update(func(st *state.State) error {
		st.Run.Restart = &state.Restart{From: "v0.3.1", To: "v0.3.2", At: f.now}
		return nil
	})
	out := f.fire("SessionStart", map[string]any{"source": "resume"})
	if msg, _ := out["systemMessage"].(string); !strings.HasPrefix(msg, "baton: could not restart this session on baton v0.3.2; it stays on v0.3.1") {
		t.Fatalf("SessionStart: %q", msg)
	}
}

// transcript writes a transcript holding a /baton load (or not) and returns its path.
func (f *fixture) transcript(loaded bool) string {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "s.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"hello"}}` + "\n"
	if loaded {
		body += `{"type":"user","isMeta":true,"message":{"role":"user","content":"Base directory for this skill: /c/baton/0.3.1/skills/baton\n\n# /baton"}}` + "\n"
	}
	os.WriteFile(path, []byte(body), 0o644)
	return path
}

func (f *fixture) restartedFrom(from, to string, atBoundary bool) {
	f.t.Helper()
	f.store.Update(func(st *state.State) error {
		st.Run.Restart = &state.Restart{From: from, To: to, At: f.now}
		if atBoundary {
			st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactQueued}
		}
		return nil
	})
	f.vars["BATON_VERSION"] = to
	f.fire("SessionStart", map[string]any{"source": "resume"})
}

// After a session restarts on a newer baton while idle, the first prompt hands the model the current
// /baton instructions, once, if the conversation still holds an earlier copy.
func TestAnIdleRestartHandsTheModelTheCurrentInstructions(t *testing.T) {
	f := upgradeFixture(t, false)
	f.restartedFrom("v0.3.1", "v0.3.2", false)
	if st := f.state(); st.Run.StaleSkill != "v0.3.1" {
		t.Fatalf("StaleSkill = %q", st.Run.StaleSkill)
	}
	tr := f.transcript(true)
	out := f.fire("UserPromptSubmit", map[string]any{"prompt": "what next?", "transcript_path": tr})
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	ctx, _ := hso["additionalContext"].(string)
	if !strings.Contains(ctx, "baton was updated from v0.3.1 to v0.3.2 since /baton's instructions were loaded in this conversation") ||
		!strings.Contains(ctx, skill.Text[:80]) {
		t.Fatalf("additionalContext:\n%.400s", ctx)
	}
	if msg, _ := out["systemMessage"].(string); !strings.Contains(msg, "gave Claude the current /baton instructions") {
		t.Fatalf("systemMessage: %q", msg)
	}
	if !strings.Contains(f.eventLog(), `"kind":"skill_refreshed"`) || f.state().Run.StaleSkill != "" {
		t.Fatalf("events: %s", f.eventLog())
	}
	if out := f.fire("UserPromptSubmit", map[string]any{"prompt": "and then?", "transcript_path": tr}); out["hookSpecificOutput"] != nil {
		t.Fatalf("handed over twice: %v", out)
	}
}

func TestNoRefreshWhenNothingIsStale(t *testing.T) {
	for name, c := range map[string]struct {
		atBoundary, loaded bool
		prompt             string
	}{
		"restarted at a phase boundary": {true, true, "go on"},
		"never ran /baton":              {false, false, "go on"},
		"the prompt is a /baton":        {false, true, "/baton status"},
	} {
		t.Run(name, func(t *testing.T) {
			f := upgradeFixture(t, false)
			f.restartedFrom("v0.3.1", "v0.3.2", c.atBoundary)
			out := f.fire("UserPromptSubmit", map[string]any{"prompt": c.prompt, "transcript_path": f.transcript(c.loaded)})
			if out["hookSpecificOutput"] != nil || strings.Contains(f.eventLog(), "skill_refreshed") || f.state().Run.StaleSkill != "" {
				t.Fatalf("out %v, stale %q", out, f.state().Run.StaleSkill)
			}
		})
	}
}
