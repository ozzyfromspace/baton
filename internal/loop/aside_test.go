package loop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A run attached by an earlier baton runs its plan from the session's plan-mode file, which Claude Code
// adds in full to the context after every compaction. As the lead's next compaction that starts a phase is
// asked for, its plan moves out of the way, once; a checkpoint's compaction, a file not the session's, and
// a run baton looked at already leave it.
func TestAnEarlierRunsPlanMovesAsideAtTheLeadsNextBoundary(t *testing.T) {
	const doc = "# Demo\n\n## P0 — a\nDo a.\n\n## P1 — b\nDo b.\n"
	for _, c := range []struct {
		name   string
		slug   string // the slug the lead's transcript carries
		reason string // the compaction's
		looked bool   // baton looked at the plan already
		moves  bool
	}{
		{name: "at a boundary", slug: "humming-whistling-hare", reason: "boundary", moves: true},
		{name: "at a switch", slug: "humming-whistling-hare", reason: "switch", moves: true},
		{name: "at a checkpoint", slug: "humming-whistling-hare", reason: "checkpoint"},
		{name: "another slug's file", slug: "calm-river-otter", reason: "boundary"},
		{name: "no slug known", reason: "boundary"},
		{name: "looked at already", slug: "humming-whistling-hare", reason: "boundary", looked: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			dir := t.TempDir()
			file := filepath.Join(dir, "humming-whistling-hare.md")
			os.WriteFile(file, []byte(doc), 0o644)
			pl, err := plan.Build(file, []byte(doc), plan.Suggest([]byte(doc)))
			if err != nil {
				t.Fatal(err)
			}
			r.loop.Store.SavePlan(pl)
			tp := filepath.Join(t.TempDir(), "transcript.jsonl")
			line := `{"type":"user","promptSource":"typed","uuid":"u0","timestamp":"2026-10-04T11:00:00Z"}` + "\n"
			if c.slug != "" {
				line += fmt.Sprintf(`{"type":"attachment","isSidechain":false,"attachment":{"type":"plan_mode"},"uuid":"pm","timestamp":"2026-10-04T11:59:00Z","slug":%q}`+"\n", c.slug)
			}
			os.WriteFile(tp, []byte(line), 0o644)
			r.set(func(st *state.State) {
				st.Run.Proven = &state.Mark{Path: tp}
				st.Run.Compaction.Reason = c.reason
				st.PlanLooked = c.looked
			})
			r.tick(time.Second)
			if len(r.in.text) != 1 || r.in.text[0] != "/compact" {
				t.Fatalf("typed %q", r.in.text)
			}
			got, _ := r.loop.Store.LoadPlan()
			events, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl"))
			st := r.state()
			if !c.moves {
				if got.File != file || got.Origin != "" || strings.Contains(string(events), "plan_moved") {
					t.Fatalf("moved: %+v", got)
				}
				if _, err := os.Stat(file); err != nil {
					t.Fatal(err)
				}
				if c.reason == "checkpoint" && st.PlanLooked {
					t.Error("a checkpoint's compaction used up the look the next boundary owes")
				}
				return
			}
			moved := filepath.Join(dir, "demo.md")
			if got.File != moved || got.Origin != file || got.SHA256 != got.SHA() || !st.PlanLooked {
				t.Fatalf("plan %+v", got)
			}
			if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the plan-mode file is still there: %v", err)
			}
			if !strings.Contains(string(events), `"kind":"plan_moved"`) || !strings.Contains(string(events), `"by":"boundary"`) {
				t.Errorf("events:\n%s", events)
			}
			if !slices.Contains(st.Run.Announce, decide.PlanMoved(file, moved)) {
				t.Errorf("not announced: %q", st.Run.Announce)
			}

			// Once: the plan-mode file Claude Code writes at the session's next planning stays where it is.
			os.WriteFile(file, []byte(doc), 0o644)
			r.set(func(st *state.State) {
				st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactQueued, Reason: "boundary", Queued: r.now}
			})
			r.tick(time.Second)
			if again, _ := r.loop.Store.LoadPlan(); again.File != moved {
				t.Fatalf("moved again: %+v", again)
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The workers' sub-runs follow the plan to its new path, which their briefs read, and their agents are
// told nothing: the plan's text is what it was.
func TestWorkersFollowAMovedPlan(t *testing.T) {
	r := newRig(t)
	p, err := state.OpenProject(filepath.Join(t.TempDir(), ".baton"), func() time.Time { return r.now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Bind(state.Binding{Session: "sess-1", Instance: "inst"})
	if err != nil {
		t.Fatal(err)
	}
	const doc = "# Demo\n\n## P0 — a\nDo a.\n\n## P1 — b\nDo b.\n"
	dir := t.TempDir()
	file := filepath.Join(dir, "humming-whistling-hare.md")
	os.WriteFile(file, []byte(doc), 0o644)
	pl, _ := plan.Build(file, []byte(doc), plan.Suggest([]byte(doc)))
	s.SavePlan(pl)
	s.Update(func(st *state.State) error {
		*st = state.Attach(pl, r.now, state.Origin{})
		st.Workers = []state.Worker{{Phase: "P1", Ref: state.WorkerRef(s.Ref, "P1"), Worktree: t.TempDir(), Generation: st.Generation}}
		return nil
	})
	c := &Coordinator{Lead: r.loop, Project: p}
	sub, err := c.subRun(s, pl, state.Worker{Phase: "P1", Worktree: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "demo.md")
	os.Rename(file, moved)
	pl.Origin, pl.File = file, moved
	s.SavePlan(pl)
	st, _ := s.Load()
	c.replan(r.now, s, st, pl)
	got, _ := sub.LoadPlan()
	if got.File != moved || got.Origin != file {
		t.Fatalf("the sub-run's plan %+v", got)
	}
	if x, _ := sub.Load(); len(x.Inbox) != 0 {
		t.Errorf("its agent was told %+v", x.Inbox)
	}
}
