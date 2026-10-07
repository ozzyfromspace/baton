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
	return &session{t: t, env: map[string]string{"BATON_DIR": filepath.Join(dir, ".baton"), "BATON_HOST": "1",
		"BATON_INSTANCE": "inst", "CLAUDE_CODE_SESSION_ID": "sess-1"},
		now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}, planFile
}

// store is the session's run, as the hooks would have bound it.
func (s *session) store() *state.Store {
	s.t.Helper()
	p, err := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
	if err != nil {
		s.t.Fatal(err)
	}
	st, err := p.Bind(state.Binding{Session: "sess-1"})
	if err != nil {
		s.t.Fatal(err)
	}
	return st
}

func (s *session) run(stdin string, args ...string) (int, string, string) {
	io, out, errb := testIO(stdin, s.env)
	io.Now = func() time.Time { return s.now }
	code := Main(args, io)
	return code, out.String(), errb.String()
}

// startNext does what the post-compaction hook does at a phase boundary: start the next phase.
func (s *session) startNext() {
	st := s.store()
	origin := state.OriginOf(st.Root)
	st.Update(func(x *state.State) error { state.Start(x, s.now, origin); return nil })
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
	handoff, _ := os.ReadFile(filepath.Join(s.store().Dir, "handoff.md"))
	if !strings.Contains(string(handoff), "## P0") || !strings.Contains(string(handoff), "Overlay closes modals") {
		t.Fatalf("handoff: %s", handoff)
	}
	if st := s.must("", "status"); !strings.Contains(st, "✓ P0") || !strings.Contains(st, "→ P1") {
		t.Fatalf("status after done: %s", st)
	}

	s.must("", "waiting", "the", "build", "--until", "20m")
	s.mustFail("needs --tried", "blocked", "need", "a", "product", "decision")
	s.must("", "blocked", "need", "a", "product", "decision", "--tried", "the spec and the issue tracker")
	if st := s.must("", "status"); !strings.Contains(st, "blocked: need a product decision\n  tried: the spec and the issue tracker") || strings.Contains(st, "waiting:") {
		t.Fatalf("status when blocked: %s", st)
	}
	// The model cannot clear its own block: only the human, by taking part, lets it act again.
	for _, args := range [][]string{{"resume"}, {"done", "P1"}, {"checkpoint"}, {"waiting", "x", "--until", "1m"}, {"blocked", "x", "--tried", "y"}} {
		s.mustFail("only they can clear that", args...)
	}
	s.human()
	s.must("", "resume")
	s.must("", "checkpoint", "--notes", "half way")
	s.must("", "pause")
	s.mustFail("only apply while a plan is running", "checkpoint")
	s.must("", "resume")

	// done refuses the next phase until the boundary compaction has started it.
	s.mustFail("has not started yet", "done", "P1")
	s.startNext()
	s.must("", "done", "P1")
	s.startNext()
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

	events, _ := os.ReadFile(filepath.Join(s.store().Dir, "events.jsonl"))
	for _, kind := range []string{"attached", "phase_done", "waiting", "blocked", "resumed", "checkpoint", "paused"} {
		if !strings.Contains(string(events), `"kind":"`+kind+`"`) {
			t.Errorf("no %s event in %s", kind, events)
		}
	}
}

func TestCommandsWithoutAPlan(t *testing.T) {
	s, _ := newSession(t)
	for _, args := range [][]string{{"done", "P0"}, {"blocked", "x", "--tried", "y"}, {"waiting", "x", "--until", "1m"}, {"checkpoint"}} {
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
	st := s.store()

	s.env["BATON_COMPACT_CAP"] = "810000"
	raw, _ := json.Marshal(map[string]any{
		"workspace": map[string]any{"project_dir": t.TempDir()}, // a Windows path must be JSON-escaped
		"context_window": map[string]any{"used_percentage": 41, "context_window_size": 1000000,
			"current_usage": map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 2343, "cache_read_input_tokens": 410000, "output_tokens": 90}},
	})
	input := string(raw)
	out := s.must(input, "statusline")
	if out != "◆ baton · P0 1/3 The overlay · ctx 412k/810k  user-line" {
		t.Fatalf("status line %q", out)
	}
	loaded, _ := st.Load()
	if c := loaded.Run.Context; c == nil || c.Tokens != 412_345 || c.WindowSize != 1_000_000 || c.Limit != 810_000 {
		t.Fatalf("context not recorded: %+v", loaded.Run.Context)
	}
	want := "context: 412k of 810k · checkpoint at 486k · asks you at 729k · Claude Code compacts at about 777k"
	if out := s.must("", "status"); !strings.Contains(out, want) {
		t.Fatalf("status (hosted) lacks %q:\n%s", want, out)
	}
	delete(s.env, "BATON_HOST") // from a plain shell: the config's settings, the reading's limit
	delete(s.env, "CLAUDE_CODE_SESSION_ID")
	delete(s.env, "BATON_COMPACT_CAP")
	s.env["BATON_HOME"] = t.TempDir()
	if out := s.must("", "status"); !strings.Contains(out, want) {
		t.Fatalf("status (shell) lacks %q:\n%s", want, out)
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

// Two sessions in one working tree each run a plan of their own. From a shell, status lists both; in a
// session, it shows that session's run and names the other one.
func TestTwoSessionsRunTwoPlans(t *testing.T) {
	s, planFile := newSession(t)
	s.attach(planFile)
	other := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"], "BATON_HOST": "1",
		"BATON_INSTANCE": "inst-2", "CLAUDE_CODE_SESSION_ID": "sess-2"}}
	other.attach(planFile)
	other.must("", "done", "P0")
	if st := s.state(); st.Current != "P0" || st.Phases["P0"].Status != state.PhaseActive {
		t.Fatalf("the other session's done moved this run: %+v", st)
	}

	out := s.must("", "status")
	if !strings.Contains(out, "▶ P0") || !strings.Contains(out, "other baton sessions working in this checkout:\n  sess-2 (running, pid") {
		t.Fatalf("status in session 1:\n%s", out)
	}
	shell := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"]}}
	out = shell.must("", "status")
	if !strings.Contains(out, "several runs") || !strings.Contains(out, "sess-1") || !strings.Contains(out, "on P1") {
		t.Fatalf("status from a shell:\n%s", out)
	}
	if errs := shell.fails("done", "P0"); !strings.Contains(errs, "run this inside the session whose run you mean") {
		t.Fatalf("done from a shell: %s", errs)
	}
}
