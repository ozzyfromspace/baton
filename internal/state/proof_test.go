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
			SetCheckpoint(st, t0)
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
		{"paused, adopted", func(st *State) {
			SetCheckpoint(st, t0)
			Pause(st)
		}, t0.Add(time.Minute), true, "", CompactDone, "P0"},
		{"from before the owe", func(st *State) {
			st.Run.Compaction = Compaction{Epoch: 4, Status: CompactDone, Queued: t0.Add(-time.Hour)}
			SetCheckpoint(st, t0.Add(2*time.Minute))
		}, t0.Add(time.Minute), false, "", CompactDone, "P0"},
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
	SetCheckpoint(&st, t0)
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

// agentBoundary is a compact boundary as Claude Code writes it to a phase agent's own transcript, every
// entry of which is a sidechain's.
func agentBoundary(id string, at time.Time) string {
	return fmt.Sprintf(`{"isSidechain":true,"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","postTokens":9000},"uuid":%q,"timestamp":%q}`+"\n",
		id, at.UTC().Format(time.RFC3339Nano))
}

const agentLine = `{"isSidechain":true,"type":"user","uuid":"u1"}` + "\n"

// checkpointing is a phase's sub-run whose agent a1 checkpointed and stopped: its stop queued the
// compaction a checkpoint owes, which nothing types into an agent.
func checkpointing(t *testing.T, path string) State {
	st := New()
	st.Mode, st.Current = ModeRunning, "P2"
	st.Phases["P2"] = &PhaseState{Status: PhaseWorking}
	st.Run.Briefed, st.Run.Transcript, st.Run.Proven = "a1", path, &Mark{Path: path, Offset: int64(len(agentLine))}
	if err := SetCheckpoint(&st, t0); err != nil {
		t.Fatal(err)
	}
	st.Run.Compaction = Compaction{Status: CompactQueued, Reason: "checkpoint", Epoch: 1}
	st.Run.Context = &ContextUse{Tokens: 150_000, WindowSize: 200_000}
	st.Run.ContextNudged = true
	return st
}

// A phase agent's compaction is proven only by a boundary in its own transcript, past its sub-run's mark,
// and counts for the agent briefed there alone; the lead's proofs move nothing in a sub-run, and an
// agent's nothing of the lead's.
func TestAPhaseAgentsCompactionIsProvenInItsOwnTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-a1.jsonl")
	os.WriteFile(path, []byte(agentLine+agentBoundary("b1", t0)), 0o644)
	lead := filepath.Join(t.TempDir(), "sess.jsonl")
	os.WriteFile(lead, []byte(userLine+boundaryLine("b2", "auto", t0)), 0o644)

	st := checkpointing(t, path)
	p, _, ok := AgentBoundary(st, 0)
	if !ok {
		t.Fatal("no proof in the agent's own transcript")
	}
	for _, c := range []struct {
		name string
		mut  func(st *State)
	}{
		{"another agent briefed now", func(st *State) { st.Run.Briefed = "a2" }},
		{"a mark past it", func(st *State) { st.Run.Proven = &Mark{Path: path, Offset: 1 << 10} }},
		{"a mark in another transcript", func(st *State) { st.Run.Proven = &Mark{Path: lead} }},
	} {
		x := checkpointing(t, path)
		c.mut(&x)
		if _, ok := AgentCompacted(&x, p, t0); ok || !x.CheckpointOwed {
			t.Errorf("%s: counted", c.name)
		}
	}
	// The lead's proofs, and a new agent's, are not a compaction of this agent's conversation.
	leadProof, _, _ := Boundary(lead, &Mark{Path: lead}, 0)
	for name, q := range map[string]Proof{"the lead's boundary": leadProof, "the mod's report": Reported(true, "auto", t0), "a new agent": Started("a1", t0)} {
		x := checkpointing(t, path)
		if _, ok := AgentCompacted(&x, q, t0); ok || !x.CheckpointOwed || x.Run.Compaction.Status != CompactQueued {
			t.Errorf("%s: counted for the agent", name)
		}
	}
	// And an agent's proofs move nothing of the lead's.
	for name, q := range map[string]Proof{"its boundary": p, "a new agent": Started("a1", t0)} {
		x := underway(t)
		before := x.Run.Compaction
		if Report(&x, q, t0) {
			t.Errorf("%s: reported for the lead", name)
		}
		if _, ok := Compacted(&x, q, t0, Origin{}); ok || x.Run.Compaction != before {
			t.Errorf("%s: counted for the lead", name)
		}
	}
	if _, _, ok := AgentBoundary(State{Run: Runtime{Briefed: "a1", Transcript: lead, Proven: &Mark{Path: lead}}}, 0); ok {
		t.Error("the lead's boundary, read as an agent's own transcript")
	}

	// Its own: counted once, ending the checkpoint it owed (its conversation goes on from a summary).
	checkpoint, ok := AgentCompacted(&st, p, t0)
	if !ok || !checkpoint || st.CheckpointOwed || st.Run.Compaction.Status != CompactDone || st.Run.Proven.Offset != int64(len(agentLine)+len(agentBoundary("b1", t0))) {
		t.Fatalf("counted %v, checkpoint %v: %+v, owed %v", ok, checkpoint, st.Run.Compaction, st.CheckpointOwed)
	}
	if _, ok := AgentCompacted(&st, p, t0); ok {
		t.Error("counted twice")
	}
	if _, _, ok := AgentBoundary(st, 0); ok {
		t.Error("proven again past the mark")
	}
}

// A new agent on a phase is a new conversation: a checkpoint the agent before it owed is over, it is the
// agent briefed from then on, and nothing the sub-run knew of the context before it is its own.
func TestANewAgentEndsTheCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-a2.jsonl")
	os.WriteFile(path, []byte(agentLine), 0o644)
	st := checkpointing(t, filepath.Join(dir, "agent-a1.jsonl"))
	if Fresh(&st, Started("a1", t0), path, t0) || !st.CheckpointOwed {
		t.Fatal("the agent briefed already ended its own checkpoint")
	}
	for name, q := range map[string]Proof{"the mod's report": Reported(true, "manual", t0), "a boundary": {agent: "a2", over: true, path: path, end: 9}} {
		if x := checkpointing(t, path); Fresh(&x, q, path, t0) || !x.CheckpointOwed || x.Run.Briefed != "a1" {
			t.Errorf("%s: taken for a new agent", name)
		}
	}
	if !Fresh(&st, Started("a2", t0), path, t0) {
		t.Fatal("a checkpoint was owed")
	}
	if st.CheckpointOwed || st.Run.Compaction.Status != CompactDone || st.Run.Briefed != "a2" || st.Run.Transcript != path ||
		st.Run.Proven == nil || *st.Run.Proven != (Mark{Path: path, Offset: int64(len(agentLine))}) || st.Run.Context != nil || st.Run.ContextNudged {
		t.Fatalf("after a new agent: owed %v, %+v, briefed %q, %q, %+v, %+v", st.CheckpointOwed, st.Run.Compaction, st.Run.Briefed, st.Run.Transcript, st.Run.Proven, st.Run.Context)
	}
	// One whose transcript is not written yet is marked at its start.
	x := checkpointing(t, path)
	gone := filepath.Join(dir, "agent-a3.jsonl")
	Fresh(&x, Started("a3", t0), gone, t0)
	if m := x.Run.Proven; m == nil || m.Path != gone || m.Offset != 0 {
		t.Errorf("marked %+v", m)
	}
}

// The compaction the mod runs for baton's command is baton's, by the command, whatever trigger Claude
// Code gives it: its ack moves it under way (also before the host recorded the try), and an automatic start
// reported meanwhile changes nothing. It is counted once: the mod's word that it is over is kept for the
// rewake, the transcript's boundary counts it, and the mod's word coming again, however late, is dropped.
func TestTheModsOwnCompactionCountsOnce(t *testing.T) {
	st := underway(t)
	SetCheckpoint(&st, t0)
	st.Run.Compaction.Queue("checkpoint", t0)
	epoch := st.Run.Compaction.Epoch
	if !Report(&st, Ran(7, epoch, false, t0), t0) || st.Run.Compaction.Status != CompactActive || !st.Run.Compaction.ByBaton ||
		st.Run.Compaction.Trigger != "manual" || st.Run.ModReports != t0 {
		t.Fatalf("acked before the try was recorded: %+v", st.Run.Compaction)
	}
	st.Run.Compaction.Status, st.Run.Compaction.Command = CompactTyped, 7
	if !Report(&st, Ran(7, epoch, false, t0), t0) || st.Run.Compaction.Status != CompactActive || !st.Run.Compaction.ByBaton {
		t.Fatalf("begun: %+v", st.Run.Compaction)
	}
	if Report(&st, Reported(false, "auto", t0), t0) || st.Run.Compaction.Status != CompactActive {
		t.Fatalf("an automatic start meanwhile: %+v", st.Run.Compaction)
	}
	over := t0.Add(5 * time.Second)
	if !Report(&st, Ran(7, epoch, true, over), over) || st.Run.Compaction.Reported == nil {
		t.Fatalf("over, not kept: %+v", st.Run.Compaction)
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(path, []byte(userLine+boundaryLine("b1", "manual", over)), 0o644)
	st.Run.Proven = &Mark{Path: path, Offset: int64(len(userLine))}
	p, _, _ := Boundary(path, st.Run.Proven, 0)
	if a, ok := Compacted(&st, p, over, Origin{}); !ok || !a.ByBaton || a.Reason != "checkpoint" || st.Run.Compaction.Status != CompactDone || st.Run.Compaction.Reported != nil {
		t.Fatalf("the boundary: %+v %+v", a, st.Run.Compaction)
	}
	late := over.Add(2 * time.Minute)
	if Report(&st, Ran(7, epoch, true, late), late) || st.Run.Compaction.Reported != nil {
		t.Errorf("baton's compaction counted again: %+v", st.Run.Compaction)
	}
}

// A try that did not compact the lead is declined for the try it was: the mod's, by its command, for the
// compaction in flight; or a /compact a hook vetoed, only if baton took it for its own. A decline of an
// earlier try is not the latest one's.
func TestADeclinedTry(t *testing.T) {
	inFlight := func(status string, byBaton bool) State {
		st := underway(t)
		st.Run.Compaction.Queue("boundary", t0)
		st.Run.Compaction.Status, st.Run.Compaction.ByBaton, st.Run.Compaction.Command = status, byBaton, 7
		return st
	}
	for _, c := range []struct {
		name             string
		st               State
		epoch, command   int
		changed, refused bool
	}{
		{"the mod's try", inFlight(CompactTyped, false), 1, 7, true, true},
		{"the mod's try, under way", inFlight(CompactActive, true), 1, 7, true, true},
		{"queued, not recorded yet", inFlight(CompactQueued, false), 1, 7, true, true},
		{"an earlier try", inFlight(CompactTyped, false), 1, 6, true, false},
		{"another compaction's", inFlight(CompactTyped, false), 2, 7, false, false},
		{"one over", inFlight(CompactDone, true), 1, 7, false, false},
		{"a /compact baton took for its own, vetoed", inFlight(CompactActive, true), 0, 0, true, true},
		{"the human's own /compact, vetoed", inFlight(CompactActive, false), 0, 0, false, false},
	} {
		st := c.st
		if got := Declined(&st, c.epoch, c.command, "a turn is running (t1)", t0); got != c.changed {
			t.Errorf("%s: changed %v", c.name, got)
		}
		if got := st.Run.Compaction.Refused() != ""; got != c.refused {
			t.Errorf("%s: refused %q", c.name, st.Run.Compaction.Refused())
		}
	}
}

// proven writes a compact boundary at at to a fresh transcript of the lead's, marked before it, and
// returns its proof.
func proven(t *testing.T, st *State, at time.Time) Proof {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(path, []byte(userLine+boundaryLine("b", "manual", at)), 0o644)
	st.Run.Proven = &Mark{Path: path, Offset: int64(len(userLine))}
	p, _, found := Boundary(path, st.Run.Proven, 0)
	if !found {
		t.Fatal("no proof")
	}
	return p
}

// A compaction while the run is paused counts for the one it owed, baton's or not: /baton run keeps it
// (Keep), so the phase owed starts with no compaction of its own, or the checkpoint is over, unless the
// conversation grew by more than ResumeGrowth since it (measured from the smallest reading after it), or
// a compaction became owed after it.
func TestAPausedRunKeepsItsCompaction(t *testing.T) {
	p := threePhases()
	boundary := func(st *State) {
		Done(st, p, "P0", t0, DoneOpts{})
		st.Run.Compaction.Queue("boundary", t0)
	}
	reading := func(st *State, tokens int, after time.Duration) {
		SawContext(st, ContextUse{Tokens: tokens, At: t0.Add(time.Minute + after)})
	}
	for _, c := range []struct {
		name    string
		owe     func(st *State)
		after   func(st *State) // after the compaction, still paused
		kept    bool
		reason  string
		current string
		resumed bool
	}{
		{name: "a boundary, baton's queued", owe: boundary, kept: true, reason: "boundary", current: "P1"},
		{name: "a boundary, not queued: adopted", owe: func(st *State) { Done(st, p, "P0", t0, DoneOpts{}) }, kept: true, reason: "boundary", current: "P1"},
		{name: "a checkpoint", owe: func(st *State) { SetCheckpoint(st, t0) }, kept: true, reason: "checkpoint", current: "P0"},
		{name: "a parked phase resumes, with its result", owe: func(st *State) {
			q, _, _ := QueueJob(st, Job{Phase: "P0", Kind: JobVerify, Command: "make test"}, t0)
			j := st.Job(q.ID)
			j.Status, j.Exit, j.Ended = JobFailed, 1, t0
			ParkPhase(st, p, "P0", Park{Reason: ParkRed, Since: t0, Job: q.ID})
		}, kept: true, reason: "boundary", current: "P0", resumed: true},
		{name: "grew a little since", owe: boundary, after: func(st *State) {
			reading(st, 30_000, time.Second)
			reading(st, 30_000+ResumeGrowth, time.Hour)
		}, kept: true, reason: "boundary", current: "P1"},
		{name: "grew too much since", owe: boundary, after: func(st *State) {
			reading(st, 30_000, time.Second)
			reading(st, 30_000+ResumeGrowth+1, time.Hour)
		}, current: "P1"},
		{name: "a stale reading first", owe: boundary, after: func(st *State) {
			reading(st, 150_000, time.Second) // the size before the compaction, read again
			reading(st, 30_000, 2*time.Second)
			reading(st, 45_000, time.Hour)
		}, kept: true, reason: "boundary", current: "P1"},
		{name: "a reading from before it", owe: boundary, after: func(st *State) {
			reading(st, 10_000, -time.Hour)
			reading(st, 30_000, time.Hour)
		}, kept: true, reason: "boundary", current: "P1"},
		{name: "owed again after it", owe: func(st *State) { SetCheckpoint(st, t0) }, after: func(st *State) {
			Done(st, p, "P0", t0.Add(time.Hour), DoneOpts{Force: true})
		}, current: "P1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := underway(t)
			st.Run.Context = &ContextUse{Tokens: 150_000, At: t0}
			c.owe(&st)
			if err := Pause(&st); err != nil {
				t.Fatal(err)
			}
			pr := proven(t, &st, t0.Add(time.Minute))
			if a, ok := Compacted(&st, pr, t0.Add(time.Minute), Origin{}); !ok || !a.ByBaton || a.Started || a.Reason != "" {
				t.Fatalf("while paused: %+v %v", a, ok)
			}
			if st.Covered.IsZero() || st.PauseCompacted.IsZero() || st.PauseContext != 0 || st.Run.Compaction.Status != CompactDone {
				t.Fatalf("not covered: covered %v, compacted %v, context %d, %+v", st.Covered, st.PauseCompacted, st.PauseContext, st.Run.Compaction)
			}
			if c.after != nil {
				c.after(&st)
			}
			owed := st.BoundaryOwed || st.CheckpointOwed
			a, kept := Keep(&st, t0.Add(2*time.Hour), Origin{})
			if kept != c.kept || a.Reason != c.reason || a.Resumed != c.resumed || st.Current != c.current {
				t.Fatalf("kept %v (want %v): %+v, current %s", kept, c.kept, a, st.Current)
			}
			if err := Resume(&st); err != nil || st.Mode != ModeRunning || !st.Covered.IsZero() || !st.PauseCompacted.IsZero() {
				t.Fatalf("resume: %v %+v", err, st)
			}
			switch {
			case !c.kept:
				if !owed || !(st.BoundaryOwed || st.CheckpointOwed) || a.Proof != "" {
					t.Errorf("a compaction not kept, and nothing owed after it: %+v", a)
				}
			case c.reason == "boundary":
				if st.BoundaryOwed || !a.Started || a.Proof != "kept" || st.Phases[c.current].Status != PhaseActive {
					t.Errorf("%s not started: %+v %+v", c.current, a, st.Phases[c.current])
				}
				if c.resumed && (a.Result == nil || st.Job(a.Result.ID).Delivered.IsZero()) {
					t.Errorf("its result: %+v", a.Result)
				}
			case c.reason == "checkpoint":
				if st.CheckpointOwed {
					t.Error("the checkpoint is still owed")
				}
			}
		})
	}

	// Nothing to keep: a compaction while running, or a paused run that owed nothing as it compacted.
	st := underway(t)
	SetCheckpoint(&st, t0)
	Compacted(&st, proven(t, &st, t0.Add(time.Minute)), t0.Add(time.Minute), Origin{})
	if _, kept := Keep(&st, t0.Add(time.Hour), Origin{}); kept || !st.Covered.IsZero() {
		t.Errorf("kept a compaction made while running: covered %v", st.Covered)
	}
	st = underway(t)
	Pause(&st)
	Compacted(&st, proven(t, &st, t0.Add(time.Minute)), t0.Add(time.Minute), Origin{})
	st.OweCheckpoint(t0.Add(time.Hour))
	if _, kept := Keep(&st, t0.Add(time.Hour), Origin{}); kept || !st.Covered.IsZero() {
		t.Errorf("kept a compaction that covered nothing owed: covered %v", st.Covered)
	}
}
