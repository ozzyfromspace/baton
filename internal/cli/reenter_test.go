package cli

import (
	"os"
	"strings"
	"testing"
	"time"

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

	// A plan file baton can no longer follow is reported, and the run goes on with the plan as attached.
	s.must("", "pause")
	doc, _ := os.ReadFile(planFile)
	os.WriteFile(planFile, []byte(strings.Replace(string(doc), "## P1", "## Q1", 1)), 0o644)
	if out := s.must("", "run"); !strings.Contains(out, "baton cannot follow it") || !strings.Contains(out, "the run goes on with the plan as attached") || !strings.Contains(out, "resumed") {
		t.Errorf("run with a broken plan: %s", out)
	}
	if st := s.state(); st.Mode != state.ModeRunning {
		t.Fatalf("not resumed: %s", st.Mode)
	}
	os.WriteFile(planFile, doc, 0o644)
	s.must("", "pause")
	says("resumed")

	s.set(func(x *state.State) { x.Mode, x.Current = state.ModeComplete, "" })
	says("is complete: every phase is done, so there is nothing to run. Ask the human which plan to run next.")
	says("is complete: every phase is done, so there is nothing to run. To run it again from the start, /baton drop it first", planFile)
	says("this session's last plan, is complete, so attach other.md", "other.md")
}

// A phase the human adds to the plan file runs: next, if it is added before the last phase ends, and
// after a compaction when /baton run finds it in a plan that had finished, or resumes a paused run that
// held no phase.
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

	// A run paused while it held no phase, every phase left waiting on a job, takes up a phase added
	// meanwhile that can start as it resumes, rather than once the job ends. cur is the file with R2 and
	// R3, as before the broken addition.
	s.set(func(x *state.State) {
		x.Mode, x.Current, x.BoundaryOwed = state.ModePaused, "", false
		x.Phases["R3"].Status = state.PhaseVerifying
		x.Jobs = append(x.Jobs, state.Job{ID: "j9", Phase: "R3", Kind: state.JobVerify, Command: "make test", Status: state.JobRunning, Started: s.now})
	})
	os.WriteFile(planFile, append(cur, []byte("\n## R5 — The roster is listed\nAfter: P0\n\nDo it.\n")...), 0o644)
	out = s.must("", "run")
	for _, want := range []string{"the plan file now adds R5 (The roster is listed).\n",
		"resumed \"The widget campaign\". No phase was in hand, and R5 (The roster is listed) can start: end your turn now, so baton compacts this conversation, then R5 starts"} {
		if !strings.Contains(out, want) {
			t.Errorf("run said:\n%s\nwant %q", out, want)
		}
	}
	if st := s.state(); st.Mode != state.ModeRunning || st.Current != "R5" || !st.BoundaryOwed || st.Job("j9").Status != state.JobRunning {
		t.Fatalf("after run: mode %s current %s boundary %v", st.Mode, st.Current, st.BoundaryOwed)
	}
	if !s.logged("resumed", map[string]any{"next": "R5"}) {
		t.Errorf("events: %s", s.events())
	}
	// With nothing that can start, the model is told to end its turn.
	s.set(func(x *state.State) {
		x.Mode, x.Current, x.BoundaryOwed = state.ModePaused, "", false
		x.Phases["R5"].Status = state.PhaseVerifying
	})
	if out := s.must("", "run"); !strings.Contains(out, "No phase can start yet") || s.state().Current != "" {
		t.Errorf("run with nothing to start said:\n%s", out)
	}
}

// The human's /compact while the run is paused at a boundary is the one it owed: /baton run counts it, and
// the next phase starts with no compaction of its own, its brief handed over with the command's result,
// unless the conversation grew by more than state.ResumeGrowth since the compaction.
func TestRunKeepsACompactionMadeWhilePaused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		since []int // the context's size as read after the compaction
		kept  bool
	}{
		{name: "nothing read since", kept: true},
		{name: "grew a little since", since: []int{20_000, 20_000 + state.ResumeGrowth}, kept: true},
		{name: "grew too much since", since: []int{20_000, 20_000 + state.ResumeGrowth + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, planFile := newSession(t)
			s.attach(planFile)
			s.must("", "done", "P0")
			s.set(func(x *state.State) { x.Run.Context = &state.ContextUse{Tokens: 150_000, At: s.now} })
			s.must("", "pause")
			// The mod reports the human's /compact, begun and over; the rewake counts it.
			s.now = s.now.Add(time.Minute)
			s.set(func(x *state.State) {
				state.Report(x, state.Reported(false, "manual", s.now), s.now)
				state.Report(x, state.Reported(true, "manual", s.now), s.now)
				p, _ := state.Kept(*x)
				state.Compacted(x, p, s.now, state.Origin{})
				for i, n := range tc.since {
					state.SawContext(x, state.ContextUse{Tokens: n, At: s.now.Add(time.Duration(i+1) * time.Second)})
				}
			})
			if st := s.state(); st.Covered.IsZero() || !st.BoundaryOwed || st.Phases["P1"].Status == state.PhaseActive {
				t.Fatalf("while paused: %+v", st)
			}
			if out := s.must("", "status"); !strings.Contains(out, "compaction: the context was compacted while baton was paused; /baton run counts that for the compaction owed, unless the conversation grows by more than 20k tokens first") {
				t.Errorf("status:\n%s", out)
			}
			s.now = s.now.Add(time.Hour)
			out := s.must("", "run")
			st := s.state()
			if !tc.kept {
				if !strings.Contains(out, "baton drives the plan again") || !st.BoundaryOwed || st.Run.Briefing != nil || !s.logged("compaction_not_kept", map[string]any{"grew": float64(state.ResumeGrowth + 1)}) {
					t.Fatalf("run said:\n%s\nboundary owed %v, events: %s", out, st.BoundaryOwed, s.events())
				}
				return
			}
			if !strings.Contains(out, "P1 (The one-liners) begins now, with no compaction of its own: its brief comes with this command's result") {
				t.Errorf("run said:\n%s", out)
			}
			if st.Mode != state.ModeRunning || st.BoundaryOwed || st.Phases["P1"].Status != state.PhaseActive || st.Run.Compaction.InFlight() {
				t.Fatalf("P1 not started: %+v", st)
			}
			if b := st.Run.Briefing; b == nil || b.Kind != "boundary" || b.Phase != "P1" || b.Wake {
				t.Errorf("the brief owed: %+v", b)
			}
			for kind, want := range map[string]map[string]any{
				"compaction_kept": {"phase": "P1", "reason": "boundary"},
				"phase_started":   {"phase": "P1"},
				"resumed":         {"kept": true},
			} {
				if !s.logged(kind, want) {
					t.Errorf("no %s %v in %s", kind, want, s.events())
				}
			}
		})
	}
}

// A plan attached over a paused run stays paused until /baton run, and keeps the record of the plan it
// replaced, which baton status lists; dropping a plan keeps its record too.
func TestAttachWhilePausedKeepsThePauseAndTheRecord(t *testing.T) {
	s, planFile := newSession(t)
	s.attach(planFile)
	s.must("", "done", "P0")
	s.must("", "pause")
	out := s.must(s.must("", "attach", planFile, "--suggest"), "attach", planFile, "--spec", "-", "--replace")
	for _, want := range []string{"baton was paused, and stays paused: P0 (The overlay) starts when the human runs /baton run.",
		`the record of "The widget campaign" (its state, plan and notes) is kept in .baton/runs/`} {
		if !strings.Contains(out, want) {
			t.Errorf("attach said:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "End your turn now") {
		t.Errorf("attach told the model to end its turn while paused:\n%s", out)
	}
	st := s.state()
	if st.Mode != state.ModePaused || st.Generation != 2 || st.Phases["P0"].Status == state.PhaseDone {
		t.Fatalf("after attach: mode %s, generation %d, P0 %s", st.Mode, st.Generation, st.Phases["P0"].Status)
	}
	if !s.logged("attached", map[string]any{"paused": true}) || !s.logged("plan_archived", map[string]any{"generation": float64(1)}) {
		t.Errorf("events: %s", s.events())
	}
	status := s.must("", "status")
	if !strings.Contains(status, "earlier plans of this run:\n  \"The widget campaign\" — 1 of 3 phases done, kept in .baton/runs/") || !strings.Contains(status, "/gen-1\n") {
		t.Errorf("status:\n%s", status)
	}
	if out := s.must("", "run"); !strings.Contains(out, "resumed") || s.state().Mode != state.ModeRunning {
		t.Fatalf("run: %s", out)
	}

	if out := s.must("", "drop"); !strings.Contains(out, "its record (its state, plan and notes) is kept in .baton/runs/") || !strings.Contains(out, "/gen-2.") {
		t.Errorf("drop said:\n%s", out)
	}
	if got := s.store().Archives(); len(got) != 2 || got[1].Generation != 2 {
		t.Errorf("archives: %+v", got)
	}
}
