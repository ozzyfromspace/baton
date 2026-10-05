package statusline

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

func TestSegment(t *testing.T) {
	pl := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0", Title: "The overlay"}, {ID: "P1", Title: "A very long phase title that keeps going"}}}
	now := time.Now()
	run := state.Attach(pl, now, "")
	run.Run.Context = &state.ContextUse{UsedPct: 41.4}
	cases := []struct {
		name string
		st   func() state.State
		plan bool
		want string
	}{
		{"no plan", func() state.State { return state.New() }, false, "◆ baton"},
		{"running", func() state.State { return run }, true, "◆ baton · P0 1/2 The overlay · ctx 41%"},
		{"long title", func() state.State { s := run; state.Done(&s, pl, "P0", now, false); state.Start(&s, now, ""); return s }, true,
			"◆ baton · P1 2/2 A very long phase title tha… · ctx 41%"},
		{"compacting", func() state.State { s := run; s.Run.Compaction.Status = state.CompactTyped; return s }, true, "◆ baton · compacting…"},
		{"escalated", func() state.State { s := run; s.Run.Escalation = &state.Escalation{}; return s }, true, "◆ baton · ⚠ waiting on you"},
		{"paused", func() state.State { s := run; s.Mode = state.ModePaused; return s }, true, "◆ baton · paused"},
		{"complete", func() state.State { s := run; s.Mode = state.ModeComplete; return s }, true, "◆ baton · ✓ plan complete"},
		{"waiting", func() state.State { s := run; state.SetWaiting(&s, "the build", time.Hour, now); return s }, true, "◆ baton · P0 1/2 The overlay · ctx 41% · waiting: the build"},
	}
	for _, c := range cases {
		if got := Segment(c.st(), pl, c.plan); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestUserCommandPrecedenceSkipsBaton(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write := func(path, cmd string) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(`{"statusLine":{"type":"command","command":`+quote(cmd)+`}}`), 0o644)
	}
	if UserCommand(proj, home) != "" {
		t.Fatal("found a command where none is configured")
	}
	write(filepath.Join(home, ".claude", "settings.json"), "user-line")
	write(filepath.Join(proj, ".claude", "settings.json"), "project-line")
	if got := UserCommand(proj, home); got != "project-line" {
		t.Fatalf("project should beat user: %q", got)
	}
	write(filepath.Join(proj, ".claude", "settings.local.json"), "'/opt/baton/bin/baton' statusline")
	if got := UserCommand(proj, home); got != "project-line" {
		t.Fatalf("baton's own command must be skipped: %q", got)
	}
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func TestRunUserAndCompose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	out := RunUser(`read x; echo "user: $x"; echo second`, []byte("hello\n"), t.TempDir(), 2*time.Second)
	if got := Compose("◆ baton", out); got != "◆ baton  user: hello\nsecond" {
		t.Fatalf("%q", got)
	}
	if got := RunUser("sleep 5", nil, t.TempDir(), 100*time.Millisecond); got != "" {
		t.Fatalf("slow command: %q", got)
	}
	if Compose("◆ baton", "") != "◆ baton" {
		t.Fatal("empty user line")
	}
}

func TestParse(t *testing.T) {
	in := Parse([]byte(`{"workspace":{"project_dir":"/p"},"context_window":{"used_percentage":12.5,"context_window_size":400000}}`))
	if in.Workspace.ProjectDir != "/p" || in.ContextWindow.UsedPercentage == nil || *in.ContextWindow.UsedPercentage != 12.5 || in.ContextWindow.ContextWindowSize != 400000 {
		t.Fatalf("%+v", in)
	}
	if Parse([]byte(`{"context_window":{"used_percentage":null}}`)).ContextWindow.UsedPercentage != nil {
		t.Fatal("null percentage")
	}
}
