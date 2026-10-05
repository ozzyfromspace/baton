package hooks

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// afterCompaction runs for SessionStart with source "compact": it starts the next phase if this was the
// boundary compaction, and injects the brief. Claude Code runs this before PostCompact, so the brief is
// in place before the rewake wakes the model (docs/research/verification-2.md).
func (h *handlers) afterCompaction(c Context, s *state.Store) (Result, error) {
	pl, err := s.LoadPlan()
	if err != nil {
		return Result{}, nil
	}
	head := gitx.Head(filepath.Dir(s.Dir))
	kind := ""
	_, st, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Mode != state.ModeRunning {
			return nil
		}
		comp := st.Run.Compaction
		switch {
		case comp.ByBaton && comp.Reason == "boundary" && st.BoundaryOwed:
			kind = brief.Boundary
			state.Start(st, c.Now, head)
		case comp.ByBaton && comp.Reason == "checkpoint":
			kind = brief.Checkpoint
			st.CheckpointOwed = false
		default:
			kind = brief.Auto
		}
		return nil
	})
	if err != nil || kind == "" {
		return Result{}, ok(err)
	}
	doc, _ := os.ReadFile(pl.File)
	handoff, _ := os.ReadFile(s.HandoffPath())
	text := brief.Write(brief.Input{Kind: kind, Plan: pl, Doc: doc, State: st, Handoff: string(handoff)})
	msg := "baton: context compacted — continuing " + phaseTitle(pl, st.Current)
	if kind == brief.Boundary {
		msg = "baton: " + phaseTitle(pl, st.Current) + " starts now, with a fresh brief"
		s.Event("phase_started", map[string]any{"phase": st.Current})
	}
	s.Event("brief", map[string]any{"type": kind, "phase": st.Current, "chars": len(text)})
	return Result{Output: map[string]any{
		"systemMessage": msg,
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	}}, nil
}

// postCompactRewake runs in the background after a manual compaction ("asyncRewake"). If baton asked
// for that compaction, it wakes the idle model exactly once per compaction.
func (h *handlers) postCompactRewake(c Context) (Result, error) {
	s, err := h.d.Open(c.Env)
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
		if comp.Status != state.CompactActive && comp.Status != state.CompactDone {
			return nil
		}
		comp.Rewoken, epoch = comp.Epoch, comp.Epoch
		if comp.Reason == "boundary" {
			msg = fmt.Sprintf("%s Context compacted. Begin %s now — the brief above has your instructions.", NudgePrefix, phaseTitle(pl, st.Current))
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
