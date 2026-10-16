package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// planModeFile copies the session's plan to a plans folder of its own as the session's plan-mode file,
// <plans>/<slug>.md, and has the session's transcript carry slug, marked by its hooks (Runtime.Proven),
// as Claude Code and baton's hooks leave them. It returns the file.
func (s *session) planModeFile(planFile, slug string) string {
	s.t.Helper()
	doc, err := os.ReadFile(planFile)
	if err != nil {
		s.t.Fatal(err)
	}
	dir := filepath.Join(s.t.TempDir(), "plans")
	os.MkdirAll(dir, 0o755)
	file := filepath.Join(dir, "humming-whistling-hare.md")
	os.WriteFile(file, doc, 0o644)
	tp := filepath.Join(s.t.TempDir(), "transcript.jsonl")
	line := fmt.Sprintf(`{"type":"attachment","isSidechain":false,"attachment":{"type":"plan_mode"},"uuid":"pm","timestamp":"2026-10-04T11:59:00Z","slug":%q}`+"\n", slug)
	os.WriteFile(tp, []byte(line), 0o644)
	s.store().Update(func(st *state.State) error { st.Run.Proven = &state.Mark{Path: tp}; return nil })
	return file
}

// `baton attach` of the session's plan-mode file moves it out of Claude Code's way and attaches it there;
// a file of another slug, one in a shell, and one another live run's plan stay where they are.
func TestAttachMovesThePlanModeFileAside(t *testing.T) {
	for _, c := range []struct {
		name  string
		slug  string // the slug the session's transcript carries
		shell bool   // run outside any session
		other bool   // another live run's plan is the file
		moves bool
	}{
		{name: "the session's plan-mode file", slug: "humming-whistling-hare", moves: true},
		{name: "another slug's file", slug: "calm-river-otter"},
		{name: "in a shell", slug: "humming-whistling-hare", shell: true},
		{name: "another live run's plan", slug: "humming-whistling-hare", other: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, planFile := newSession(t)
			file := s.planModeFile(planFile, c.slug)
			if c.other {
				o := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"], "BATON_HOST": "1",
					"BATON_INSTANCE": "inst-2", "CLAUDE_CODE_SESSION_ID": "sess-2"}}
				o.attach(file)
			}
			if c.shell {
				for _, k := range []string{"CLAUDE_CODE_SESSION_ID", "BATON_INSTANCE", "BATON_HOST"} {
					delete(s.env, k)
				}
			}
			out := s.must(s.must("", "attach", file, "--suggest"), "attach", file, "--spec", "-")
			p, _ := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
			run := p.RunOf("sess-1")
			if c.shell {
				for _, r := range p.Runs() {
					if r.Pending {
						run = r.ID // attached outside any session: the next session started here takes it
					}
				}
			}
			st, err := p.Run(run, "")
			if err != nil {
				t.Fatal(err)
			}
			pl, _ := st.LoadPlan()
			moved := filepath.Join(filepath.Dir(file), "the-widget-campaign.md")
			events, _ := os.ReadFile(filepath.Join(st.Dir, "events.jsonl"))
			if !c.moves {
				if pl.File != file || pl.Origin != "" || strings.Contains(out, "moved the plan") || strings.Contains(string(events), `"plan_moved"`) {
					t.Fatalf("moved: %+v\n%s", pl, out)
				}
				if _, err := os.Stat(file); err != nil {
					t.Fatal(err)
				}
				if c.other && !strings.Contains(string(events), `"kind":"plan_not_moved"`) {
					t.Errorf("not logged:\n%s", events)
				}
				return
			}
			if pl.File != moved || pl.Origin != file || pl.SHA256 != pl.SHA() || !strings.Contains(out, decide.PlanMoved(file, moved)) {
				t.Fatalf("plan %+v\n%s", pl, out)
			}
			if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the plan-mode file is still there: %v", err)
			}
			for _, want := range []string{`"kind":"attached"`, `"plan":"` + moved + `"`, `"kind":"plan_moved"`, `"by":"attach"`} {
				if !strings.Contains(string(events), want) {
					t.Errorf("events lack %s:\n%s", want, events)
				}
			}
			if x, _ := st.Load(); !x.PlanLooked {
				t.Error("not on record as looked at")
			}
			// Named by the path it was moved from, while nothing is there, the plan is the run's own.
			if again := s.must("", "run", file); strings.Contains(again, "discards that run's progress") || !strings.Contains(again, "already running") {
				t.Errorf("run by its old path:\n%s", again)
			}
		})
	}
}

// A run whose plan moved out of Claude Code's way is carried on by a session that names it by its new path
// or by the one it was moved from; one left by its host, that is.
func TestAMovedPlansRunIsCarriedOn(t *testing.T) {
	for _, name := range []string{"new path", "old path"} {
		t.Run(name, func(t *testing.T) {
			s, planFile := newSession(t)
			file := s.planModeFile(planFile, "humming-whistling-hare")
			s.attach(file)
			pl, _ := s.store().LoadPlan()
			if pl.Origin != file {
				t.Fatalf("not moved: %+v", pl)
			}
			s.must("", "done", "P0", "--notes", "P0's notes")
			s.startNext()
			s.now = s.now.Add(state.OwnerTTL + time.Minute) // its host is gone
			bg := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"], "BATON_HOST": "1",
				"BATON_INSTANCE": "inst-bg", "CLAUDE_CODE_SESSION_ID": "bg-sess"}}
			named := pl.File
			if name == "old path" {
				named = file
			}
			if out := bg.must("", "run", named); !strings.Contains(out, "carried on the run of") || !strings.Contains(out, "at P1") {
				t.Fatalf("not carried on:\n%s", out)
			}
		})
	}
}
