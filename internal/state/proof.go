package state

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/ozzyfromspace/baton/internal/transcript"
)

// A loop is the lead's conversation, or one phase agent's. baton changes what it knows about a loop's
// compaction only on a Proof naming that loop, and only these make one (docs/research/visible-workers.md):
// the mod's report of a compaction of the lead (Reported, or Ran for one it ran for baton); a compact boundary written to the loop's own
// transcript past baton's mark (Boundary for the lead's, AgentBoundary for a phase agent's); and, for a
// phase agent, the first start of a new agent on its phase, whose conversation is new (Started). The compaction hooks
// (PreCompact, SessionStart(compact), PostCompact) fire for a phase agent's compaction too, with no agent
// id and the lead's transcript, so they cannot make one: a Proof's fields are unexported, and every
// function that moves Runtime.Compaction past typed, clears an owed compaction, starts a phase after one,
// clears WarnOwed or owes a brief for one takes a Proof (Report, Compacted).
type Proof struct {
	agent   string    // the phase agent whose conversation compacted ("" for the lead's)
	mod     bool      // the mod's report; otherwise a boundary in the transcript
	over    bool      // the compaction is over (a boundary always is); otherwise it has begun
	trigger string    // "manual" or "auto"
	at      time.Time // when the mod reported it, or Claude Code wrote the boundary
	path    string    // a boundary's transcript,
	start   int64     // and where its line starts
	end     int64     // and ends
	command int       // the mod's: the bridge command it ran the compaction for (0: none of baton's),
	epoch   int       // and the compaction that command was for (Compaction.Epoch)
}

// Reported is the proof the mod's report gives of a compaction of the lead, started by trigger: begun, or
// over (over), reported at at. The mod sees the lead's compactions alone (spike 32).
func Reported(over bool, trigger string, at time.Time) Proof {
	return Proof{mod: true, over: over, trigger: trigger, at: at}
}

// Over reports a proof of a compaction over; otherwise it has begun.
func (p Proof) Over() bool { return p.over }

// Trigger is what started the compaction p proves: "manual" or "auto".
func (p Proof) Trigger() string { return p.trigger }

// Ran is the proof the mod's report gives of a compaction of the lead it ran for baton's bridge command
// command, given for the compaction of epoch: begun, as the mod started it (its ack), or over (over), reported
// at at. It is baton's compaction whatever trigger Claude Code gives it (the mod's own call is "plugin" to
// the mod's hooks, "manual" to the settings hooks, spike 35); it counts as manual.
func Ran(command, epoch int, over bool, at time.Time) Proof {
	return Proof{mod: true, over: over, trigger: "manual", at: at, command: command, epoch: epoch}
}

// Boundary reads the lead's transcript at path for a compaction baton has not counted: a compact boundary
// written past mark (Runtime.Proven), read from from on when that is further (where an earlier look
// stopped). It returns the proof if there is one, and where to look on from. A transcript baton has no mark
// in, or one it cannot read, proves nothing.
func Boundary(path string, mark *Mark, from int64) (Proof, int64, bool) {
	if mark == nil || path == "" || mark.Path != path {
		return Proof{}, from, false
	}
	b, next, err := transcript.Compacted(path, max(from, mark.Offset), false)
	if err != nil || b == nil {
		return Proof{}, next, false
	}
	return Proof{over: true, trigger: b.Trigger, at: b.Time, path: path, start: b.Start, end: b.End}, next, true
}

// AgentBoundary reads the own transcript of the agent briefed in a phase's sub-run st (Runtime.Transcript)
// for a compaction of its conversation baton has not counted: a compact boundary past the sub-run's mark
// (Runtime.Proven), read from from on when that is further. It returns the proof, which names that agent,
// if there is one, and where to look on from.
func AgentBoundary(st State, from int64) (Proof, int64, bool) {
	agent, path, mark := st.Run.Briefed, st.Run.Transcript, st.Run.Proven
	if agent == "" || path == "" || mark == nil || mark.Path != path {
		return Proof{}, from, false
	}
	b, next, err := transcript.Compacted(path, max(from, mark.Offset), true)
	if err != nil || b == nil {
		return Proof{}, next, false
	}
	return Proof{agent: agent, over: true, trigger: b.Trigger, at: b.Time, path: path, start: b.Start, end: b.End}, next, true
}

// Started is the proof that a phase agent's first start on its phase gives (its SubagentStart, routed to
// the phase's sub-run by its own id): its conversation is a new one, which no context before it reaches.
func Started(agent string, at time.Time) Proof { return Proof{agent: agent, over: true, at: at} }

// Fresh records in a phase's sub-run st that a new agent took the phase up (p, from Started), whose own
// transcript is at path: it is the agent briefed there from now on, its transcript is marked where it
// stands (its compactions come after), and nothing the sub-run knew of the context before it is its own.
// A checkpoint the agent before it owed is over, since its notes carry the phase on in this conversation;
// Fresh reports whether one was. It changes nothing for any other proof, or for the agent briefed already.
func Fresh(st *State, p Proof, path string, now time.Time) (checkpoint bool) {
	if !p.over || p.agent == "" || p.path != "" || p.mod || p.agent == st.Run.Briefed {
		return false
	}
	checkpoint = st.CheckpointOwed
	endCheckpoint(st, now)
	st.Run.Briefed, st.Run.Transcript, st.Run.Proven, st.Run.Briefing = p.agent, path, markEnd(path), nil
	st.Run.Context, st.Run.ContextNoticed, st.Run.ContextNudged, st.Run.ContextNudgedAt, st.Run.ContextWarned, st.Run.WarnOwed = nil, false, false, 0, false, false
	return checkpoint
}

// AgentCompacted records in a phase's sub-run st a compaction of its agent's conversation that p proves
// (AgentBoundary): the sub-run's mark moves past it, and the context question is moot. One while the agent
// owes a checkpoint is that checkpoint, as the lead's would be: its conversation goes on from a summary
// and its notes. It reports whether it was, and false, changing nothing, for any other proof: the lead's,
// another agent's, or a boundary counted already.
func AgentCompacted(st *State, p Proof, now time.Time) (checkpoint, ok bool) {
	m := st.Run.Proven
	if !p.over || p.agent == "" || p.agent != st.Run.Briefed || p.path == "" || m == nil || m.Path != p.path || p.end <= m.Offset {
		return false, false
	}
	st.Run.Proven = &Mark{Path: p.path, Offset: p.end}
	st.Run.WarnOwed, st.Run.History = false, true
	checkpoint = st.CheckpointOwed
	endCheckpoint(st, now)
	return checkpoint, true
}

// endCheckpoint ends a checkpoint a phase agent owes, by a new conversation or a compaction proven of it:
// the compaction its stop queued for it, which nothing types into an agent, is done.
func endCheckpoint(st *State, now time.Time) {
	st.CheckpointOwed, st.CheckpointAsked = false, false
	if c := &st.Run.Compaction; c.InFlight() && c.Reason == "checkpoint" {
		c.Status, c.ByBaton, c.Finished = CompactDone, true, now
	}
}

// markEnd marks the transcript at path where it ends now (nil for none, or one baton cannot read); one not
// written yet is marked at its start.
func markEnd(path string) *Mark {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &Mark{Path: path}
	case err != nil:
		return nil
	}
	return &Mark{Path: path, Offset: fi.Size()}
}

// ModReport is the mod's report of a compaction of the lead over, kept for the rewake (Report).
type ModReport struct {
	At      time.Time `json:"at"`
	Trigger string    `json:"trigger,omitempty"`
}

// sameCompaction is how soon after a compaction was proven over a report of one over is that compaction's,
// late: the lead does not compact twice in so short a time.
const sameCompaction = 30 * time.Second

// Report records the mod's report of a compaction of the lead (p, from Reported or Ran), and reports
// whether it changed anything. One begun by the human's /compact, or by the mod for baton's command, moves
// the compaction baton asked for (or is about to) to under way at once, or makes one baton owes count as
// underway, as PreCompact once did; an automatic one proves nothing yet, since Claude Code prepares its
// summary well before it compacts, if it does (docs/research/context-window.md). One over is kept for the
// rewake, which counts it only when the lead's transcript cannot say (Compacted): once, so the mod's report
// of baton's compaction that the transcript proved already is dropped, however late it comes.
func Report(st *State, p Proof, now time.Time) bool {
	if !p.mod || p.agent != "" {
		return false
	}
	comp := &st.Run.Compaction
	if p.over {
		if !comp.Finished.IsZero() && p.at.Sub(comp.Finished) < sameCompaction && !p.at.Before(comp.Finished) {
			return false // the compaction the transcript proved already
		}
		if p.command != 0 && p.epoch == comp.Epoch && comp.Status == CompactDone {
			return false // baton's, proven already
		}
		comp.Reported = &ModReport{At: p.at, Trigger: p.trigger}
		return true
	}
	st.Run.ModReports = now
	if p.trigger != "manual" {
		return false
	}
	switch {
	case comp.Status == CompactQueued || comp.Status == CompactTyped:
		comp.ByBaton = true
	case st.BoundaryOwed || st.CheckpointOwed:
		adoptOwed(st, now)
	default:
		comp.ByBaton = false
	}
	if comp.ByBaton {
		comp.Status = CompactActive
	}
	comp.Trigger, comp.Started = p.trigger, now
	return true
}

// Declined records that the try the mod made at the compaction of epoch, for bridge command command, did
// not compact the lead, for why: its call failed (Claude Code refuses one while a turn runs), or a hook
// vetoed it. With command 0 it is the mod's report of a /compact a hook vetoed, which counts for baton's
// compaction only if baton took it for its own (under way, ByBaton). The host asks again (loop). It reports
// whether it changed anything: nothing for a compaction no longer in flight, or another one.
func Declined(st *State, epoch, command int, why string, now time.Time) bool {
	c := &st.Run.Compaction
	switch {
	case command != 0 && epoch == c.Epoch && c.InFlight():
	case command == 0 && c.Status == CompactActive && c.ByBaton:
	default:
		return false
	}
	c.Declined = &Decline{Command: command, Why: why, At: now}
	return true
}

// Kept is the proof the mod's report of a compaction over gives (Report), while it is kept for the rewake.
func Kept(st State) (Proof, bool) {
	r := st.Run.Compaction.Reported
	if r == nil {
		return Proof{}, false
	}
	return Reported(true, r.Trigger, r.At), true
}

// After is what a compaction of the lead proven over did (Compacted).
type After struct {
	Epoch   int
	Trigger string
	// ByBaton: the compaction baton asked for, or one it owed and Claude Code or the human made.
	ByBaton bool
	// Reason is what baton's compaction was for ("boundary", "switch" or "checkpoint"), when it did that:
	// "" for a compaction that only compacted, which the conversation goes on from by itself.
	Reason string
	// Started: the phase after a boundary or a switch started; Resumed: it was set aside unfinished, and
	// Result is the finished job it was set aside for (delivered with its brief).
	Started, Resumed bool
	Result           *Job
	// Unseen: nothing proved it had begun, so it is logged as begun when it is proven over.
	Unseen bool
	Proof  string // what proved it: "transcript" or "mod"; "kept" for one /baton run counted (Keep)
}

// Compacted records a compaction of the lead that p proves over, and does what it means for the run, as
// SessionStart(compact) and PostCompact once did. The compaction baton asked for is done, and so is one
// it owed that the human or Claude Code made instead; after a boundary or a switch the next phase starts
// (o is the repository as it does), after a checkpoint the checkpoint is over (cover). Any compaction
// ends the context question (WarnOwed), and means the conversation has turns before the next one. A
// boundary older than the compaction baton queued, or than what the run owes (OwedAt), proves only that
// the conversation compacted then. While the run is paused, the one it owed is kept for /baton run
// (Covered, Keep), and what the paused conversation adds is measured from here (PauseCompacted).
//
// It reports false, changing nothing, for a proof of anything else: a compaction begun, a phase agent's,
// a boundary baton counted already (its mark is past it, Proven), or a report it no longer keeps.
func Compacted(st *State, p Proof, now time.Time, o Origin) (After, bool) {
	comp := &st.Run.Compaction
	if !p.over || p.agent != "" {
		return After{}, false
	}
	a := After{Proof: "transcript"}
	if p.mod {
		if r := comp.Reported; r == nil || !r.At.Equal(p.at) {
			return After{}, false
		}
		// The transcript could not say: its mark is no use now, and the lead's next hook makes another.
		st.Run.Proven, a.Proof = nil, "mod"
	} else {
		if m := st.Run.Proven; m == nil || m.Path != p.path || p.end <= m.Offset {
			return After{}, false
		}
		st.Run.Proven = &Mark{Path: p.path, Offset: p.end}
	}
	comp.Reported = nil
	// A compaction from before baton queued its own is not that one: what baton asked for is still to come.
	early := !p.at.IsZero() && !comp.Queued.IsZero() && p.at.Before(comp.Queued)
	ours := !early && (comp.InFlight() || comp.Status == CompactFailed && (st.BoundaryOwed || st.CheckpointOwed))
	a.Unseen = comp.Status != CompactActive
	owed := (st.Mode == ModeRunning || st.Mode == ModePaused) && (st.BoundaryOwed || st.CheckpointOwed)
	if !ours && !early && owed && (p.at.IsZero() || !p.at.Before(st.OwedAt)) {
		// Claude Code (or the human) compacted while a compaction was owed: that one counts.
		adoptOwed(st, now)
		ours = true
	}
	st.Run.WarnOwed = false // the context the question was about is gone
	st.Run.History = true
	a.Epoch, a.Trigger, a.ByBaton = comp.Epoch, p.trigger, ours
	if !ours && comp.InFlight() {
		return a, true // baton's own compaction is still to come, and this is not it
	}
	if ours {
		if comp.Status == CompactFailed {
			// baton gave up waiting; a compaction that finished late still counts.
			if e := st.Run.Escalation; e != nil && e.Kind == "compaction_failed" {
				st.Run.Escalation = nil
			}
		}
		comp.Status = CompactDone
	}
	if a.Unseen {
		comp.Started = now
	}
	comp.ByBaton, comp.Trigger, comp.Finished = ours, p.trigger, now
	if st.Mode == ModePaused {
		at := p.at
		if at.IsZero() || at.After(now) {
			at = now
		}
		st.PauseContext, st.PauseCompacted = 0, at
		if ours && (st.BoundaryOwed || st.CheckpointOwed) {
			st.Covered = now
		}
	}
	if st.Mode != ModeRunning || !ours {
		return a, true
	}
	cover(st, &a, now, o)
	return a, true
}

// cover does what a compaction of the lead that covered what the run owed means for it, and says what in
// a: after a boundary or a switch the phase in hand starts (o is the repository as it does), or resumes
// from where it was set aside, with the finished job it was set aside for; after a checkpoint the
// checkpoint is over. Compacted calls it as the compaction is proven, and Keep for one proven while the
// run was paused.
func cover(st *State, a *After, now time.Time, o Origin) {
	comp := st.Run.Compaction
	switch {
	case comp.StartsPhase() && st.BoundaryOwed:
		a.Reason, a.Started = comp.Reason, true
		if ps := st.Phases[st.Current]; ps != nil && ps.Status == PhaseParked {
			a.Resumed = true
			if j := st.Job(parkedOn(ps.Park)); j != nil && j.Finished() {
				if j.Delivered.IsZero() {
					j.Delivered = now
				}
				x := *j
				a.Result = &x
			}
		}
		Start(st, now, o)
	case comp.Reason == "checkpoint":
		a.Reason = "checkpoint"
		st.CheckpointOwed = false
	}
}

// Keep counts the compaction a paused run kept (Covered) as the one it owes, as /baton run gives the run
// back, before Resume: the phase owed starts with no compaction of its own (o is the repository as it
// does), or the checkpoint is over (cover), and a says what, with Proof "kept". It does nothing when the
// conversation grew by more than ResumeGrowth since (GrownWhilePaused), or a compaction became owed
// after it (OwedAt): one then comes as it would have.
func Keep(st *State, now time.Time, o Origin) (After, bool) {
	comp := st.Run.Compaction
	if st.Mode != ModePaused || st.Covered.IsZero() || st.Covered.Before(st.OwedAt) || st.GrownWhilePaused() > ResumeGrowth ||
		comp.Status != CompactDone || !comp.ByBaton || !(st.BoundaryOwed || st.CheckpointOwed) {
		return After{}, false
	}
	a := After{Epoch: comp.Epoch, Trigger: comp.Trigger, ByBaton: true, Proof: "kept"}
	cover(st, &a, now, o)
	st.Covered = time.Time{}
	return a, a.Reason != ""
}

// SawContext records a reading of the context's size (Claude Code's status line). Once the lead compacted
// while the run is paused, PauseContext is the smallest reading since, from which Resume measures what the
// paused conversation added: the first readings after a compaction may still give the size before it.
func SawContext(st *State, c ContextUse) {
	st.Run.Context = &c
	if st.Mode != ModePaused || st.PauseCompacted.IsZero() || c.At.Before(st.PauseCompacted) || c.Used() <= 0 {
		return
	}
	if st.PauseContext == 0 || c.Used() < st.PauseContext {
		st.PauseContext = c.Used()
	}
}

// adoptOwed makes a compaction baton did not type count as the one it owes: a new epoch, underway. It
// applies while the run is paused too, for /baton run to keep (Keep).
func adoptOwed(st *State, now time.Time) {
	comp := &st.Run.Compaction
	if !comp.InFlight() {
		comp.Epoch++
	}
	reason := "checkpoint"
	if st.BoundaryOwed {
		reason = "boundary"
	}
	comp.ByBaton, comp.Reason, comp.Status, comp.Started, comp.Tries = true, reason, CompactActive, now, 0
}

// parkedOn is the job a park waits for ("" for none).
func parkedOn(pk *Park) string {
	if pk == nil {
		return ""
	}
	return pk.Job
}

// recentBoundary is how long a compact boundary may wait in the lead's transcript for a rewake to count it:
// the rewake looks within a fraction of a second of the compaction, and waits half a minute at most.
const recentBoundary = time.Minute

// Remark marks the lead's transcript at path where it ends now (Runtime.Proven), on a hook of the lead's
// own that is not a compaction's: a compact boundary written before it is one baton has counted, or one
// that came while baton was not looking (its rewake never ran), which proves nothing about what baton owes
// now. It never moves a mark past a boundary written in the last recentBoundary, which a rewake may be
// about to count, nor past a line still being written. A transcript baton has no mark in yet (a session
// started, a /clear, a compaction the mod's report proved) is marked at its end; one not written yet at
// its start; one baton cannot read is left as it was.
func Remark(st *State, path string, now time.Time) { Remarks(*st, path, now).Apply(st) }

// A Remarked is what Remark sets, read from the transcript before the run's lock is taken (Remarks) and
// set under it (Apply): no transcript is read under the lock.
type Remarked struct {
	was, mark *Mark // the mark it read from, and the one to set
	set       bool
}

// Remarks reads the lead's transcript at path as Remark does, from the mark st holds (the run's state a
// moment before the lock, as Store.Peek gives it), and returns what Remark would set.
func Remarks(st State, path string, now time.Time) Remarked {
	if path == "" {
		return Remarked{}
	}
	r := Remarked{was: st.Run.Proven, set: true}
	m := r.was
	if m != nil && m.Path != path {
		m = nil
	}
	var from int64
	if m != nil {
		from = m.Offset
	}
	b, next, err := transcript.Compacted(path, from, false)
	if errors.Is(err, transcript.ErrShrunk) {
		m = nil // another file than the one marked
		b, next, err = transcript.Compacted(path, 0, false)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.mark = &Mark{Path: path}
	case err != nil:
		return Remarked{}
	case m != nil && b != nil && now.Sub(b.Time) < recentBoundary:
		r.mark = &Mark{Path: path, Offset: b.Start}
	default:
		r.mark = &Mark{Path: path, Offset: next}
	}
	return r
}

// Apply sets in st the mark r read, if st's mark is still the one it read from: a mark that moved meanwhile
// (another hook's Remark, a proof counted) stays as it was moved, and the lead's next hook marks the
// transcript again.
func (r Remarked) Apply(st *State) {
	if r.set && sameMark(st.Run.Proven, r.was) {
		st.Run.Proven = r.mark
	}
}

func sameMark(a, b *Mark) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
