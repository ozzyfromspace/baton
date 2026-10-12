// Package transcript reads Claude Code's transcript of a session, the JSONL file every hook's input names
// in transcript_path, for what no hook's input says: why a turn failed, and whether the human rewound the
// conversation. Claude Code does not document the format; what baton reads was measured
// (docs/research/recovery.md). Reading is best effort and read-only: a missing file or a line that does not
// parse gives nothing, and baton then behaves as if it had not looked.
package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// Entry is one line of a transcript, as far as baton reads it.
type Entry struct {
	Type    string // user, assistant, system, attachment, …
	Subtype string // a system entry's: compact_boundary, model_refusal_no_fallback, …
	Trigger string // a compact boundary's: what started the compaction, "manual" or "auto"
	UUID    string
	// Parent is the entry this one follows: parentUuid, or a compact boundary's logicalParentUuid.
	Parent string
	// Source is a prompt's promptSource ("typed", "system"): set on the user entry that starts a turn,
	// never on a tool result or Claude Code's own text.
	Source    string
	Meta      bool // isMeta: Claude Code's own text, not the human's
	Sidechain bool // a subagent's
	// Refusal is set on the entries Claude Code writes when the model's safeguards declined a turn and it
	// will not go on with it.
	Refusal *Refusal
	Time    time.Time // when it was written (the zero time if it says not)
	// Attachment is an attachment entry's type (hook_additional_context, file, …), and HookEvent the hook
	// whose output it carries, for a hook's (SessionStart, UserPromptSubmit, …).
	Attachment string
	HookEvent  string
	// Tools are the tool calls of an assistant entry.
	Tools []Tool
	// Context is the context an assistant entry's request had, in tokens (Context), or what a compact
	// boundary left; 0 for any other entry.
	Context int
}

// Tool is a tool call the model made.
type Tool struct {
	Name    string // Bash, Write, Edit, …
	Command string // a Bash call's command line
	File    string // the file it names: file_path, or a notebook's notebook_path
}

// Prompt reports a prompt that starts a turn: one the human typed, or one Claude Code queued (a task's
// notification, a hook's wake).
func (e Entry) Prompt() bool { return e.Type == "user" && e.Source != "" }

// Refusal is why the model's safeguards declined a turn, as the API said it.
type Refusal struct {
	Category    string // "cyber", …; "" when the API named none
	Explanation string
}

// TailBytes is how much of the end of a transcript Refused reads: a failed turn's last entries are
// written before its StopFailure hook runs, and are small.
const TailBytes = 256 << 10

// Tail is the entries in the last n bytes of the transcript at path, oldest first. The first line is
// skipped when the tail starts inside it, and so is any line that does not parse (one cut off, say).
func Tail(path string, n int64) ([]Entry, error) {
	f, size, err := open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return read(f, max(size-n, 0), size)
}

// ErrNotFile is a transcript path that names no regular file: a directory, say, which Windows opens as
// if it were an empty file.
var ErrNotFile = errors.New("transcript: not a file")

// open opens the transcript at path, and says how long it is.
func open(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = ErrNotFile
	}
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// read is the entries in bytes [from, to) of f, oldest first. A line that from starts inside is skipped,
// and so is any line that does not parse.
func read(f *os.File, from, to int64) ([]Entry, error) {
	start := from
	if from > 0 {
		start-- // the byte before says whether from starts a line
	}
	data := make([]byte, to-start)
	if _, err := f.ReadAt(data, start); err != nil && err != io.EOF {
		return nil, err
	}
	if from > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, nil
		}
		data = data[i+1:]
	}
	var out []Entry
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if e, ok := parse(line); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// A subagent's own transcript (subagents/agent-<id>.jsonl, beside its session's) holds its conversation
// alone, and Claude Code marks every entry of it as a sidechain's; in a session's transcript, a sidechain
// entry is some subagent's, never the session's own (spike 32). So which entries are the conversation's
// own depends on which transcript is read: agent says it is a subagent's own.
func own(e Entry, agent bool) bool { return e.Sidechain == agent }

// LastRefusal is the refusal that ended the last turn of the entries, oldest first: one with no prompt or
// reply of the model's after it. nil if the last turn did not end in one. agent: the entries are a
// subagent's own transcript's.
//
// Claude Code goes on once by itself when the safeguards stop a response ("continuing once with that
// noted"), and that response carries stop_reason "refusal" too; only when it will not go on does it write
// the two entries read here: a system entry model_refusal_no_fallback, and a synthetic assistant message
// marked isApiErrorMessage. Either one is enough.
func LastRefusal(entries []Entry, agent bool) *Refusal {
	var r *Refusal
	for _, e := range entries {
		switch {
		case !own(e, agent):
		case e.Refusal != nil:
			if r == nil {
				r = &Refusal{}
			}
			if r.Category == "" {
				r.Category = e.Refusal.Category
			}
			if r.Explanation == "" {
				r.Explanation = e.Refusal.Explanation
			}
		case e.Prompt(), e.Type == "assistant":
			r = nil // a turn after it, or the model going on
		}
	}
	return r
}

// Refused reads the end of the transcript at path for a refusal that ended its last turn (LastRefusal);
// agent: it is a subagent's own.
func Refused(path string, agent bool) (*Refusal, error) {
	entries, err := Tail(path, TailBytes)
	if err != nil {
		return nil, err
	}
	return LastRefusal(entries, agent), nil
}

// Context is how many tokens the conversation of the transcript at path holds, as it last said, and when it
// said so: the input of its last reply (the request's input_tokens, cache_read_input_tokens and
// cache_creation_input_tokens: the context the model had, spike 32), or what a compaction since left it
// (a compact boundary's postTokens). 0 when the end of the transcript says neither. agent: the transcript
// is a subagent's own.
func Context(path string, agent bool) (tokens int, at time.Time, err error) {
	for _, n := range []int64{TailBytes, contextBytes} {
		entries, err := Tail(path, n)
		if err != nil {
			return 0, time.Time{}, err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; own(e, agent) && e.Context > 0 {
				return e.Context, e.Time, nil
			}
		}
	}
	return 0, time.Time{}, nil
}

// contextBytes is how much of a transcript Context reads when its last TailBytes hold no reply: a tool's
// result can be that large.
const contextBytes = 4 << 20

// line is the JSON of a transcript entry, as far as baton reads it. message and attachment stay raw: their
// shapes differ between entry types, and one baton does not expect must not lose it the rest of the line.
// So does the timestamp, which is parsed on its own.
type line struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	UUID              string          `json:"uuid"`
	ParentUUID        string          `json:"parentUuid"`
	LogicalParentUUID string          `json:"logicalParentUuid"`
	PromptSource      string          `json:"promptSource"`
	IsMeta            bool            `json:"isMeta"`
	IsSidechain       bool            `json:"isSidechain"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"`
	RefusalCategory   string          `json:"apiRefusalCategory"`
	RefusalWhy        string          `json:"apiRefusalExplanation"`
	Timestamp         string          `json:"timestamp"`
	CompactMetadata   compactMetadata `json:"compactMetadata"`
	Attachment        json.RawMessage `json:"attachment"`
	Message           json.RawMessage `json:"message"`
}

// compactMetadata is the part of a compact boundary's compactMetadata baton reads.
type compactMetadata struct {
	Trigger    string `json:"trigger"`
	PostTokens int    `json:"postTokens"`
}

// attachment is the part of an attachment entry's attachment baton reads.
type attachment struct {
	Type      string `json:"type"`
	HookEvent string `json:"hookEvent"`
}

// message is the part of an assistant entry's message baton reads.
type message struct {
	StopReason  string `json:"stop_reason"`
	StopDetails struct {
		Category    string `json:"category"`
		Explanation string `json:"explanation"`
	} `json:"stop_details"`
	Content json.RawMessage `json:"content"`
	Usage   struct {
		Input         int `json:"input_tokens"`
		CacheRead     int `json:"cache_read_input_tokens"`
		CacheCreation int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// block is a content block of an assistant message, as far as baton reads a tool call's.
type block struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Input struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"input"`
}

func parse(raw []byte) (Entry, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Entry{}, false
	}
	var l line
	if json.Unmarshal(raw, &l) != nil || l.Type == "" {
		return Entry{}, false
	}
	e := Entry{Type: l.Type, Subtype: l.Subtype, Trigger: l.CompactMetadata.Trigger, UUID: l.UUID, Parent: l.ParentUUID, Source: l.PromptSource, Meta: l.IsMeta, Sidechain: l.IsSidechain}
	if e.Parent == "" {
		e.Parent = l.LogicalParentUUID
	}
	e.Time, _ = time.Parse(time.RFC3339Nano, l.Timestamp)
	if l.Type == "attachment" {
		var a attachment
		if json.Unmarshal(l.Attachment, &a) == nil {
			e.Attachment, e.HookEvent = a.Type, a.HookEvent
		}
	}
	if l.Type != "assistant" {
		switch {
		case l.Type == "system" && l.Subtype == "model_refusal_no_fallback":
			e.Refusal = &Refusal{Category: l.RefusalCategory, Explanation: l.RefusalWhy}
		case l.Type == "system" && l.Subtype == "compact_boundary":
			e.Context = l.CompactMetadata.PostTokens
		}
		return e, true
	}
	var m message
	if json.Unmarshal(l.Message, &m) != nil {
		return e, true
	}
	e.Context = m.Usage.Input + m.Usage.CacheRead + m.Usage.CacheCreation
	if l.IsAPIErrorMessage && m.StopReason == "refusal" {
		e.Refusal = &Refusal{Category: m.StopDetails.Category, Explanation: m.StopDetails.Explanation}
	}
	var blocks []block
	if json.Unmarshal(m.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "tool_use" {
				file := b.Input.FilePath
				if file == "" {
					file = b.Input.NotebookPath
				}
				e.Tools = append(e.Tools, Tool{Name: b.Name, Command: b.Input.Command, File: file})
			}
		}
	}
	return e, true
}
