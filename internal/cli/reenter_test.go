package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// /baton run in a conversation that had turns before it compacts first, as an approved plan does: the
// first phase starts from a brief, not from whatever came before. A conversation whose first turn this
// is starts at once, and a plain session compacts once baton hosts it.
func TestRunCompactsAConversationThatHadTurns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     func(map[string]string)
		history bool
		compact bool
		says    []string
	}{
		{name: "hosted, with turns before", history: true, compact: true,
			says: []string{"End your turn now: baton compacts this conversation, then starts P0 (The overlay) with a fresh brief."}},
		{name: "hosted, its first turn", says: []string{"Current phase: P0 (The overlay)."}},
		{name: "plain session", env: func(e map[string]string) { delete(e, "BATON_HOST"); delete(e, "BATON_INSTANCE") }, compact: true,
			says: []string{"P0 (The overlay) starts once baton has compacted this conversation.", "Run /baton start"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, planFile := newSession(t)
			if tc.env != nil {
				tc.env(s.env)
			}
			s.set(func(x *state.State) { x.Run.History = tc.history })
			out := s.must(s.must("", "attach", planFile, "--suggest"), "attach", planFile, "--spec", "-")
			for _, want := range tc.says {
				if !strings.Contains(out, want) {
					t.Errorf("attach said:\n%s\nwant %q", out, want)
				}
			}
			st := s.state()
			if st.BoundaryOwed != tc.compact || st.Current != "P0" || (st.Phases["P0"].Status == state.PhasePending) != tc.compact {
				t.Fatalf("boundary owed %v, P0 %s; want compaction %v", st.BoundaryOwed, st.Phases["P0"].Status, tc.compact)
			}
			if !s.logged("attached", map[string]any{"compact_first": tc.compact}) {
				t.Errorf("events: %s", s.events())
			}
			if tc.compact {
				s.mustFail("has not started yet", "done", "P0")
			}
		})
	}
}

// A resume after the conversation grew by more than state.ResumeGrowth while paused makes the model
// record where the phase stands before it goes on; baton compacts at the stop that follows.
func TestResumeAfterTheConversationGrewAsksForACheckpoint(t *testing.T) {
	s, planFile := newSession(t)
	s.attach(planFile)
	tokens := func(n int) {
		s.set(func(x *state.State) { x.Run.Context = &state.ContextUse{Tokens: n, WindowSize: 1_000_000} })
	}

	tokens(50_000)
	if out := s.must("", "pause"); !strings.Contains(out, "grows by more than 20k tokens") {
		t.Fatalf("pause: %s", out)
	}
	tokens(60_000)
	if out := s.must("", "run"); strings.Contains(out, "checkpoint") || s.state().CheckpointDue {
		t.Fatalf("a small growth asked for a checkpoint: %s", out)
	}

	s.must("", "pause")
	tokens(95_000)
	out := s.must("", "run")
	for _, want := range []string{"grew by 35k tokens while baton was paused", "First record where P0 stands", "baton checkpoint --notes"} {
		if !strings.Contains(out, want) {
			t.Errorf("resume said:\n%s\nwant %q", out, want)
		}
	}
	if st := s.state(); !st.CheckpointDue || st.Mode != state.ModeRunning {
		t.Fatalf("after resume: %+v", st)
	}
	if !s.logged("resumed", map[string]any{"grew": float64(35_000), "checkpoint_due": true}) {
		t.Errorf("events: %s", s.events())
	}
	if out := s.must("", "status"); !strings.Contains(out, "checkpoint: the conversation grew while baton was paused") {
		t.Errorf("status: %s", out)
	}
	if errs := s.fails("propose", "x", "--because", "y", "--undo", "z"); !strings.Contains(errs, "First record where P0 stands") {
		t.Errorf("propose: %s", errs)
	}
	s.must("", "checkpoint", "--notes", "the overlay half done; the paused talk moved the button left")
	if st := s.state(); st.CheckpointDue || !st.CheckpointOwed {
		t.Fatalf("after the checkpoint: due %v owed %v", st.CheckpointDue, st.CheckpointOwed)
	}
}

// baton run decides what running a plan means in this session: the session's own plan carries on, any
// other is attached, never over a run underway without the human, and a finished plan never runs again
// by accident.
func TestRunDecidesWhatRunningMeans(t *testing.T) {
	s, planFile := newSession(t)
	says := func(want string, args ...string) {
		t.Helper()
		if out := s.must("", append([]string{"run"}, args...)...); !strings.Contains(out, want) {
			t.Errorf("baton run %v said:\n%s\nwant %q", args, out, want)
		}
	}
	says("no plan is underway in this session, so attach a plan: find its file")
	says("no plan is underway in this session, so attach other.md: baton attach other.md --suggest", "other.md")

	s.attach(planFile)
	says("is already running, at P0 (The overlay). Nothing to resume.")
	says("is already running, at P0 (The overlay).", planFile)
	says("is running in this session, at P0 (The overlay). Running other.md instead discards that run's progress: ask the human first", "other.md")

	s.must("", "pause")
	says("is paused in this session, at P0 (The overlay). Running other.md instead", "other.md")
	if st := s.state(); st.Mode != state.ModePaused {
		t.Fatalf("a different plan resumed the run: %s", st.Mode)
	}
	says("resumed", planFile)
	if st := s.state(); st.Mode != state.ModeRunning {
		t.Fatalf("not resumed: %s", st.Mode)
	}

	// A plan file baton can no longer follow keeps the run paused.
	s.must("", "pause")
	doc, _ := os.ReadFile(planFile)
	os.WriteFile(planFile, []byte(strings.Replace(string(doc), "## P1", "## Q1", 1)), 0o644)
	if errs := s.fails("run"); !strings.Contains(errs, "not resumed — the plan file changed, and baton can no longer follow it") {
		t.Errorf("run with a broken plan: %s", errs)
	}
	if st := s.state(); st.Mode != state.ModePaused {
		t.Fatalf("resumed onto a plan baton cannot follow: %s", st.Mode)
	}
	os.WriteFile(planFile, doc, 0o644)
	says("resumed")

	s.set(func(x *state.State) { x.Mode, x.Current = state.ModeComplete, "" })
	says("is complete: every phase is done, so there is nothing to run. Ask the human which plan to run next.")
	says("is complete: every phase is done, so there is nothing to run. To run it again from the start, /baton drop it first", planFile)
	says("this session's last plan, is complete, so attach other.md", "other.md")
}

// A phase the human adds to the plan file runs: next, if it is added before the last phase ends, and
// after a compaction when /baton run finds it in a plan that had finished.
func TestPhasesAddedToThePlanFileRun(t *testing.T) {
	s, planFile := newSession(t)
	s.attach(planFile)
	doc, _ := os.ReadFile(planFile)
	addPhase := func(id, title string) {
		cur, _ := os.ReadFile(planFile)
		os.WriteFile(planFile, []byte(strings.Replace(string(cur), "## Verification", "## "+id+" — "+title+"\nDo it.\n\n## Verification", 1)), 0o644)
	}
	for _, id := range []string{"P0", "P1"} {
		s.must("", "done", id)
		s.startNext()
	}
	addPhase("R2", "The roster shrinks") // while R1, the last phase, is under way
	if out := s.must("", "done", "R1"); !strings.Contains(out, "Next phase: R2 (The roster shrinks)") {
		t.Fatalf("done R1 with R2 added:\n%s", out)
	}
	if !s.logged("plan_changed", map[string]any{"adopted": true}) {
		t.Errorf("events: %s", s.events())
	}
	s.startNext()
	if out := s.must("", "done", "R2"); !strings.Contains(out, "the plan is complete") {
		t.Fatalf("done R2: %s", out)
	}

	addPhase("R3", "The roster rests") // after the plan finished
	out := s.must("", "run")
	for _, want := range []string{"was complete, and its file now adds R3 (The roster rests).", "End your turn now: baton compacts this conversation, then starts R3 (The roster rests)"} {
		if !strings.Contains(out, want) {
			t.Errorf("run said:\n%s\nwant %q", out, want)
		}
	}
	st := s.state()
	if st.Mode != state.ModeRunning || st.Current != "R3" || !st.BoundaryOwed || st.Phases["R2"].Status != state.PhaseDone || st.Run.CompleteNotified {
		t.Fatalf("after run: mode %s current %s boundary %v", st.Mode, st.Current, st.BoundaryOwed)
	}

	// A phase the file adds that baton cannot follow is reported, not ignored.
	s.set(func(x *state.State) { x.Mode, x.Current, x.BoundaryOwed = state.ModeComplete, "", false })
	cur, _ := os.ReadFile(planFile)
	os.WriteFile(planFile, append(cur, []byte("\n## R4 — Twice\n\n## R4 — Twice\n")...), 0o644)
	if errs := s.fails("run"); !strings.Contains(errs, "adds phases baton cannot follow") {
		t.Errorf("run with a broken addition: %s", errs)
	}
	os.WriteFile(planFile, doc, 0o644)
}
