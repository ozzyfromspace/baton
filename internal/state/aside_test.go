package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// What moving a plan file out of Claude Code's way reads: the session's slug, from its transcript, says
// whether the file is its plan-mode file, and a live run other than the one looked for whose plan is the
// file, or was moved from it, keeps it where it is.
func TestLookAside(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	plans := t.TempDir()
	file := filepath.Join(plans, "humming-whistling-hare.md")
	doc := []byte("# Demo\n\n## P0 — a\nDo a.\n")
	os.WriteFile(file, doc, 0o644)
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	os.WriteFile(tp, fmt.Appendf(nil, `{"type":"attachment","isSidechain":false,"attachment":{"type":"plan_mode"},"uuid":"pm","timestamp":"2026-10-04T11:59:00Z","slug":%q}`+"\n", "humming-whistling-hare"), 0o644)

	for _, c := range []struct {
		name     string
		other    string // the other run's plan: "file", "origin" or none
		live     bool   // its host drives it
		tp       string
		movable  bool
		planMode bool
	}{
		{name: "the session's plan-mode file", tp: tp, movable: true, planMode: true},
		{name: "no transcript", planMode: false},
		{name: "a live run's plan file", other: "file", live: true, tp: tp, planMode: true},
		{name: "a live run's plan moved from it", other: "origin", live: true, tp: tp, planMode: true},
		{name: "a run no host drives", other: "file", tp: tp, movable: true, planMode: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := OpenProject(filepath.Join(t.TempDir(), ".baton"), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if c.other != "" {
				o, _ := p.Bind(Binding{Session: "sess-2", Instance: "inst-2"})
				pl, _ := plan.Build(file, doc, plan.Suggest(doc))
				if c.other == "origin" {
					pl.Origin, pl.File = file, filepath.Join(plans, "demo.md")
				}
				o.SavePlan(pl)
				o.Update(func(st *State) error {
					*st = Attach(pl, now, Origin{})
					if c.live {
						return Claim(st, "inst-2", 4242, now)
					}
					return nil
				})
			}
			a := p.LookAside("sess-1", file, c.tp)
			if a.Movable(file) != c.movable || a.PlanMode(file) != c.planMode || a.Movable(file+".other") {
				t.Fatalf("%+v: movable %v, plan mode %v", a, a.Movable(file), a.PlanMode(file))
			}
			if (c.live && c.other != "") != (a.UsedBy == "sess-2") {
				t.Errorf("used by %q", a.UsedBy)
			}
		})
	}
}

// Move moves only what it may, and Unmove puts it back, never over a file that is there again.
func TestMoveAndUnmove(t *testing.T) {
	plans := t.TempDir()
	file := filepath.Join(plans, "humming-whistling-hare.md")
	os.WriteFile(file, []byte("# Demo\n"), 0o644)
	pl := plan.Plan{Title: "Demo", File: file}
	if moved, err := (Aside{File: file, Slug: "calm-river-otter"}).Move(&pl); moved || err != nil || pl.File != file {
		t.Fatalf("moved another slug's file: %+v", pl)
	}
	a := Aside{File: file, Slug: "humming-whistling-hare"}
	if moved, err := a.Move(&pl); !moved || err != nil || pl.File != filepath.Join(plans, "demo.md") || pl.Origin != file {
		t.Fatalf("moved %v (%v): %+v", moved, err, pl)
	}
	Unmove(&pl)
	if pl.File != file || pl.Origin != "" {
		t.Fatalf("not put back: %+v", pl)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	a.Move(&pl)
	os.WriteFile(file, []byte("# Another plan\n"), 0o644) // Claude Code wrote the next plan there
	Unmove(&pl)
	if b, _ := os.ReadFile(file); string(b) != "# Another plan\n" || pl.Origin != file {
		t.Fatalf("put back over the next plan: %q, %+v", b, pl)
	}
}
