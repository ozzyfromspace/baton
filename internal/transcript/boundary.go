package transcript

import (
	"bytes"
	"time"
)

// A compaction writes a compact_boundary entry to the transcript of the conversation that compacted, and
// to no other: the lead's to the session's transcript, a subagent's to its own
// (subagents/agent-<id>.jsonl). The lead's is written just after its SessionStart(compact) and PostCompact
// hooks have run (154–175 ms after PostCompact in spike 32), while those hooks, fired for a subagent's
// compaction too, cannot tell whose it is (docs/research/visible-workers.md). So a boundary written to a
// conversation's transcript after a point baton marked is the one proof that this conversation compacted.

// Boundary is a compact_boundary entry of the conversation: where its line starts and ends in the file,
// what started the compaction, and when it was written.
type Boundary struct {
	UUID       string
	Trigger    string // "manual" or "auto" (compactMetadata.trigger)
	Time       time.Time
	Start, End int64 // the byte offsets of its line, End just past its newline
}

// boundaryBytes bounds how much of a transcript Compacted reads: a compaction's boundary is written moments
// before baton looks for it, and what follows it by then (the summary, the files Claude Code reads back in)
// is far smaller.
const boundaryBytes = 4 << 20

// Compacted reads the transcript at path from offset from on (from its last boundaryBytes, if that is
// less) for the conversation's compact boundaries, and returns the last one, or nil for none. A line from
// starts inside is skipped, and so is one not yet ended by a newline: next is where the lines it read end,
// so a caller looking again for a boundary still being written reads on from there. ErrShrunk means the
// file is shorter than from: not the transcript the offset was taken in.
func Compacted(path string, from int64) (b *Boundary, next int64, err error) {
	f, size, err := open(path)
	if err != nil {
		return nil, from, err
	}
	defer f.Close()
	if size < from {
		return nil, from, ErrShrunk
	}
	start := max(from, size-boundaryBytes)
	at := start
	if start > 0 {
		at-- // the byte before says whether start starts a line
	}
	data := make([]byte, size-at)
	if _, err := f.ReadAt(data, at); err != nil {
		return nil, from, err
	}
	if start > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, from, nil
		}
		data, at = data[i+1:], at+int64(i)+1
	}
	next = at
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return b, next, nil
		}
		line := data[:i]
		if bytes.Contains(line, []byte(`"compact_boundary"`)) {
			if e, ok := parse(line); ok && e.Type == "system" && e.Subtype == "compact_boundary" && !e.Sidechain {
				b = &Boundary{UUID: e.UUID, Trigger: e.Trigger, Time: e.Time, Start: at, End: at + int64(i) + 1}
			}
		}
		at += int64(i) + 1
		data, next = data[i+1:], at
	}
}
