package hooks

import (
	"fmt"
	"os"

	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/state"
)

// afterCompaction runs for SessionStart with source "compact": it starts the next phase if this was a
// boundary or a switch, and injects the brief, which says which it was. Claude Code runs this before PostCompact, so the brief is
// in place before the rewake wakes the model (docs/research/verification-2.md). A phase resumed after its
// job finished gets the job's result in its brief. ann is what the host left for the human.
func (h *handlers) afterCompaction(c Context, s *state.Store, ann string) (Result, error) {
	pl, err := s.LoadPlan()
	if err != nil {
		return Result{}, nil
	}
	// What the repository looks like as the next phase starts (read before taking the lock: git can be
	// slow). Only a boundary starts a phase.
	var origin state.Origin
	if cur, err := s.Load(); err == nil && cur.BoundaryOwed {
		origin = state.OriginOf(s.Root)
	}
	kind := ""
	var result *state.Job // the job a resumed phase was set aside for, now finished
	_, st, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.WarnOwed = false // the context the question was about is gone
		if st.Mode != state.ModeRunning {
			return nil
		}
		comp := &st.Run.Compaction
		ours := comp.ByBaton && comp.Status == state.CompactActive
		if !ours && (st.BoundaryOwed || st.CheckpointOwed) {
			// Claude Code compacted on its own while a compaction was owed: that one counts.
			adoptOwed(st, c.Now)
			comp.Trigger, ours = "auto", true
		}
		switch {
		case ours && comp.StartsPhase() && st.BoundaryOwed:
			kind = brief.Boundary
			if comp.Reason == "switch" {
				kind = brief.Switch
			}
			if ps := st.Phases[st.Current]; ps != nil && ps.Status == state.PhaseParked {
				kind = brief.Resume
				if j := st.Job(parkJob(ps.Park)); j != nil && j.Finished() {
					if j.Delivered.IsZero() {
						j.Delivered = c.Now
					}
					x := *j
					result = &x
				}
			}
			state.Start(st, c.Now, origin)
		case ours && comp.Reason == "checkpoint":
			kind = brief.Checkpoint
			st.CheckpointOwed = false
		default:
			// Mid-phase auto-compaction: the turn continues by itself, and nothing of baton's is pending.
			kind = brief.Auto
			if !ours {
				comp.ByBaton, comp.Trigger = false, "auto"
			}
		}
		return nil
	})
	if err != nil || kind == "" {
		return Result{}, ok(err)
	}
	doc, _ := os.ReadFile(pl.File)
	handoff, _ := os.ReadFile(s.HandoffPath())
	in := brief.Input{Kind: kind, Plan: pl, Doc: doc, State: st, Handoff: string(handoff), Words: words(c, s)}
	if result != nil {
		in.Result = jobResult(s, *result, nil)
	}
	text := brief.Write(in)
	msg := "baton: context compacted — continuing " + phaseTitle(pl, st.Current)
	switch kind {
	case brief.Boundary, brief.Switch:
		msg = "baton: " + phaseTitle(pl, st.Current) + " starts now, with a fresh brief"
		s.Event("phase_started", map[string]any{"phase": st.Current})
	case brief.Resume:
		msg = "baton: " + phaseTitle(pl, st.Current) + " resumes now, from where it was set aside"
		resumed := map[string]any{"phase": st.Current}
		if pk := st.Phases[st.Current].Park; pk != nil {
			resumed["reason"] = pk.Reason
		}
		s.Event("phase_resumed", resumed)
	}
	if result != nil {
		delivery{jobs: []state.Job{*result}}.event(s, "brief")
	}
	s.Event("brief", map[string]any{"type": kind, "phase": st.Current, "chars": len(text)})
	return Result{Output: map[string]any{
		"systemMessage": lines(ann, msg),
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	}}, nil
}

// postCompactRewake runs in the background after a manual compaction ("asyncRewake"). If baton asked
// for that compaction, it wakes the idle model exactly once per compaction.
func (h *handlers) postCompactRewake(c Context) (Result, error) {
	s, err := h.d.Open(c)
	if err != nil {
		return Result{}, err
	}
	pl, _ := s.LoadPlan()
	var msg string
	var epoch int
	_, _, err = h.update(c, func(st *state.State, _ *state.Store) error {
		comp := &st.Run.Compaction
		if !comp.ByBaton || comp.Epoch <= comp.Rewoken || st.Mode != state.ModeRunning {
			return nil
		}
		if comp.Status != state.CompactActive && comp.Status != state.CompactDone && comp.Status != state.CompactFailed {
			return nil // (Failed: PostCompact, running alongside, is promoting a late finish to Done)
		}
		comp.Rewoken, epoch = comp.Epoch, comp.Epoch
		if comp.StartsPhase() {
			verb := "Begin"
			if st.Resumed() {
				verb = "Resume"
			}
			msg = fmt.Sprintf("%s Context compacted. %s %s now — the brief above has your instructions.", NudgePrefix, verb, phaseTitle(pl, st.Current))
		} else {
			msg = fmt.Sprintf("%s Context compacted at your checkpoint. Continue %s.", NudgePrefix, phaseTitle(pl, st.Current))
		}
		return nil
	})
	if err != nil || msg == "" {
		return Result{}, ok(err)
	}
	s.Event("rewake", map[string]any{"epoch": epoch})
	return Result{Rewake: msg}, nil
}

// parkJob is the job a park waits for ("" for none).
func parkJob(pk *state.Park) string {
	if pk == nil {
		return ""
	}
	return pk.Job
}
