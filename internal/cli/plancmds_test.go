package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

type session struct {
	t   *testing.T
	env map[string]string
	now time.Time
}

func newSession(t *testing.T) (*session, string) {
	dir := t.TempDir()
	doc, err := os.ReadFile(filepath.Join("..", "plan", "testdata", "headings-emdash.md"))
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(dir, "plan.md")
	os.WriteFile(planFile, doc, 0o644)
	return &session{t: t, env: map[string]string{"BATON_DIR": filepath.Join(dir, ".baton"), "BATON_HOST": "1"},
		now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}, planFile
}

func (s *session) run(stdin string, args ...string) (int, string, string) {
	io, out, errb := testIO(stdin, s.env)
	io.Now = func() time.Time { return s.now }
	code := Main(args, io)
	return code, out.String(), errb.String()
}

func (s *session) must(stdin string, args ...string) string {
	s.t.Helper()
	code, out, errs := s.run(stdin, args...)
	if code != 0 {
		s.t.Fatalf("baton %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errs)
	}
	return out
}

func (s *session) mustFail(want string, args ...string) {
	s.t.Helper()
	code, _, errs := s.run("", args...)
	if code == 0 || !strings.Contains(errs, want) {
		s.t.Fatalf("baton %v: exit %d stderr %q; want failure mentioning %q", args, code, errs, want)
	}
}

func TestPlanLifecycleThroughTheCLI(t *testing.T) {
	s, planFile := newSession(t)

	spec := s.must("", "attach", planFile, "--suggest")
	if !strings.Contains(spec, `"anchor": "## P0 — The overlay"`) {
		t.Fatalf("suggestion: %s", spec)
	}
	s.mustFail("pass --suggest", "attach", planFile)
	if out := s.must(spec, "attach", planFile, "--spec", "-"); !strings.Contains(out, "3 phases") || !strings.Contains(out, "Current phase: P0") {
		t.Fatalf("attach: %s", out)
	}
	if code, _, errs := s.run(spec, "attach", planFile, "--spec", "-"); code == 0 || !strings.Contains(errs, "already running") {
		t.Fatalf("re-attach without --replace: exit %d %s", code, errs)
	}

	s.mustFail("not the current phase", "done", "P1")
	out := s.must("", "done", "P0", "--notes", "Overlay closes modals; see commit abc.")
	if !strings.Contains(out, "Next phase: P1 (The one-liners)") || !strings.Contains(out, "End your turn now") {
		t.Fatalf("done: %s", out)
	}
	handoff, _ := os.ReadFile(filepath.Join(s.env["BATON_DIR"], "handoff.md"))
	if !strings.Contains(string(handoff), "## P0") || !strings.Contains(string(handoff), "Overlay closes modals") {
		t.Fatalf("handoff: %s", handoff)
	}
	if st := s.must("", "status"); !strings.Contains(st, "✓ P0") || !strings.Contains(st, "→ P1") {
		t.Fatalf("status after done: %s", st)
	}

	s.must("", "waiting", "the", "build", "--until", "20m")
	s.must("", "blocked", "need", "a", "product", "decision")
	if st := s.must("", "status"); !strings.Contains(st, "blocked: need a product decision") || strings.Contains(st, "waiting:") {
		t.Fatalf("status when blocked: %s", st)
	}
	s.must("", "resume")
	s.must("", "checkpoint", "--notes", "half way")
	s.must("", "pause")
	s.mustFail("only apply while a plan is running", "checkpoint")
	s.must("", "resume")

	s.must("", "done", "P1")
	if out := s.must("", "done", "R1"); !strings.Contains(out, "the plan is complete") {
		t.Fatalf("final done: %s", out)
	}
	var status struct {
		State struct{ Mode string } `json:"state"`
	}
	json.Unmarshal([]byte(s.must("", "status", "--json")), &status)
	if status.State.Mode != "complete" {
		t.Fatalf("mode = %q", status.State.Mode)
	}

	events, _ := os.ReadFile(filepath.Join(s.env["BATON_DIR"], "events.jsonl"))
	for _, kind := range []string{"attached", "phase_done", "waiting", "blocked", "resumed", "checkpoint", "paused"} {
		if !strings.Contains(string(events), `"kind":"`+kind+`"`) {
			t.Errorf("no %s event in %s", kind, events)
		}
	}
}

func TestCommandsWithoutAPlan(t *testing.T) {
	s, _ := newSession(t)
	for _, args := range [][]string{{"done", "P0"}, {"blocked", "x"}, {"waiting", "x", "--until", "1m"}, {"checkpoint"}} {
		s.mustFail("no plan is attached", args...)
	}
	if out := s.must("", "status"); !strings.Contains(out, "no plan attached") {
		t.Fatalf("status: %s", out)
	}
}

func TestAttachRejectsASpecThatDoesNotMatch(t *testing.T) {
	s, planFile := newSession(t)
	bad := `{"title":"x","phases":[{"id":"P0","title":"a","anchor":"## nowhere"}]}`
	code, _, errs := s.run(bad, "attach", planFile, "--spec", "-")
	if code == 0 || !strings.Contains(errs, "does not occur") {
		t.Fatalf("exit %d: %s", code, errs)
	}
}

func TestNotHostedIsSaidOutLoud(t *testing.T) {
	s, planFile := newSession(t)
	delete(s.env, "BATON_HOST")
	spec := s.must("", "attach", planFile, "--suggest")
	if out := s.must(spec, "attach", planFile, "--spec", "-"); !strings.Contains(out, "not hosted by baton") {
		t.Fatalf("attach: %s", out)
	}
}

func TestParseArgs(t *testing.T) {
	p, err := parseArgs([]string{"P1", "--notes", "a b", "--force", "x", "--until=5m"}, []string{"notes", "until"}, []string{"force"})
	if err != nil || !reflect.DeepEqual(p.pos, []string{"P1", "x"}) || p.vals["notes"] != "a b" || p.vals["until"] != "5m" || !p.bools["force"] {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range [][]string{{"--nope"}, {"--notes"}, {"--force=1"}} {
		if _, err := parseArgs(bad, []string{"notes"}, []string{"force"}); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if p, _ := parseArgs([]string{"--", "--notes"}, []string{"notes"}, nil); !reflect.DeepEqual(p.pos, []string{"--notes"}) {
		t.Errorf("-- did not end flags: %+v", p)
	}
}

func TestStatuslineRecordsContextAndWrapsTheUsersLine(t *testing.T) {
	s, planFile := newSession(t)
	spec := s.must("", "attach", planFile, "--suggest")
	s.must(spec, "attach", planFile, "--spec", "-")
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"statusLine":{"type":"command","command":"echo user-line"}}`), 0o644)
	s.env["HOME"] = home
	s.env["BATON_INSTANCE"] = "inst"
	st, _ := state.Open(s.env["BATON_DIR"], "inst", func() time.Time { return s.now })
	st.Update(func(x *state.State) error { return state.Claim(x, "inst", 1, s.now) })

	raw, _ := json.Marshal(map[string]any{
		"workspace":      map[string]any{"project_dir": t.TempDir()}, // a Windows path must be JSON-escaped
		"context_window": map[string]any{"used_percentage": 42.4, "context_window_size": 400000},
	})
	input := string(raw)
	out := s.must(input, "statusline")
	if out != "◆ baton · P0 1/3 The overlay · ctx 42%  user-line" {
		t.Fatalf("status line %q", out)
	}
	loaded, _ := st.Load()
	if loaded.Run.Context == nil || loaded.Run.Context.WindowSize != 400000 {
		t.Fatalf("context not recorded: %+v", loaded.Run.Context)
	}
}

func TestAttachSuggestedAndInlineSpec(t *testing.T) {
	s, planFile := newSession(t)
	if out := s.must("", "attach", planFile, "--suggested"); !strings.Contains(out, "3 phases") {
		t.Fatalf("--suggested: %s", out)
	}
	inline := `{"title":"Inline","phases":[{"id":"P0","title":"The overlay","anchor":"## P0 — The overlay"},{"id":"R1","title":"Roster","anchor":"## R1 — The roster can grow"}]}`
	if out := s.must("", "attach", planFile, "--replace", "--spec", inline); !strings.Contains(out, `"Inline" — 2 phases`) {
		t.Fatalf("inline spec: %s", out)
	}
}
