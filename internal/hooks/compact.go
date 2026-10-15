package hooks

import (
	"os"
	"time"

	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's compaction fires PreCompact, SessionStart(compact) and PostCompact as the lead's does,
// with no agent_id and the lead's transcript_path (spike 31), so none of them can say whose compaction it
// is. They move nothing: what baton knows of a compaction changes only on a proof (state.Proof), and the
// rewake is where the lead's is found, counted, and briefed (docs/research/visible-workers.md).

// compactionHook reports a hook a compaction fires: PreCompact, SessionStart(compact), PostCompact, and the
// rewake registered beside PostCompact.
func compactionHook(c Context) bool {
	switch c.Event {
	case "PreCompact", "PostCompact", "PostCompactRewake":
		return true
	case "SessionStart":
		return str(c.Input, "source") == "compact"
	}
	return false
}

// compactSeen is all PreCompact, SessionStart(compact) and PostCompact do: they log compact_seen, with the
// prompt_id that tells one loop's hooks from another's, for a run this host drives.
func (h *handlers) compactSeen(c Context) (Result, error) {
	s, err := h.d.Open(c)
	if err != nil {
		return Result{}, ok(err)
	}
	if st, err := s.Load(); err != nil || !st.IsOwner(s.Instance, c.Now) {
		return Result{}, nil
	}
	fields := map[string]any{"hook": c.Event, "prompt_id": str(c.Input, "prompt_id")}
	if t := str(c.Input, "trigger"); t != "" {
		fields["trigger"] = t
	}
	if c.Event == "SessionStart" {
		fields["source"] = "compact"
	}
	s.Event("compact_seen", fields)
	return Result{}, nil
}

// rewakeWait bounds how long the rewake waits for proof that the lead compacted: the lead's boundary is
// written within a fifth of a second of PostCompact (spike 32), and a phase agent's compaction, which fires
// the same hooks, gives none. rewakePoll is how often it looks.
var rewakeWait, rewakePoll = 30 * time.Second, 100 * time.Millisecond

// sleep waits between looks (a variable, so a test need not wait).
var sleep = time.Sleep

// postCompactRewake runs in the background after every PostCompact ("asyncRewake"), the lead's or a phase
// agent's alike. It is the one path that counts a compaction of the lead, and only on proof: a compact
// boundary in the lead's transcript past baton's mark (state.Boundary), waited for up to rewakeWait, or
// the mod's report of the compaction (state.Kept) when the transcript cannot say. A compaction baton asked
// for (or owed) wakes the model with its brief, exactly once; any other leaves its brief for the model's
// next tool call or prompt, since the conversation goes on by itself (or the human goes on with it). With
// no proof, nothing changes, and a compaction baton still owes is the watchdog's (compaction_failed).
func (h *handlers) postCompactRewake(c Context) (Result, error) {
	s, err := h.d.Open(c)
	if err != nil {
		return Result{}, ok(err)
	}
	path := str(c.Input, "transcript_path")
	var from int64
	for waited := time.Duration(0); ; waited += rewakePoll {
		st, err := s.Load()
		if err != nil || !st.IsOwner(s.Instance, c.Now) {
			return Result{}, nil
		}
		if b := st.Run.Briefing; b != nil && b.Wake {
			return h.wake(c, s)
		}
		p, next, found := state.Boundary(path, st.Run.Proven, from)
		from = next
		if !found && (waited >= rewakeWait || !readable(st, path)) {
			p, found = state.Kept(st)
		}
		if found {
			// Counted here, or by another rewake an instant before (it then wakes the model, if anyone does).
			counted, wake, err := h.prove(c, s, p)
			switch {
			case err != nil:
				return Result{}, ok(err)
			case wake:
				return h.wake(c, s)
			case counted:
				return Result{}, nil
			}
		}
		if waited >= rewakeWait {
			return Result{}, nil
		}
		sleep(rewakePoll)
	}
}

// readable reports a lead's transcript baton can read for proof: it has a mark in it, and it is there.
func readable(st state.State, path string) bool {
	if m := st.Run.Proven; m == nil || m.Path != path {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// prove counts the compaction of the lead that p proves (state.Compacted), owes its brief, and logs what it
// did. It reports whether it counted it, and whether the brief it owes wakes the model: the compaction was
// baton's.
func (h *handlers) prove(c Context, s *state.Store, p state.Proof) (counted, wake bool, err error) {
	pl, _ := s.LoadPlan()
	// What the repository looks like as the next phase starts (read before taking the lock: git can be
	// slow). Only a boundary starts a phase.
	var origin state.Origin
	if cur, err := s.Load(); err == nil && cur.BoundaryOwed {
		origin = state.OriginOf(s.Root)
	}
	var a state.After
	var applied bool
	var owed Owing
	st, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, c.Now) {
			return errNotOwner
		}
		if a, applied = state.Compacted(st, p, c.Now, origin); !applied || st.Mode != state.ModeRunning {
			return nil
		}
		owed = Owe(st, pl, a, true, c.Now)
		return nil
	})
	if err != nil || !applied {
		return false, false, err
	}
	if a.Unseen {
		s.Event("compact_started", map[string]any{"trigger": a.Trigger, "by_baton": a.ByBaton, "epoch": a.Epoch, "reason": st.Run.Compaction.Reason, "proof": a.Proof})
	}
	s.Event("compact_finished", map[string]any{"trigger": a.Trigger, "by_baton": a.ByBaton, "epoch": a.Epoch, "proof": a.Proof})
	owed.Log(s)
	return true, owed.Brief != nil && owed.Brief.Wake, nil
}

// Owing is the brief a compaction of the lead owes the model (Owe), and the phase it starts or resumes.
type Owing struct {
	Brief *state.Briefing
	park  *state.Park
}

// Owe records the brief a compaction of the lead that a describes owes the model (Runtime.Briefing), and
// announces what it starts, in the update that counted the compaction: as the rewake proves it (prove),
// or as /baton run counts one the run kept while it was paused (state.Keep). With wake, the rewake gives
// the brief of a compaction baton asked for, waking the model; without, the model's next tool call takes
// it (baton run's own).
func Owe(st *state.State, pl plan.Plan, a state.After, wake bool, now time.Time) Owing {
	st.Run.LastActivity = now
	o := Owing{Brief: &state.Briefing{Kind: briefKind(a), Phase: st.Current, Wake: wake && a.Reason != "", At: now}}
	if a.Result != nil {
		o.Brief.Job = a.Result.ID
	}
	if ps := st.Phases[st.Current]; ps != nil {
		o.park = ps.Park
	}
	st.Run.Briefing = o.Brief
	msg := "baton: context compacted — continuing " + phaseTitle(pl, st.Current)
	switch o.Brief.Kind {
	case brief.Boundary, brief.Switch:
		msg = "baton: " + phaseTitle(pl, st.Current) + " starts now, with a fresh brief"
	case brief.Resume:
		msg = "baton: " + phaseTitle(pl, st.Current) + " resumes now, from where it was set aside"
	}
	st.Run.Announce = append(st.Run.Announce, msg)
	return o
}

// Log logs the phase o starts (phase_started) or resumes (phase_resumed, with why it was set aside).
func (o Owing) Log(s *state.Store) {
	switch b := o.Brief; {
	case b == nil:
	case b.Kind == brief.Boundary || b.Kind == brief.Switch:
		s.Event("phase_started", map[string]any{"phase": b.Phase})
	case b.Kind == brief.Resume:
		resumed := map[string]any{"phase": b.Phase}
		if o.park != nil {
			resumed["reason"] = o.park.Reason
		}
		s.Event("phase_resumed", resumed)
	}
}

// briefKind is the kind of brief a proven compaction owes (brief.Write).
func briefKind(a state.After) string {
	switch {
	case a.Started && a.Resumed:
		return brief.Resume
	case a.Started && a.Reason == "switch":
		return brief.Switch
	case a.Started:
		return brief.Boundary
	case a.Reason == "checkpoint":
		return brief.Checkpoint
	}
	return brief.Auto
}

// wake takes the brief a compaction baton asked for owes the model (Runtime.Briefing) and wakes the model
// with it, under the line that says what to do now: exactly once, whichever rewake (or prompt, or tool
// call) takes it first. A plan no longer running is owed nothing.
func (h *handlers) wake(c Context, s *state.Store) (Result, error) {
	var b *state.Briefing
	st, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, c.Now) {
			return errNotOwner
		}
		if x := st.Run.Briefing; x != nil && x.Wake {
			st.Run.Briefing = nil
			if st.Mode == state.ModeRunning {
				b = x
			}
		}
		return nil
	})
	if err != nil || b == nil {
		return Result{}, ok(err)
	}
	pl, _ := s.LoadPlan()
	text := h.briefed(c, s, st, *b, "rewake")
	line := decide.Compacted(phaseTitle(pl, b.Phase), b.Kind != brief.Checkpoint, b.Kind == brief.Resume)
	s.Event("rewake", map[string]any{"epoch": st.Run.Compaction.Epoch})
	if text != "" {
		line = text + "\n\n" + line
	}
	return Result{Rewake: line}, nil
}

// takeBriefing takes the brief a compaction owes the model, to give it with a tool call or a prompt of the
// loop's own (one baton's rewake has not given yet, or one that never wakes the model), or as a phase agent
// starts again: never with a hook of a subagent of the loop's model (Context.Agent).
func takeBriefing(c Context, st *state.State) *state.Briefing {
	b := st.Run.Briefing
	if b == nil || c.Agent != "" {
		return nil
	}
	st.Run.Briefing = nil
	if st.Mode != state.ModeRunning {
		return nil
	}
	return b
}

// briefed writes the brief b owes, as the state st stands, and logs that it was given (via the rewake, a
// tool call or a prompt), with the job result it carries.
func (h *handlers) briefed(c Context, s *state.Store, st state.State, b state.Briefing, via string) string {
	pl, err := s.LoadPlan()
	if err != nil {
		return ""
	}
	doc, _ := os.ReadFile(pl.File)
	handoff, _ := os.ReadFile(s.HandoffPath())
	in := brief.Input{Kind: b.Kind, Plan: pl, Doc: doc, State: st, Handoff: string(handoff), Words: words(c, s), Decisions: s.Decisions(st)}
	if s.Worker() != "" {
		in.Tree, in.Needs = s.Root, builtOn(s, st.Current)
	}
	var result *state.Job
	if j := st.Job(b.Job); b.Job != "" && j != nil {
		result = j
		in.Result = jobResult(s, pl, *j, nil, in.Words.AggregateQA)
	}
	text := brief.Write(in)
	if result != nil {
		delivery{jobs: []state.Job{*result}}.event(s, "brief")
	}
	s.Event("brief", map[string]any{"type": b.Kind, "phase": b.Phase, "chars": len(text), "via": via})
	return text
}
