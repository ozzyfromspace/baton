// Package transcript reads Claude Code's transcript of a session, the JSONL file every hook's input names
// in transcript_path, for what no hook's input says: why a turn failed. Claude Code does not document the
// format; what baton reads was measured (docs/research/recovery.md). Reading is best effort and read-only:
// a missing file or a line that does not parse gives nothing, and baton then behaves as if it had not
// looked.
package transcript

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// Entry is one line of a transcript, as far as baton reads it.
type Entry struct {
	Type    string // user, assistant, system, attachment, …
	Subtype string // a system entry's: compact_boundary, model_refusal_no_fallback, …
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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	from := max(fi.Size()-n, 0)
	data := make([]byte, fi.Size()-from)
	if _, err := f.ReadAt(data, from); err != nil && err != io.EOF {
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

// LastRefusal is the refusal that ended the last turn of the entries, oldest first: one with no prompt or
// reply of the model's after it. nil if the last turn did not end in one.
//
// Claude Code goes on once by itself when the safeguards stop a response ("continuing once with that
// noted"), and that response carries stop_reason "refusal" too; only when it will not go on does it write
// the two entries read here: a system entry model_refusal_no_fallback, and a synthetic assistant message
// marked isApiErrorMessage. Either one is enough.
func LastRefusal(entries []Entry) *Refusal {
	var r *Refusal
	for _, e := range entries {
		switch {
		case e.Sidechain:
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

// Refused reads the end of the transcript at path for a refusal that ended its last turn (LastRefusal).
func Refused(path string) (*Refusal, error) {
	entries, err := Tail(path, TailBytes)
	if err != nil {
		return nil, err
	}
	return LastRefusal(entries), nil
}

// line is the JSON of a transcript entry, as far as baton reads it. message stays raw: its shape differs
// between entry types, and one baton does not expect must not lose it the rest of the line.
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
	Message           json.RawMessage `json:"message"`
}

// message is the part of an assistant entry's message baton reads.
type message struct {
	StopReason  string `json:"stop_reason"`
	StopDetails struct {
		Category    string `json:"category"`
		Explanation string `json:"explanation"`
	} `json:"stop_details"`
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
	e := Entry{Type: l.Type, Subtype: l.Subtype, UUID: l.UUID, Parent: l.ParentUUID, Source: l.PromptSource, Meta: l.IsMeta, Sidechain: l.IsSidechain}
	if e.Parent == "" {
		e.Parent = l.LogicalParentUUID
	}
	switch {
	case l.Type == "system" && l.Subtype == "model_refusal_no_fallback":
		e.Refusal = &Refusal{Category: l.RefusalCategory, Explanation: l.RefusalWhy}
	case l.Type == "assistant" && l.IsAPIErrorMessage:
		var m message
		if json.Unmarshal(l.Message, &m) == nil && m.StopReason == "refusal" {
			e.Refusal = &Refusal{Category: m.StopDetails.Category, Explanation: m.StopDetails.Explanation}
		}
	}
	return e, true
}
