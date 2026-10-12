package hooks

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/transcript"
)

// rewindAt is a time in the transcript fixtures (../transcript/testdata), which are shaped like spike 29's.
func rewindAt(clock string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, "2026-10-12T"+clock+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// session is a transcript the fixture's session writes to: a copy of the fixture's first lines, up to the
// entry with uuid upTo, with its files under the project's root. write appends the rest, up to the entry
// with uuid next ("" for all of it), as Claude Code writes the prompt after UserPromptSubmit.
type session struct {
	t     *testing.T
	path  string
	lines []string
	n     int
}

func (f *fixture) session(file, upTo string) *session {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", file))
	if err != nil {
		f.t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "/work/demo", filepath.Dir(f.dir))
	ss := &session{t: f.t, path: filepath.Join(f.t.TempDir(), "transcript.jsonl"), lines: strings.SplitAfter(text, "\n")}
	ss.write(upTo)
	return ss
}

func (ss *session) write(upTo string) {
	ss.t.Helper()
	end := len(ss.lines)
	if upTo != "" {
		for i, l := range ss.lines {
			if strings.Contains(l, `"uuid":"`+upTo+`"`) {
				end = i
				break
			}
		}
	}
	fh, err := os.OpenFile(ss.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		ss.t.Fatal(err)
	}
	defer fh.Close()
	for ; ss.n < end; ss.n++ {
		fh.WriteString(ss.lines[ss.n])
	}
}

// in is a hook input for the session's transcript.
func (ss *session) in(kv ...any) map[string]any {
	m := map[string]any{"transcript_path": ss.path}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func read(path string) map[string]any {
	return map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": path}, "tool_use_id": "t1"}
}

// rewound finds the rewound event in the run's log, or nil.
func (f *fixture) rewound() map[string]any {
	for _, e := range f.events() {
		if e["kind"] == "rewound" {
			return e
		}
	}
	return nil
}

// A rewind past a note, a job and edits tells the model, once and at its next tool result, that the human
// rewound and what still stands, and leaves baton's record as it was.
func TestARewindIsToldWhatStillStands(t *testing.T) {
	f := newFixture(t, true)
	f.now = rewindAt("12:03:00")
	f.set(func(st *state.State) {
		state.AddNote(st, "committed P0 before the rewind", "revert it", rewindAt("12:00:30")) // before: the model remembers it
		state.AddNote(st, "wrote a.txt unsigned", "rm a.txt", rewindAt("12:01:04.500"))
		st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test && make vet", Commit: "abc1234def",
			Status: state.JobRunning, Queued: rewindAt("12:01:06.500")})
	})
	ss := f.session("rewound.jsonl", "p3")
	f.fire("UserPromptSubmit", ss.in("prompt", "Run echo four."))
	before := f.state()
	if m := f.state().Run.Mark; m == nil || m.Leaf != "s4" || m.Path != ss.path {
		t.Fatalf("the mark: %+v", m)
	}
	ss.write("")
	if out := f.fire("PreToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{"file_path": "x"})); out != nil {
		t.Errorf("PreToolUse said something: %v", out)
	}
	if st := f.state(); st.Run.Mark != nil || len(st.Inbox) != 1 || st.Inbox[0].Kind != state.MessageRewind || st.Inbox[0].Phase != "P0" {
		t.Fatalf("after the check: mark %+v, inbox %+v", st.Run.Mark, st.Inbox)
	}
	out := f.fire("PostToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{"file_path": "x"}, "tool_use_id", "t1"))
	if out["decision"] != "block" {
		t.Fatalf("not told: %v", out)
	}
	told := out["reason"].(string)
	for _, want := range []string{
		"[baton] The human rewound this conversation (Esc Esc) to before " + rewindAt("12:01:00").Local().Format("15:04") + ": what came after that is gone from it",
		"baton does not roll anything back for a rewind",
		"- d2, a note: wrote a.txt unsigned. Undo: rm a.txt.",
		"- Job j1 (P0, verify `make test && make vet` on abc1234def) still runs: baton gives you its result when it ends.",
		"Files you edited since then, which are as they are on disk now: a.txt, b.txt.",
		"Go on from the human's message with all this as it stands.",
	} {
		if !strings.Contains(told, want) {
			t.Errorf("the model is not told %q:\n%s", want, told)
		}
	}
	for _, not := range []string{"d1", "P0 is done", "brief", "subagent.txt"} {
		if strings.Contains(told, not) {
			t.Errorf("the model is told %q, which the rewind did not take away:\n%s", not, told)
		}
	}
	if say, _ := out["systemMessage"].(string); !strings.Contains(say, "baton: noticed a rewind to before "+rewindAt("12:01:00").Local().Format("15:04")+" — telling Claude what still stands") {
		t.Errorf("the human's line: %q", say)
	}
	e := f.rewound()
	if e == nil || e["to"] != "2026-10-12T12:01:00Z" || e["told"] != true || e["brief"] != false ||
		!reflect.DeepEqual(e["decisions"], []any{"d2"}) || !reflect.DeepEqual(e["jobs"], []any{"j1"}) || !reflect.DeepEqual(e["files"], []any{"a.txt", "b.txt"}) {
		t.Errorf("the event: %v\n%s", e, f.eventLog())
	}
	if !f.logged("message_delivered", map[string]any{"message": "rewind", "via": "tool"}) {
		t.Errorf("no delivery logged:\n%s", f.eventLog())
	}
	// Once.
	if out := f.fire("PostToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{"file_path": "y"}, "tool_use_id", "t2")); out["decision"] != nil {
		t.Errorf("told twice: %v", out)
	}
	// baton's record stands as it was (the notes were shown to the human meanwhile, as at any tool result).
	after := f.state()
	for i := range after.Decisions {
		after.Decisions[i].Announced = before.Decisions[i].Announced
	}
	if !reflect.DeepEqual(after.Decisions, before.Decisions) || !reflect.DeepEqual(after.Jobs, before.Jobs) || !reflect.DeepEqual(after.Phases, before.Phases) {
		t.Errorf("the record changed:\n before %+v %+v\n after %+v %+v", before.Decisions, before.Jobs, after.Decisions, after.Jobs)
	}
}

// A rewind first seen at the stop refuses it with what still stands: the model goes on with that.
func TestARewindSeenAtTheStopIsToldThere(t *testing.T) {
	f := newFixture(t, true)
	f.now = rewindAt("12:03:00")
	f.set(func(st *state.State) { state.AddNote(st, "wrote a.txt unsigned", "rm a.txt", rewindAt("12:01:04.500")) })
	ss := f.session("rewound.jsonl", "p3")
	f.fire("UserPromptSubmit", ss.in("prompt", "Run echo four."))
	ss.write("")
	out := f.fire("Stop", ss.in())
	if out["decision"] != "block" || !strings.Contains(out["reason"].(string), "d1, a note: wrote a.txt unsigned") {
		t.Fatalf("the stop: %v", out)
	}
	if !f.logged("message_delivered", map[string]any{"message": "rewind", "via": "stop"}) {
		t.Errorf("no delivery logged:\n%s", f.eventLog())
	}
}

// No rewind, nothing told: the prompt goes on from the mark, directly or through an entry written since.
// A prompt not written yet keeps the mark for a later hook of the turn, and the stop clears it.
func TestNoRewindTellsNothing(t *testing.T) {
	for _, c := range []struct{ name, file, upTo string }{
		{"the prompt follows the leaf", "goes-on.jsonl", "p3"},
		{"an entry in between", "goes-on-after.jsonl", "q1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.now = rewindAt("12:03:00")
			f.set(func(st *state.State) { state.AddNote(st, "wrote a.txt unsigned", "rm a.txt", rewindAt("12:01:04.500")) })
			ss := f.session(c.file, c.upTo)
			f.fire("UserPromptSubmit", ss.in("prompt", "go on"))
			ss.write("")
			f.fire("PreToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{}))
			out := f.fire("PostToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{}, "tool_use_id", "t1"))
			if out["decision"] != nil || f.rewound() != nil || f.state().Run.Mark != nil || len(f.state().Inbox) != 0 {
				t.Errorf("told %v, event %v, mark %+v", out, f.rewound(), f.state().Run.Mark)
			}
		})
	}
	t.Run("not written yet", func(t *testing.T) {
		f := newFixture(t, true)
		f.now = rewindAt("12:03:00")
		ss := f.session("rewound.jsonl", "p3")
		f.fire("UserPromptSubmit", ss.in("prompt", "Run echo four."))
		f.fire("PreToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{}))
		if f.state().Run.Mark == nil {
			t.Fatal("the mark went before the prompt was written")
		}
		f.fire("Stop", ss.in())
		if f.state().Run.Mark != nil {
			t.Error("the stop left the mark")
		}
	})
	t.Run("another conversation", func(t *testing.T) {
		f := newFixture(t, true)
		f.now = rewindAt("12:03:00")
		f.set(func(st *state.State) { state.AddNote(st, "wrote a.txt unsigned", "rm a.txt", rewindAt("12:01:04.500")) })
		ss := f.session("rewound.jsonl", "p3")
		f.fire("UserPromptSubmit", ss.in("prompt", "Run echo four."))
		ss.write("")
		f.fire("PreToolUse", map[string]any{"transcript_path": ss.path + ".other", "tool_name": "Read", "tool_input": map[string]any{}})
		if f.state().Run.Mark != nil || f.rewound() != nil {
			t.Errorf("mark %+v, event %v", f.state().Run.Mark, f.rewound())
		}
	})
	t.Run("no plan running", func(t *testing.T) {
		f := newFixture(t, false)
		ss := f.session("rewound.jsonl", "p3")
		f.fire("UserPromptSubmit", ss.in("prompt", "Run echo four."))
		if f.state().Run.Mark != nil {
			t.Error("a mark with no plan running")
		}
	})
}

// A rewind to the /compact entry leaves baton's brief behind: the model is given it again, as a
// conversation /clear started is.
func TestARewindPastTheBriefGivesItAgain(t *testing.T) {
	f := newFixture(t, true)
	f.now = rewindAt("12:02:00")
	f.store.AppendHandoff("P0 (checkpoint)", "Half of a in abc123.")
	ss := f.session("compacted.jsonl", "p3")
	f.fire("UserPromptSubmit", ss.in("prompt", "What was the codeword?"))
	ss.write("")
	out := f.fire("PostToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{}, "tool_use_id", "t1"))
	told, _ := out["reason"].(string)
	for _, want := range []string{
		"The human rewound this conversation",
		"The part rewound away held baton's brief, which this conversation no longer has, so here it is again:\n\nThis session is hosted by baton",
		"[baton] This conversation started without the phase's context",
		"Phase P0, from the plan:\n\n## P0 — First\nDo a.",
		"Half of a in abc123.",
	} {
		if !strings.Contains(told, want) {
			t.Errorf("the model is not told %q:\n%s", want, told)
		}
	}
	if strings.Contains(told, "What it recorded since then still stands") || strings.Contains(told, "Files you edited") {
		t.Errorf("told of a record the rewind left as it was:\n%s", told)
	}
	if e := f.rewound(); e == nil || e["brief"] != true || e["told"] != true {
		t.Errorf("the event: %v", e)
	}
	if !f.logged("brief", map[string]any{"type": "fresh", "phase": "P0", "via": "rewind"}) {
		t.Errorf("no brief event:\n%s", f.eventLog())
	}
}

// While a compaction is owed, the model is told nothing of a rewind: the compaction's brief gives it the
// phase afresh, with baton's record. The human is told so.
func TestARewindWhileACompactionIsOwedTellsTheModelNothing(t *testing.T) {
	f := newFixture(t, true)
	f.now = rewindAt("12:03:00")
	pl, _ := f.store.LoadPlan()
	f.set(func(st *state.State) {
		state.AddNote(st, "wrote a.txt unsigned", "rm a.txt", rewindAt("12:01:04.500"))
		if _, err := state.Done(st, pl, "P0", rewindAt("12:01:10.500"), state.DoneOpts{}); err != nil {
			t.Fatal(err)
		}
	})
	ss := f.session("rewound.jsonl", "p3")
	f.fire("UserPromptSubmit", ss.in("prompt", "Run echo four."))
	ss.write("")
	f.fire("PreToolUse", ss.in("tool_name", "Read", "tool_input", map[string]any{}))
	if st := f.state(); len(st.Inbox) != 0 {
		t.Errorf("a message the compaction would bury: %+v", st.Inbox)
	}
	e := f.rewound()
	if e == nil || e["told"] != false || !reflect.DeepEqual(e["done"], []any{"P0"}) {
		t.Errorf("the event: %v", e)
	}
	out := f.fire("Stop", ss.in())
	if say, _ := out["systemMessage"].(string); !strings.Contains(say, "the brief after baton's next compaction gives Claude what stands") || out["decision"] != nil {
		t.Errorf("the stop: %v", out)
	}
}

// What a rewind finds of baton's record, from the branch it left behind.
func TestRewoundFindsWhatStands(t *testing.T) {
	f := newFixture(t, true)
	b := transcript.Branch{Since: rewindAt("12:01:00"), Until: rewindAt("12:03:00"),
		Baton: []transcript.Command{{Sub: "done", Args: []string{"P1"}}, {Sub: "done", Args: []string{"P0"}}, {Sub: "done"}},
		Files: []string{filepath.Join(filepath.Dir(f.dir), "src", "a.go"), "/elsewhere/b.go"}}
	in, out := rewindAt("12:02:00"), rewindAt("12:00:00")
	st := f.state()
	st.Decisions = []state.Decision{
		{ID: "d1", Kind: state.Note, At: out},
		{ID: "d2", Kind: state.Note, At: in},
		{ID: "d3", Kind: state.Proposal, At: out, Resolved: &state.Resolution{By: state.ByHuman, At: in}}, // answered there
		{ID: "d4", Kind: state.Proposal, At: out},
	}
	st.Jobs = []state.Job{
		{ID: "j1", Status: state.JobPassed, Queued: out, Delivered: out},
		{ID: "j2", Status: state.JobRunning, Queued: in},
		{ID: "j3", Status: state.JobFailed, Queued: out, Delivered: in}, // its result came there
	}
	st.Phases["P1"] = &state.PhaseState{Status: state.PhaseDone}
	st.Blocked = &state.Block{Reason: "no key", Since: in}
	r, brief := rewound(Context{Env: f.env(f.inst)}, st, f.store, b)
	var ds, js []string
	for _, d := range r.Decisions {
		ds = append(ds, d.ID)
	}
	for _, j := range r.Jobs {
		js = append(js, j.ID)
	}
	if !reflect.DeepEqual(ds, []string{"d2", "d3"}) || !reflect.DeepEqual(js, []string{"j2", "j3"}) {
		t.Errorf("decisions %v, jobs %v", ds, js)
	}
	if r.Jobs[1].Log != f.store.JobLog("j3") || r.Jobs[0].Log != "" {
		t.Errorf("the logs: %q, %q", r.Jobs[0].Log, r.Jobs[1].Log)
	}
	if !reflect.DeepEqual(r.Done, []string{"P1"}) || r.Blocked != "no key" || brief != 0 || r.Brief != "" {
		t.Errorf("done %v, blocked %q, brief %d", r.Done, r.Blocked, brief)
	}
	if !reflect.DeepEqual(r.Files, []string{"src/a.go", "/elsewhere/b.go"}) {
		t.Errorf("files %v", r.Files)
	}
}

// Every new path of the rewind check fails open: a reader that panics, or anything after it, leaves the
// hook's own work done, and no mark behind.
func TestTheRewindCheckFailsOpen(t *testing.T) {
	for _, c := range []struct {
		name       string
		leaf, fork bool // which reader panics
	}{{"marking", true, false}, {"checking", false, true}} {
		t.Run(c.name, func(t *testing.T) {
			leaf, fork := readLeaf, readFork
			defer func() { readLeaf, readFork = leaf, fork }()
			if c.leaf {
				readLeaf = func(string) (transcript.Mark, error) { panic("boom") }
			}
			if c.fork {
				readFork = func(string, transcript.Mark) (transcript.Fork, error) { panic("boom") }
			}
			f := newFixture(t, true)
			f.now = rewindAt("12:03:00")
			f.set(func(st *state.State) { state.AddNote(st, "wrote a.txt unsigned", "rm a.txt", rewindAt("12:01:04.500")) })
			ss := f.session("rewound.jsonl", "p3")
			for _, ev := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Stop", "StopFailure"} {
				if code, errs := f.fireCode(ev, ss.in("prompt", "x", "tool_name", "Read", "tool_input", map[string]any{})); code != 0 || errs != "" {
					t.Errorf("%s exited %d: %s", ev, code, errs)
				}
				if ev == "UserPromptSubmit" {
					ss.write("")
					if f.state().Run.TurnOpen != true {
						t.Error("the prompt's own work was not done")
					}
				}
			}
			if st := f.state(); st.Run.Mark != nil || len(st.Inbox) != 0 || f.rewound() != nil {
				t.Errorf("mark %+v, inbox %+v, event %v", st.Run.Mark, st.Inbox, f.rewound())
			}
		})
	}
	// A panic past the readers, in what the check makes of a branch, clears the mark and changes nothing
	// else.
	fork := readFork
	defer func() { readFork = fork }()
	readFork = func(string, transcript.Mark) (transcript.Fork, error) {
		return transcript.Fork{Prompt: true, Left: &transcript.Branch{Since: rewindAt("12:01:00"), Until: rewindAt("12:03:00")}}, nil
	}
	st := state.New()
	st.Mode = state.ModeRunning
	st.Run.Mark = &state.Mark{Path: "t.jsonl", Leaf: "s4"}
	c := Context{Event: "PostToolUse", Input: map[string]any{"transcript_path": "t.jsonl"}, Env: func(string) string { return "" }}
	if ev := noticeRewind(c, &st, nil); ev != nil || st.Run.Mark != nil || len(st.Inbox) != 0 {
		t.Errorf("events %v, mark %+v, inbox %+v", ev, st.Run.Mark, st.Inbox)
	}
}

// The rewind menu's "Summarize from here" and "Summarize up to here" are compactions baton did not type
// (spike 29): the phase goes on with its brief, as after any compaction mid-phase, and nothing wakes the
// model.
func TestASummaryFromTheRewindMenuKeepsTheBrief(t *testing.T) {
	f := newFixture(t, true)
	f.fire("PreCompact", map[string]any{"trigger": "manual"})
	out := f.fire("SessionStart", map[string]any{"source": "compact"})
	if ctx := additionalContext(out); !strings.Contains(ctx, "continue P0 — First") || !strings.Contains(ctx, "Phase P0, from the plan") {
		t.Errorf("the brief:\n%s", ctx)
	}
	if !f.logged("brief", map[string]any{"type": "auto", "phase": "P0"}) {
		t.Errorf("no auto brief:\n%s", f.eventLog())
	}
	if code, _ := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"}); code != 0 {
		t.Error("woke the model after a compaction the human started")
	}
}
