package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

func init() {
	// A rewake looks once: a test writes the boundary it is to find before it fires it.
	rewakeWait, sleep = 0, func(time.Duration) {}
}

// leadTranscript is the session's transcript: one prompt, until a test writes more.
func (f *fixture) leadTranscript() string {
	if f.tp == "" {
		f.tp = filepath.Join(f.t.TempDir(), "transcript.jsonl")
		os.WriteFile(f.tp, []byte(`{"type":"user","promptSource":"typed","uuid":"u0","timestamp":"2026-10-04T11:00:00Z"}`+"\n"), 0o644)
	}
	return f.tp
}

// boundary writes the compact boundary Claude Code writes to the session's transcript just after a
// compaction of the lead (trigger) has run its hooks, stamped now.
func (f *fixture) boundary(trigger string) {
	f.t.Helper()
	tf, err := os.OpenFile(f.leadTranscript(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tf.Close()
	fmt.Fprintf(tf, `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":%q},"uuid":"b-%d","timestamp":%q}`+"\n",
		trigger, f.now.UnixNano(), f.now.UTC().Format(time.RFC3339Nano))
}

// compactionHooks fires the hooks a compaction fires, the lead's or a phase agent's alike: PreCompact,
// SessionStart(compact) and PostCompact, with no agent id and the session's transcript.
func (f *fixture) compactionHooks(trigger string) {
	f.t.Helper()
	f.fire("PreCompact", map[string]any{"trigger": trigger, "prompt_id": "p-1"})
	f.fire("SessionStart", map[string]any{"source": "compact", "prompt_id": "p-1"})
	f.fire("PostCompact", map[string]any{"trigger": trigger, "prompt_id": "p-1"})
}

// marked gives baton a mark in the session's transcript where it ends, as a hook of the lead gives it
// (state.Remark), unless it has one.
func (f *fixture) marked() {
	f.t.Helper()
	path := f.leadTranscript()
	if m := f.state().Run.Proven; m != nil && m.Path == path {
		return
	}
	f.set(func(st *state.State) { state.Remark(st, path, f.now) })
}

// compacted is a compaction of the lead (trigger): its hooks, its boundary in the lead's transcript, and
// the rewake, whose exit code and message it returns.
func (f *fixture) compacted(trigger string) (int, string) {
	f.t.Helper()
	f.marked()
	f.compactionHooks(trigger)
	f.boundary(trigger)
	code, msg := f.fireCode("PostCompactRewake", map[string]any{"trigger": trigger, "prompt_id": "p-1"})
	return code, strings.TrimSpace(msg)
}

// agentCompacted is a phase agent's compaction as the lead's session sees it: the same hooks and the same
// rewake, and no boundary in the lead's transcript (it goes to the agent's own).
func (f *fixture) agentCompacted() (int, string) {
	f.t.Helper()
	f.compactionHooks("auto")
	return f.fireCode("PostCompactRewake", map[string]any{"trigger": "auto", "prompt_id": "p-agent"})
}

// independent is a fixture whose plan's two phases can go at once (so a phase can be switched to).
func independent(t *testing.T) (*fixture, plan.Plan) {
	t.Helper()
	f := newFixture(t, false)
	doc := []byte("# Demo\n\n## P0 — First\nAfter: none\n\nDo a.\n\n## P1 — Second\nAfter: none\n\nDo b.\n")
	docPath := filepath.Join(filepath.Dir(f.dir), "plan.md")
	os.WriteFile(docPath, doc, 0o644)
	pl, err := plan.Build(docPath, doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	f.store.SavePlan(pl)
	f.set(func(st *state.State) { *st = state.Reattach(*st, pl, f.now, state.Origin{}) })
	return f, pl
}

// A phase agent's compaction fires the lead's three compaction hooks (no agent_id, the lead's transcript)
// and the rewake, and writes no boundary to the lead's transcript: whatever the lead owes, its state is
// left exactly as it was. The lead's own compaction, proven by its boundary, does what a compaction did
// before proofs: the next phase after a boundary or a switch, the phase set aside after a resume, the
// same phase after a checkpoint, each woken with its brief; Claude Code's own leaves its brief for the
// next tool call.
func TestOnlyTheLeadsOwnCompactionMovesTheLead(t *testing.T) {
	done := func(f *fixture, pl plan.Plan) {
		f.set(func(st *state.State) { state.Done(st, pl, "P0", f.now, state.DoneOpts{}) })
	}
	for _, c := range []struct {
		name    string
		owe     func(f *fixture, pl plan.Plan)
		trigger string
		kind    string // the brief the lead's compaction owes
		wake    []string
		check   func(t *testing.T, st state.State)
	}{
		{"a boundary owed", func(f *fixture, pl plan.Plan) { done(f, pl); f.fire("Stop", map[string]any{}) }, "manual", "boundary",
			[]string{"Now: P1 — Second. Begin it now.", "[baton] Context compacted. Begin P1 (Second) now"},
			func(t *testing.T, st state.State) {
				if st.BoundaryOwed || st.Current != "P1" || st.Phases["P1"].Status != state.PhaseActive {
					t.Errorf("P1 not started: %+v", st)
				}
			}},
		{"a compaction typed", func(f *fixture, pl plan.Plan) {
			done(f, pl)
			f.fire("Stop", map[string]any{})
			f.set(func(st *state.State) { st.Run.Compaction.Status, st.Run.Compaction.Typed = state.CompactTyped, f.now })
		}, "manual", "boundary", []string{"Begin P1 (Second) now"}, func(t *testing.T, st state.State) {
			if st.Run.Compaction.Status != state.CompactDone || !st.Run.Compaction.ByBaton {
				t.Errorf("compaction %+v", st.Run.Compaction)
			}
		}},
		{"a checkpoint owed", func(f *fixture, _ plan.Plan) {
			f.set(func(st *state.State) { state.SetCheckpoint(st) })
			f.fire("Stop", map[string]any{})
		}, "manual", "checkpoint", []string{"compacted at your checkpoint", "[baton] Context compacted at your checkpoint. Continue P0 (First)."},
			func(t *testing.T, st state.State) {
				if st.CheckpointOwed || st.Current != "P0" {
					t.Errorf("checkpoint still owed: %+v", st)
				}
			}},
		{"a switch owed", func(f *fixture, pl plan.Plan) {
			f.set(func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning, Notes: "a.go done", Queued: f.now}}
				if _, ok := state.Switch(st, pl, f.now); !ok {
					t.Fatal("no switch")
				}
			})
		}, "manual", "switch", []string{"compacted to switch phases", "Begin P1 (Second) now"}, func(t *testing.T, st state.State) {
			if st.Current != "P1" || st.Phases["P0"].Status != state.PhaseParked {
				t.Errorf("not switched: %+v", st)
			}
		}},
		{"a resume owed", func(f *fixture, pl plan.Plan) {
			f.set(func(st *state.State) {
				state.ParkPhase(st, pl, "P0", state.Park{Reason: state.ParkRed, Since: f.now, Notes: "a.go done."})
			})
			f.fire("Stop", map[string]any{})
		}, "manual", "resume", []string{"Now: resume P0 — First", "Resume P0 (First) now"}, func(t *testing.T, st state.State) {
			if !st.Resumed() || st.Phases["P0"].Status != state.PhaseActive {
				t.Errorf("P0 not resumed: %+v", st.Phases["P0"])
			}
		}},
		{"nothing owed", func(*fixture, plan.Plan) {}, "auto", "auto", nil, func(t *testing.T, st state.State) {
			if st.Run.Compaction.ByBaton || st.Current != "P0" {
				t.Errorf("taken for baton's: %+v", st.Run.Compaction)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, pl := independent(t)
			// The lead compacted before, and baton has marked its transcript since.
			f.now = f.now.Add(-time.Hour)
			f.boundary("auto")
			f.now = f.now.Add(time.Hour)
			f.fire("PostToolUse", bash("ls"))
			c.owe(f, pl)
			before := f.state()
			f.now = f.now.Add(time.Minute) // later, so that any write shows
			if code, msg := f.agentCompacted(); code != 0 || msg != "" {
				t.Fatalf("a phase agent's compaction woke the lead: %d %q", code, msg)
			}
			if after := f.state(); !reflect.DeepEqual(before, after) {
				t.Fatalf("a phase agent's compaction moved the lead:\nbefore %+v\nafter  %+v", before, after)
			}
			for _, kind := range []string{"compact_started", "compact_finished", "phase_started", "phase_resumed", "brief", "rewake"} {
				if f.logged(kind, nil) {
					t.Errorf("a phase agent's compaction logged %s: %v", kind, f.events())
				}
			}
			if !f.logged("compact_seen", map[string]any{"hook": "PreCompact", "prompt_id": "p-1"}) {
				t.Errorf("compact_seen not logged: %v", f.events())
			}

			f.now = f.now.Add(time.Minute)
			code, msg := f.compacted(c.trigger)
			if wake := len(c.wake) > 0; wake != (code == 2) {
				t.Fatalf("the lead's own compaction woke %v (%d):\n%s", code == 2, code, msg)
			}
			for _, want := range c.wake {
				if !strings.Contains(msg, want) {
					t.Errorf("the rewake lacks %q:\n%s", want, msg)
				}
			}
			st := f.state()
			c.check(t, st)
			if st.Run.WarnOwed || !st.Run.History || st.Run.Compaction.Finished.IsZero() {
				t.Errorf("after the compaction: %+v", st.Run)
			}
			if len(c.wake) == 0 {
				if b := st.Run.Briefing; b == nil || b.Kind != c.kind || b.Wake {
					t.Fatalf("owed %+v", b)
				}
				msg = feedback(f.fire("PostToolUse", bash("ls")))
			} else if st.Run.Briefing != nil {
				t.Errorf("the brief is still owed after the rewake gave it: %+v", st.Run.Briefing)
			}
			if !f.logged("brief", map[string]any{"type": c.kind}) || !f.logged("compact_finished", map[string]any{"proof": "transcript"}) {
				t.Errorf("events: %v", f.events())
			}
			if !strings.Contains(msg, "Progress:") {
				t.Errorf("no brief:\n%s", msg)
			}
		})
	}
}

// Without proof, nothing changes: a compaction whose boundary never reaches the lead's transcript (or one
// baton cannot read) leaves the compaction baton typed as it was, for the watchdog to escalate in time
// (compaction_failed, loop.TestSerialCompactionWaitsForItsProof).
func TestNoProofNoChange(t *testing.T) {
	f := newFixture(t, true)
	pl, _ := f.store.LoadPlan()
	f.set(func(st *state.State) { state.Done(st, pl, "P0", f.now, state.DoneOpts{}) })
	f.fire("Stop", map[string]any{})
	f.set(func(st *state.State) { st.Run.Compaction.Status, st.Run.Compaction.Typed = state.CompactTyped, f.now })
	before := f.state()
	f.compactionHooks("manual")
	if code, _ := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual", "transcript_path": filepath.Join(t.TempDir(), "gone.jsonl")}); code != 0 {
		t.Fatal("woke without proof")
	}
	if code, _ := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"}); code != 0 {
		t.Fatal("woke without proof")
	}
	if after := f.state(); !reflect.DeepEqual(before, after) {
		t.Fatalf("moved without proof:\nbefore %+v\nafter  %+v", before, after)
	}
}

// Where the lead's transcript cannot say, the mod's report proves the lead compacted. Where it can, its
// boundary does, and the report of the same compaction, early or late, counts for nothing more.
func TestTheModsReportProvesWhatTheTranscriptCannot(t *testing.T) {
	owe := func(f *fixture) {
		pl, _ := f.store.LoadPlan()
		f.set(func(st *state.State) { state.Done(st, pl, "P0", f.now, state.DoneOpts{}) })
		f.fire("Stop", map[string]any{})
		f.set(func(st *state.State) {
			st.Run.Compaction.Status = state.CompactTyped
			state.Report(st, state.Reported(false, "manual", f.now), f.now)
		})
	}
	reported := func(f *fixture) (changed bool) {
		f.set(func(st *state.State) { changed = state.Report(st, state.Reported(true, "manual", f.now), f.now) })
		return changed
	}

	f := newFixture(t, true)
	owe(f)
	f.compactionHooks("manual")
	reported(f)
	gone := filepath.Join(t.TempDir(), "gone.jsonl")
	code, msg := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual", "transcript_path": gone})
	if code != 2 || !strings.Contains(msg, "Begin P1 (Second) now") {
		t.Fatalf("unreadable transcript: %d %q", code, msg)
	}
	if st := f.state(); st.Current != "P1" || st.Run.Compaction.Reported != nil || st.Run.Proven != nil {
		t.Fatalf("after the mod's proof: %+v", st.Run)
	}
	if !f.logged("compact_finished", map[string]any{"proof": "mod"}) {
		t.Errorf("events: %v", f.events())
	}

	// Readable: the boundary proves it, and the report kept before it is spent with it.
	f = newFixture(t, true)
	owe(f)
	reported(f)
	if code, _ := f.compacted("manual"); code != 2 {
		t.Fatal("no wake")
	}
	if st := f.state(); st.Run.Compaction.Reported != nil || !f.logged("compact_finished", map[string]any{"proof": "transcript"}) {
		t.Fatalf("after the transcript's proof: %+v", st.Run.Compaction)
	}
	// One that comes after it is the same compaction's, late.
	if reported(f) || f.state().Run.Compaction.Reported != nil {
		t.Fatal("a late report was kept")
	}
	if code, _ := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"}); code != 0 || strings.Count(f.eventLog(), `"kind":"compact_finished"`) != 1 {
		t.Fatal("counted twice")
	}
}

// Every path of the compaction's proof fails open: a panic in it exits 0 and writes nothing.
func TestCompactionFailsOpen(t *testing.T) {
	failsOpen(t, []failCase{
		{"PreCompact", "PreCompact", func(string) map[string]any { return map[string]any{"trigger": "manual"} }, nil},
		{"SessionStart(compact)", "SessionStart", func(string) map[string]any { return map[string]any{"source": "compact"} }, nil},
		{"PostCompact", "PostCompact", func(string) map[string]any { return map[string]any{"trigger": "auto"} }, nil},
		{"the rewake with a proof", "PostCompactRewake", func(string) map[string]any { return map[string]any{"trigger": "manual"} }, func(f *fixture) {
			f.marked()
			f.boundary("manual")
		}},
		{"a brief with a tool call", "PostToolUse", func(string) map[string]any { return bash("ls") }, func(f *fixture) {
			f.set(func(st *state.State) { st.Run.Briefing = &state.Briefing{Kind: "auto", Phase: "P0", At: f.now} })
		}},
		{"a brief with a prompt", "UserPromptSubmit", func(string) map[string]any { return map[string]any{"prompt": "go"} }, func(f *fixture) {
			f.set(func(st *state.State) { st.Run.Briefing = &state.Briefing{Kind: "auto", Phase: "P0", At: f.now} })
		}},
	})
}
