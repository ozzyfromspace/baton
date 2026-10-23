package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// markBefore is the mark UserPromptSubmit takes in the transcript fixture file: as it stood just before
// the entry with uuid was written ("" for as it stands).
func markBefore(t *testing.T, file, uuid string) Mark {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	if uuid != "" {
		i := bytes.Index(data, []byte(`"uuid":"`+uuid+`"`))
		if i < 0 {
			t.Fatalf("no %s in %s", uuid, file)
		}
		data = data[:bytes.LastIndexByte(data[:i], '\n')+1]
	}
	prefix := filepath.Join(t.TempDir(), file)
	if err := os.WriteFile(prefix, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Leaf(prefix)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func at(clock string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, "2026-10-12T"+clock+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// The fixtures are shaped like spike 29's transcripts (docs/research/recovery.md), with none of their
// content: three turns, then a prompt that goes on from the last of them or from a rewind.
func TestForked(t *testing.T) {
	left := &Branch{
		// The attachment after the second prompt carries a time a little before it, as recorded ones can:
		// the branch starts at its earliest entry.
		Since: at("12:01:00.400"), Until: at("12:03:00.000"),
		Baton: []Command{
			{Sub: "note", Args: []string{"wrote a.txt unsigned", "--undo", "rm a.txt"}},
			{Sub: "verify", Args: []string{"make test && make vet", "--notes", "a.txt written"}},
			{Sub: "done", Args: []string{"P0", "--notes", "a.txt"}},
		},
		Files: []string{"/work/demo/a.txt", "/work/demo/b.txt"},
	}
	for _, c := range []struct {
		name, file, before string // the mark: before the entry with this uuid
		want               Fork
	}{
		// The prompt goes on from the last entry before it, which is the leaf: every prompt of 1,274 real ones,
		// but after a rewind.
		{"no rewind", "goes-on.jsonl", "p3", Fork{Prompt: true}},
		// The first prompt of a session goes on from SessionStart's attachment.
		{"no rewind, after an attachment", "turns.jsonl", "p1", Fork{Prompt: true}},
		// An entry written after the mark comes in between: the prompt still descends from the leaf.
		{"no rewind, an entry in between", "goes-on-after.jsonl", "q1", Fork{Prompt: true}},
		// Rewound to before the second turn ("Restore conversation"): its prompt's parent is the end of the
		// first turn, and the second and third turns are a branch left behind (spike 29, A).
		{"a rewind", "rewound.jsonl", "p3", Fork{Prompt: true, Left: left}},
		// "Restore code and conversation" leaves no other marker than a later snapshot (B).
		{"a rewind with the code restored", "rewound-restored.jsonl", "p3", Fork{Prompt: true, Left: left}},
		// Rewound to the /compact entry: the conversation goes on from just after the summary, and leaves
		// baton's brief behind with the rest (C2).
		{"a rewind to the compaction", "compacted.jsonl", "p3", Fork{Prompt: true, Left: &Branch{
			Since: at("12:01:00.002"), Until: at("12:02:00.000"),
			Baton: []Command{{Sub: "checkpoint", Args: []string{"--notes", "half of P1"}}},
			Brief: true,
		}}},
		// The turn rewound away began with a prompt the human typed while the model worked, which Claude
		// Code writes as a queued_command attachment rather than a prompt (the real rewind of 2026-10-12).
		{"a rewind of a queued prompt", "rewound-queued.jsonl", "p3", Fork{Prompt: true, Left: &Branch{
			Since: at("12:01:00.000"), Until: at("12:03:00.000"),
			Baton: []Command{{Sub: "note", Args: []string{"fixed b differently", "--undo", "git revert HEAD"}}},
		}}},
		// UserPromptSubmit runs before Claude Code writes the prompt: until it has, there is nothing to tell.
		{"not written yet", "turns.jsonl", "", Fork{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Forked(filepath.Join("testdata", c.file), markBefore(t, c.file, c.before))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
				if got.Left != nil {
					t.Errorf("left: %+v", *got.Left)
				}
			}
		})
	}
}

// The mark is the transcript's size and the uuid of its last entry in the conversation: not a subagent's,
// and not one of Claude Code's own records (file-history-snapshot, last-prompt), which have none.
func TestLeaf(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "rewound.jsonl"))
	for _, c := range []struct {
		name, file, before string
		leaf               string
	}{
		{"the end of a turn", "turns.jsonl", "", "s4"},
		{"after Claude Code's own records", "rewound.jsonl", "t3", "p3"},
		{"before the first prompt", "turns.jsonl", "p1", "h2"},
		{"before anything", "turns.jsonl", "h1", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if m := markBefore(t, c.file, c.before); m.Leaf != c.leaf {
				t.Errorf("leaf %q, want %q", m.Leaf, c.leaf)
			}
		})
	}
	m, err := Leaf(filepath.Join("testdata", "rewound.jsonl"))
	if err != nil || m.Offset != int64(len(data)) || m.Leaf != "t3" {
		t.Errorf("the whole file: %+v, %v", m, err)
	}
	// A subagent's entry is never the leaf: the next prompt follows the main agent's.
	sub := filepath.Join(t.TempDir(), "sub.jsonl")
	os.WriteFile(sub, []byte(`{"parentUuid":"s2","isSidechain":false,"type":"system","uuid":"s3","timestamp":"2026-10-12T12:00:00Z"}`+"\n"+
		`{"parentUuid":"s3","isSidechain":true,"type":"assistant","uuid":"x1","timestamp":"2026-10-12T12:00:01Z"}`+"\n"), 0o644)
	if m, _ := Leaf(sub); m.Leaf != "s3" {
		t.Errorf("a subagent's entry is the leaf: %+v", m)
	}
	// Past the first read: the entries at the end are bigger than it.
	big := filepath.Join(t.TempDir(), "big.jsonl")
	os.WriteFile(big, []byte(`{"parentUuid":null,"type":"user","promptSource":"typed","uuid":"p1","timestamp":"2026-10-12T12:00:00Z"}`+"\n"+
		`{"type":"file-history-snapshot","snapshot":"`+strings.Repeat("x", 3*leafBytes)+`"}`+"\n"), 0o644)
	if m, _ := Leaf(big); m.Leaf != "p1" {
		t.Errorf("a leaf further back than the first read: %+v", m)
	}
	if _, err := Leaf(filepath.Join(t.TempDir(), "gone.jsonl")); err == nil {
		t.Error("no error for a missing transcript")
	}
}

// A mark that does not fit the transcript is an error, and says nothing of a rewind: the file shrank, or
// does not hold the leaf, or is gone.
func TestForkedOnATranscriptThatIsNotTheOneMarked(t *testing.T) {
	path := filepath.Join("testdata", "rewound.jsonl")
	data, _ := os.ReadFile(path)
	if _, err := Forked(path, Mark{Offset: int64(len(data)) + 1, Leaf: "s4"}); err != ErrShrunk {
		t.Errorf("shrunk: %v", err)
	}
	m := markBefore(t, "rewound.jsonl", "p3")
	m.Leaf = "nowhere"
	if f, err := Forked(path, m); err != ErrNoLeaf || f.Left != nil {
		t.Errorf("no leaf: %+v, %v", f, err)
	}
	if _, err := Forked(filepath.Join(t.TempDir(), "gone.jsonl"), m); err == nil {
		t.Error("no error for a missing transcript")
	}
}

// A branch that holds no turn (no prompt, queued prompt or reply) is an entry beside the conversation, not
// a rewind: the human rewinds to before a turn.
func TestABranchWithoutAPromptIsNoRewind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "side.jsonl")
	os.WriteFile(path, []byte(`{"parentUuid":"s2","isSidechain":false,"type":"system","subtype":"turn_duration","uuid":"s3","timestamp":"2026-10-12T12:00:00Z"}`+"\n"+
		`{"parentUuid":"s3","isSidechain":false,"type":"progress","uuid":"g1","timestamp":"2026-10-12T12:00:01Z"}`+"\n"), 0o644)
	m, _ := Leaf(path)
	m.Leaf = "g1" // a side entry Claude Code might one day chain off the conversation
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"parentUuid":"s3","isSidechain":false,"promptSource":"typed","type":"user","uuid":"p2","timestamp":"2026-10-12T12:01:00Z"}` + "\n")
	f.Close()
	if got, err := Forked(path, m); err != nil || !got.Prompt || got.Left != nil {
		t.Errorf("got %+v, %v", got, err)
	}
}

// A branch whose turn is only a prompt the human typed while the model worked, with no reply yet, is a
// rewind too.
func TestARewindOfAQueuedPromptAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queued.jsonl")
	os.WriteFile(path, []byte(`{"parentUuid":"s2","isSidechain":false,"type":"system","subtype":"turn_duration","uuid":"s3","timestamp":"2026-10-12T12:00:00Z"}`+"\n"+
		`{"parentUuid":"s3","isSidechain":false,"type":"attachment","attachment":{"type":"queued_command"},"uuid":"q1","timestamp":"2026-10-12T12:00:30Z"}`+"\n"), 0o644)
	m, _ := Leaf(path)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"parentUuid":"s3","isSidechain":false,"promptSource":"typed","type":"user","uuid":"p2","timestamp":"2026-10-12T12:01:00Z"}` + "\n")
	f.Close()
	got, err := Forked(path, m)
	if err != nil || got.Left == nil || !got.Left.Since.Equal(at("12:00:30")) {
		t.Errorf("got %+v, %v", got, err)
	}
}

// Only a command whose program is baton counts, under any path, after VAR=value words, and in any simple
// command of a line; quoting is taken off its words, and operators inside quotes separate nothing.
func TestBatonCommands(t *testing.T) {
	for _, c := range []struct {
		line string
		want []Command
	}{
		{`baton note "wrote it" --undo 'rm it'`, []Command{{"note", []string{"wrote it", "--undo", "rm it"}}}},
		{`~/.baton/bin/v0.6.0/baton done P2`, []Command{{"done", []string{"P2"}}}},
		{`"C:\Users\me\.baton\bin\baton.exe" checkpoint`, []Command{{"checkpoint", []string{}}}},
		{`echo baton note x`, nil},
		{`cd /work && BATON_X=1 baton verify "make test; make vet" --notes "a | b"`, []Command{{"verify", []string{"make test; make vet", "--notes", "a | b"}}}},
		{"git commit -m x\nbaton done P1 --notes 'it'\\''s done'", []Command{{"done", []string{"P1", "--notes", "it's done"}}}},
		{`(baton await "gh run watch") & baton status`, []Command{{"await", []string{"gh run watch"}}, {"status", []string{}}}},
		{"git commit -F - <<'EOF'\nfix: P1's parser\n\nbaton done P1 comes next\nEOF\nbaton done P1 --notes 'it'\\''s in'",
			[]Command{{"done", []string{"P1", "--notes", "it's in"}}}}, // a here-document's body holds no command
		{`baton`, nil},
		{`batonx note`, nil},
	} {
		got := batonCommands(c.line)
		for i := range got {
			if got[i].Args == nil {
				got[i].Args = []string{}
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q:\n got %q\nwant %q", c.line, got, c.want)
		}
	}
}

// What Tail and Forked read of an entry: its time, a hook's attachment, and an assistant's tool calls.
func TestEntryFields(t *testing.T) {
	all, err := Tail(filepath.Join("testdata", "turns.jsonl"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Entry{}
	for _, e := range all {
		byID[e.UUID] = e
	}
	if e := byID["h2"]; e.Attachment != "hook_additional_context" || e.HookEvent != "SessionStart" || !e.Time.Equal(at("12:00:00.001")) {
		t.Errorf("the brief's attachment: %+v", e)
	}
	if e := byID["a5"]; !reflect.DeepEqual(e.Tools, []Tool{
		{Name: "Bash", Command: `echo baton note nothing && baton verify "make test && make vet" --notes 'a.txt written'`},
		{Name: "Edit", File: "/work/demo/b.txt"},
	}) {
		t.Errorf("the tool calls: %+v", e.Tools)
	}
	if e := byID["r1"]; e.Tools != nil || e.Prompt() {
		t.Errorf("a tool result: %+v", e)
	}
}

// Reading from a byte offset keeps the line that starts there, and skips one it starts inside.
func TestReadFromAnOffset(t *testing.T) {
	path := filepath.Join("testdata", "turns.jsonl")
	data, _ := os.ReadFile(path)
	f, _ := os.Open(path)
	defer f.Close()
	start := int64(bytes.Index(data, []byte(`{"parentUuid":"h2"`)))
	for _, c := range []struct {
		from  int64
		first string
	}{{start, "p1"}, {start + 1, "t1"}} {
		entries, err := read(f, c.from, int64(len(data)))
		if err != nil || len(entries) == 0 || entries[0].UUID != c.first {
			t.Errorf("from %d: first %+v, %v", c.from, entries, err)
		}
	}
}
