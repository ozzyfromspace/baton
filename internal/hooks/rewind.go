package hooks

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/transcript"
)

// A rewind (Esc Esc) fires no hook: baton notices it from the transcript (docs/research/recovery.md). At
// each prompt it marks where the transcript stands (markOf); the prompt is written after its
// UserPromptSubmit, so the turn's next hook looks at it (noticeRewind). A prompt that does not go on from
// the mark came after a rewind, and the branch it left behind is read once, for what baton recorded there:
// that still stands, since a rewind never rolls baton's record back, and the model is told so.

// readLeaf and readFork read the session's transcript (variables, so a test can make them fail).
var (
	readLeaf = transcript.Leaf
	readFork = transcript.Forked
)

// checksRewind are the hooks of a turn that look at its prompt: by the first of them, it is in the
// transcript.
var checksRewind = map[string]bool{"PreToolUse": true, "PostToolUse": true, "PostToolUseFailure": true, "PermissionRequest": true, "Stop": true, "StopFailure": true}

// markOf is where the transcript at path stands, as a prompt is submitted, or nil: there is no transcript
// yet, or it could not be read. Reading is best effort: whatever goes wrong, even a panic, leaves no mark,
// and a rewind goes unnoticed.
func markOf(path string) (m *state.Mark) {
	defer func() {
		if recover() != nil {
			m = nil
		}
	}()
	if path == "" {
		return nil
	}
	tm, err := readLeaf(path)
	if err != nil || tm.Leaf == "" {
		return nil
	}
	return &state.Mark{Path: path, Offset: tm.Offset, Leaf: tm.Leaf}
}

// forkAt is what the transcript shows of the prompt after mark m; best effort, as markOf.
func forkAt(m state.Mark) (f transcript.Fork, err error) {
	defer func() {
		if r := recover(); r != nil {
			f, err = transcript.Fork{}, fmt.Errorf("reading the transcript: %v", r)
		}
	}()
	return readFork(m.Path, transcript.Mark{Offset: m.Offset, Leaf: m.Leaf})
}

// noticeRewind looks, at a hook of the main agent's turn (checksRewind), at the prompt the mark on record
// was taken for, under the run's lock and before the hook does anything else, so that what it leaves for
// the model reaches it at this hook if it can. Once the prompt is in the transcript, or the turn has ended,
// the mark is cleared; a prompt that went on from a rewind is handled then, once (rewound). It returns
// the events to log. Whatever goes wrong in it, even a panic, clears the mark and logs nothing.
func noticeRewind(c Context, st *state.State, s *state.Store) (events []event) {
	m := st.Run.Mark
	if m == nil || !checksRewind[c.Event] || str(c.Input, "agent_id") != "" {
		return nil
	}
	st.Run.Mark = nil
	defer func() {
		if recover() != nil {
			events = nil
		}
	}()
	if str(c.Input, "transcript_path") != m.Path {
		return nil // not the conversation it was taken in
	}
	f, err := forkAt(*m)
	if err == nil && !f.Prompt && c.Event != "Stop" && c.Event != "StopFailure" {
		st.Run.Mark = m // not written yet: a later hook of the turn looks again
		return nil
	}
	if err != nil || f.Left == nil {
		return nil
	}
	r, brief := rewound(c, *st, s, *f.Left)
	return tellRewind(st, r, brief, c.Now, words(c, s))
}

// rewound is what still stands of what baton recorded in the branch a rewind left behind (b), from st.
// Decisions and jobs are found by time: everything baton recorded between the branch's start and the prompt
// the conversation went on with was recorded in that branch. When the branch held baton's brief, the
// conversation is given what a fresh one is (freshContext); brief is the length of the phase's brief in
// it.
func rewound(c Context, st state.State, s *state.Store, b transcript.Branch) (r decide.Rewind, brief int) {
	r.Since = b.Since
	in := func(t time.Time) bool { return !t.IsZero() && !t.Before(b.Since) && !t.After(b.Until) }
	for _, d := range st.Decisions {
		if in(d.At) || d.Resolved != nil && in(d.Resolved.At) {
			r.Decisions = append(r.Decisions, d)
		}
	}
	for _, j := range st.Jobs {
		if in(j.Queued) || in(j.Delivered) {
			if j.Log == "" && j.Finished() {
				j.Log = s.JobLog(j.ID)
			}
			r.Jobs = append(r.Jobs, j)
		}
	}
	done := map[string]bool{}
	for _, cmd := range b.Baton {
		if cmd.Sub != "done" || len(cmd.Args) == 0 || done[cmd.Args[0]] {
			continue
		}
		if ps := st.Phases[cmd.Args[0]]; ps != nil && (ps.Status == state.PhaseDone || ps.Status == state.PhaseVerifying) {
			done[cmd.Args[0]] = true
			r.Done = append(r.Done, cmd.Args[0])
		}
	}
	if st.Blocked != nil && in(st.Blocked.Since) {
		r.Blocked = st.Blocked.Reason
	}
	for _, f := range b.Files {
		if rel, err := filepath.Rel(s.Root, f); err == nil && filepath.IsLocal(rel) {
			f = filepath.ToSlash(rel)
		}
		r.Files = append(r.Files, f)
	}
	if b.Brief {
		if pl, err := s.LoadPlan(); err == nil && st.Mode != state.ModeIdle {
			r.Brief, brief = freshContext(c, s, pl, st)
		}
	}
	return r, brief
}

// tellRewind leaves the model what still stands after a rewind (state.MessageRewind), and the human a line
// that says so. While a compaction is owed or underway, the model is told nothing: the compaction's brief
// gives it the phase afresh, with baton's record.
func tellRewind(st *state.State, r decide.Rewind, brief int, now time.Time, w decide.Words) []event {
	since := r.Since.Local().Format("15:04")
	told := r.Any() && !st.Compacting()
	switch {
	case told:
		state.Tell(st, state.Message{Kind: state.MessageRewind, Text: w.Rewound(r), Phase: st.Current}, now)
		st.Run.Announce = append(st.Run.Announce, "baton: noticed a rewind to before "+since+" — telling Claude what still stands of baton's record")
	case r.Any():
		st.Run.Announce = append(st.Run.Announce, "baton: noticed a rewind to before "+since+" — the brief after baton's next compaction gives Claude what stands")
	default:
		st.Run.Announce = append(st.Run.Announce, "baton: noticed a rewind to before "+since+" — nothing baton recorded since then")
	}
	var decisions, jobs []string
	for _, d := range r.Decisions {
		decisions = append(decisions, d.ID)
	}
	for _, j := range r.Jobs {
		jobs = append(jobs, j.ID)
	}
	fields := map[string]any{"to": r.Since.UTC().Format(time.RFC3339), "phase": st.Current, "decisions": decisions, "jobs": jobs,
		"done": r.Done, "files": r.Files, "brief": r.Brief != "", "told": told}
	events := []event{{"rewound", fields}}
	if brief > 0 && told {
		events = append(events, event{"brief", map[string]any{"type": "fresh", "phase": st.Current, "chars": brief, "via": "rewind"}})
	}
	return events
}
