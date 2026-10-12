package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// boundaryLine is a compact boundary as Claude Code writes it to the lead's transcript, written at at.
func boundaryLine(id, trigger string, at time.Time) string {
	return fmt.Sprintf(`{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":%q},"uuid":%q,"timestamp":%q}`+"\n",
		trigger, id, at.UTC().Format(time.RFC3339Nano))
}

const userLine = `{"type":"user","promptSource":"typed","uuid":"u1"}` + "\n"

// The lead's transcript is marked where it ends, at the lead's own hooks; never past a boundary written a
// moment ago that a rewake may be counting, nor past a line still being written. A transcript baton has
// no mark in is marked at its end, whatever it holds.
func TestRemark(t *testing.T) {
	recent, old := t0.Add(-10*time.Second), t0.Add(-time.Hour)
	for _, c := range []struct {
		name   string
		text   string
		mark   func(path string) *Mark
		want   func(text string) int64 // the mark's offset; -1: unchanged
		noFile bool
	}{
		{"not written yet", "", nil, func(string) int64 { return 0 }, true},
		{"no mark yet", userLine + boundaryLine("b1", "auto", recent) + userLine, nil, func(s string) int64 { return int64(len(s)) }, false},
		{"nothing new", userLine + userLine, func(p string) *Mark { return &Mark{Path: p, Offset: int64(len(userLine))} },
			func(s string) int64 { return int64(len(s)) }, false},
		{"a boundary from long ago", userLine + boundaryLine("b1", "auto", old) + userLine, func(p string) *Mark { return &Mark{Path: p, Offset: int64(len(userLine))} },
			func(s string) int64 { return int64(len(s)) }, false},
		{"a boundary a rewake may be counting", userLine + boundaryLine("b1", "auto", recent) + userLine, func(p string) *Mark { return &Mark{Path: p, Offset: int64(len(userLine))} },
			func(string) int64 { return int64(len(userLine)) }, false},
		{"a line still being written", userLine + userLine + `{"type":"assist`, func(p string) *Mark { return &Mark{Path: p} },
			func(string) int64 { return int64(2 * len(userLine)) }, false},
		{"a mark in another transcript", userLine + boundaryLine("b1", "auto", recent), func(string) *Mark { return &Mark{Path: "/elsewhere", Offset: 3} },
			func(s string) int64 { return int64(len(s)) }, false},
		{"a mark past the end (another file now)", userLine, func(p string) *Mark { return &Mark{Path: p, Offset: 1 << 20} },
			func(s string) int64 { return int64(len(s)) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			if !c.noFile {
				os.WriteFile(path, []byte(c.text), 0o644)
			}
			st := New()
			if c.mark != nil {
				st.Run.Proven = c.mark(path)
			}
			Remark(&st, path, t0)
			if m := st.Run.Proven; m == nil || m.Path != path || m.Offset != c.want(c.text) {
				t.Errorf("marked %+v, want %s at %d", m, path, c.want(c.text))
			}
		})
	}
	// One baton cannot read is left as it was, and so is a hook that names none.
	st := New()
	st.Run.Proven = &Mark{Path: "/x", Offset: 7}
	Remark(&st, t.TempDir(), t0)
	Remark(&st, "", t0)
	if m := st.Run.Proven; m.Path != "/x" || m.Offset != 7 {
		t.Errorf("marked %+v", m)
	}
}

// A boundary proves a compaction only past baton's mark in the same transcript, and only once.
func TestABoundaryProvesItOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(path, []byte(userLine+boundaryLine("b1", "manual", t0)), 0o644)
	before := &Mark{Path: path, Offset: int64(len(userLine))}
	for _, c := range []struct {
		name string
		mark *Mark
		from int64
		want bool
	}{
		{"no mark", nil, 0, false},
		{"a mark in another transcript", &Mark{Path: "/elsewhere"}, 0, false},
		{"a mark before it", before, 0, true},
		{"a mark past it", &Mark{Path: path, Offset: 1 << 10}, 0, false},
		{"read past it already", before, 1 << 10, false},
	} {
		if _, _, got := Boundary(path, c.mark, c.from); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	st := underway(t)
	st.Run.Proven = before
	p, _, _ := Boundary(path, before, 0)
	if _, ok := Compacted(&st, p, t0, Origin{}); !ok {
		t.Fatal("not counted")
	}
	if m := st.Run.Proven; m.Path != path || m.Offset != int64(len(userLine+boundaryLine("b1", "manual", t0))) {
		t.Errorf("the mark after it: %+v", m)
	}
	if _, ok := Compacted(&st, p, t0, Origin{}); ok {
		t.Error("counted twice")
	}
	if _, ok := Compacted(&st, Reported(false, "manual", t0), t0, Origin{}); ok {
		t.Error("a compaction begun counted as over")
	}
}

// underway is a state running threePhases, with P0 under way.
func underway(t *testing.T) State {
	t.Helper()
	st := Attach(threePhases(), t0, Origin{})
	Start(&st, t0, Origin{})
	return st
}

// Whose compaction a proof proves: baton's own (queued, typed, under way, or given up on while still owed),
// one baton owes that Claude Code or the human made instead, or nobody's. One from before baton queued its
// own is not that one.
func TestWhoseCompactionItIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	for _, c := range []struct {
		name    string
		set     func(st *State)
		at      time.Time // the boundary's time
		ours    bool
		reason  string // what it did (After.Reason)
		status  string
		current string
	}{
		{"baton's, at a boundary", func(st *State) {
			Done(st, threePhases(), "P0", t0, DoneOpts{})
			st.Run.Compaction.Queue("boundary", t0)
		}, t0.Add(time.Minute), true, "boundary", CompactDone, "P1"},
		{"baton's, typed", func(st *State) {
			Done(st, threePhases(), "P0", t0, DoneOpts{})
			st.Run.Compaction.Queue("boundary", t0)
			st.Run.Compaction.Status = CompactTyped
		}, t0.Add(time.Minute), true, "boundary", CompactDone, "P1"},
		{"from before baton queued its own", func(st *State) {
			Done(st, threePhases(), "P0", t0, DoneOpts{})
			st.Run.Compaction.Queue("boundary", t0)
			st.Run.Compaction.Status = CompactTyped
		}, t0.Add(-time.Minute), false, "", CompactTyped, "P1"},
		{"owed, not queued yet", func(st *State) {
			st.Run.Compaction = Compaction{Epoch: 4, Status: CompactDone, Queued: t0.Add(-time.Hour)}
			SetCheckpoint(st)
		}, t0.Add(time.Minute), true, "checkpoint", CompactDone, "P0"},
		{"given up on, still owed", func(st *State) {
			Done(st, threePhases(), "P0", t0, DoneOpts{})
			st.Run.Compaction.Queue("boundary", t0)
			st.Run.Compaction.Status = CompactFailed
			st.Run.Escalation = &Escalation{Kind: "compaction_failed", Watchdog: true}
		}, t0.Add(time.Minute), true, "boundary", CompactDone, "P1"},
		{"nothing owed", func(st *State) {
			st.Run.Compaction = Compaction{Epoch: 4, Status: CompactDone, ByBaton: true, Reason: "checkpoint"}
		}, t0.Add(time.Minute), false, "", CompactDone, "P0"},
		{"paused, baton's queued", func(st *State) {
			st.Run.Compaction.Queue("checkpoint", t0)
			st.Mode = ModePaused
		}, t0.Add(time.Minute), true, "", CompactDone, "P0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			os.WriteFile(path, []byte(userLine+boundaryLine("b", "auto", c.at)), 0o644)
			st := underway(t)
			c.set(&st)
			st.Run.WarnOwed = true
			mark := &Mark{Path: path, Offset: int64(len(userLine))}
			st.Run.Proven = mark
			p, _, found := Boundary(path, mark, 0)
			if !found {
				t.Fatal("no proof")
			}
			a, ok := Compacted(&st, p, t0.Add(2*time.Minute), Origin{})
			if !ok || a.ByBaton != c.ours || a.Reason != c.reason || st.Run.Compaction.Status != c.status || st.Current != c.current {
				t.Fatalf("after %+v: ours %v reason %q status %q current %q", a, a.ByBaton, a.Reason, st.Run.Compaction.Status, st.Current)
			}
			if st.Run.WarnOwed || !st.Run.History {
				t.Errorf("warn owed %v, history %v", st.Run.WarnOwed, st.Run.History)
			}
			if c.ours && st.Run.Escalation != nil {
				t.Errorf("the escalation outlived the compaction: %+v", st.Run.Escalation)
			}
			if c.reason == "boundary" && (st.BoundaryOwed || !a.Started || st.Phases["P1"].Status != PhaseActive) {
				t.Errorf("P1 not started: %+v", st.Phases["P1"])
			}
			if c.reason == "checkpoint" && st.CheckpointOwed {
				t.Error("the checkpoint is still owed")
			}
		})
	}
}

// The mod reports a compaction of the lead as it begins and as it ends. One begun by a /compact makes the
// one baton typed (or owes) under way; an automatic one proves nothing yet. One over is kept for the
// rewake, once: a report of the compaction the transcript proved a moment ago is late, and is dropped. A
// kept report proves a compaction once.
func TestTheModsReport(t *testing.T) {
	st := underway(t)
	st.Run.Compaction.Queue("checkpoint", t0)
	st.Run.Compaction.Status = CompactTyped
	if Report(&st, Reported(false, "auto", t0), t0) || st.Run.Compaction.Status != CompactTyped || st.Run.ModReports != t0 {
		t.Fatalf("an automatic start: %+v", st.Run.Compaction)
	}
	if !Report(&st, Reported(false, "manual", t0), t0) || st.Run.Compaction.Status != CompactActive || !st.Run.Compaction.ByBaton {
		t.Fatalf("a /compact begun: %+v", st.Run.Compaction)
	}

	st = underway(t)
	if !Report(&st, Reported(false, "manual", t0), t0) || st.Run.Compaction.ByBaton || st.Run.Compaction.Status == CompactActive {
		t.Fatalf("the human's /compact taken for baton's: %+v", st.Run.Compaction)
	}
	SetCheckpoint(&st)
	if !Report(&st, Reported(false, "manual", t0), t0) || !st.Run.Compaction.ByBaton || st.Run.Compaction.Status != CompactActive || st.Run.Compaction.Reason != "checkpoint" {
		t.Fatalf("an owed compaction not taken up: %+v", st.Run.Compaction)
	}

	if _, ok := Kept(st); ok {
		t.Fatal("kept a report never made")
	}
	if !Report(&st, Reported(true, "manual", t0.Add(time.Minute)), t0.Add(time.Minute)) {
		t.Fatal("a report over not kept")
	}
	p, ok := Kept(st)
	if !ok {
		t.Fatal("not kept")
	}
	if a, ok := Compacted(&st, p, t0.Add(time.Minute), Origin{}); !ok || a.Proof != "mod" || !a.ByBaton || st.CheckpointOwed || st.Run.Compaction.Reported != nil {
		t.Fatalf("the kept report: %+v %+v", a, st.Run.Compaction)
	}
	if _, ok := Compacted(&st, p, t0.Add(time.Minute), Origin{}); ok {
		t.Error("a report counted twice")
	}
	if Report(&st, Reported(true, "manual", t0.Add(time.Minute+10*time.Second)), t0) {
		t.Error("a late report of the compaction just proven was kept")
	}
	if !Report(&st, Reported(true, "auto", t0.Add(time.Hour)), t0) {
		t.Error("a later compaction's report was dropped")
	}
}
