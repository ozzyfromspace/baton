package loop

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Uncommitted work is most at risk when the run halts: nobody may come back to the session for hours.
// The gate on blocked and done asks the model to commit first, but a model can get past any gate it
// can satisfy itself (docs/research/escalation.md, S2). So the host saves the work itself, into a
// private ref (gitx.Snapshot), once at the start of every halt and once more when the session ends.
// It touches nothing the human or the model works with, and only happens where git is usable.

// halt says why the run is waiting on the human or the clock, or "" when it is not halted.
func (l *Loop) halt(v host.View, st state.State) string {
	switch {
	case st.Mode == state.ModePaused:
		return "baton was paused"
	case st.Mode != state.ModeRunning:
		return ""
	case st.Run.Escalation != nil:
		return "the human was asked: " + st.Run.Escalation.Kind
	}
	if d, waiting := st.Run.Dialogs.Front(); waiting && v.Now.Sub(l.since(d.Since)) >= l.Timing.DialogNotify {
		return "a dialog waited on the human (" + d.Tool + ")"
	}
	if e := st.Run.LastError; e != nil && e.Error == "rate_limit" && !st.Run.TurnStarted.After(e.At) {
		return "a usage limit was reached"
	}
	return ""
}

// saveWork snapshots the work once per halt: when one begins, and not again until the run has moved
// on and halted anew. The snapshot runs off the tick, since git can be slow.
func (l *Loop) saveWork(v host.View, st state.State) {
	why := l.halt(v, st)
	if why == "" {
		l.halted = false
		return
	}
	if l.halted {
		return
	}
	l.halted = true
	if !gitx.Usable(filepath.Dir(l.Store.Dir)) || !l.snapping.CompareAndSwap(false, true) {
		return
	}
	l.saving.Add(1)
	go func() {
		defer l.saving.Done()
		defer l.snapping.Store(false)
		l.save(st.Current, why)
	}()
}

// Ended saves the work once more as the session ends, after any snapshot still running. The host calls
// it once the session has exited.
func (l *Loop) Ended() {
	l.saving.Wait()
	if !l.owner {
		return
	}
	st, err := l.Store.Load()
	if err != nil || st.Mode != state.ModeRunning && st.Mode != state.ModePaused {
		return
	}
	l.save(st.Current, "the session ended")
}

// save takes a snapshot and records it: an event, and the ref on the escalation waiting on the human.
func (l *Loop) save(phase, why string) {
	snapshot := l.Snapshot
	if snapshot == nil {
		snapshot = gitx.Snapshot
	}
	start := time.Now()
	saved, err := snapshot(filepath.Dir(l.Store.Dir), l.Store.Now(), phase, why)
	switch {
	case errors.Is(err, gitx.ErrNoGit):
		return
	case err != nil:
		l.logf("loop: saving uncommitted work: %v", err)
		l.Store.Event("snapshot_failed", map[string]any{"why": why, "error": err.Error()})
		return
	case saved.Ref == "" && len(saved.Skipped) == 0:
		return // nothing uncommitted
	}
	fields := map[string]any{"ref": saved.Ref, "files": saved.Files, "why": why, "took": time.Since(start).Round(time.Millisecond).String()}
	if len(saved.Skipped) > 0 {
		fields["skipped"] = saved.Skipped[:min(len(saved.Skipped), 20)]
		l.logf("loop: %d untracked files too big to snapshot: %v", len(saved.Skipped), saved.Skipped)
	}
	if saved.Unchanged {
		fields["unchanged"] = true
	}
	if saved.Ref != "" {
		l.update(func(st *state.State) {
			if e := st.Run.Escalation; e != nil && e.Snapshot == "" {
				e.Snapshot = saved.Ref
			}
		})
		l.logf("loop: saved uncommitted work (%d files) as %s: %s", saved.Files, saved.Ref, why)
	}
	l.Store.Event("snapshot", fields)
}
