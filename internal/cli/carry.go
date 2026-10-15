package cli

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// carry carries on, in this session, the run of plan file that another session left with no live host
// (state.Project.Orphans): Claude Code moved the conversation to its background service under a new
// session id, say, and its host is gone. It does so only when this session has no plan of its own and
// exactly one such run exists: it links the session to that run, makes this session's host its driver,
// logs run_carried and says so. ok is false when there is nothing to carry on: the plan is attached
// afresh then, as before.
func carry(io IO, file string) (*state.Store, bool) {
	sid := io.Env("CLAUDE_CODE_SESSION_ID")
	if sid == "" || file == "" {
		return nil, false
	}
	p, err := project(io)
	if err != nil {
		return nil, false
	}
	runs := p.Orphans(file, p.RunOf(sid))
	if len(runs) != 1 {
		return nil, false
	}
	r := runs[0]
	if err := p.Carry(sid, r.ID, file); err != nil {
		return nil, false
	}
	var s *state.Store
	if inst := instance(io); inst != "" {
		s, err = p.Bind(state.Binding{Session: sid, Instance: inst})
	} else {
		s, err = p.Run(r.ID, "")
	}
	if err != nil {
		fmt.Fprintf(io.Err, "baton: carried on run %s, but cannot open it: %v\n", r.ID, err)
		return nil, false
	}
	st, err := s.Load()
	if err != nil {
		return nil, false
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return nil, false
	}
	done := 0
	for _, ph := range pl.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status == state.PhaseDone {
			done++
		}
	}
	at := ""
	if st.Current != "" {
		at = phaseLabel(pl, st.Current)
	}
	from := r.Left(sid)
	s.Event("run_carried", map[string]any{"session": sid, "from": from, "phase": st.Current, "done": done, "hosted": hosted(io)})
	fmt.Fprintln(io.Out, decide.RunCarried(pl.Title, strings.Join(from, ", "), done, len(pl.Phases), at))
	return s, true
}

// runCarried goes on with the run a session just carried on (carry), as `baton run` does with the
// session's own: a run that waits (paused, blocked, a question of baton's) resumes, and one that is under
// way goes on with the phase in hand.
func runCarried(io IO, s *state.Store) int {
	st, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return fail(io, "%v", err)
	}
	for _, line := range append(takeUp(io, s), askAgain(io, s)...) {
		fmt.Fprintln(io.Out, line)
	}
	if !hosted(io) {
		fmt.Fprintln(io.Out, notHostedNote(io, "this session is not hosted by baton, so nothing drives the run here"))
	}
	if st.Resumable() {
		return resume(io, s, pl, st)
	}
	at := ""
	if st.InHand() {
		at = phaseLabel(pl, st.Current)
	}
	fmt.Fprintln(io.Out, decide.CarriedOn(at))
	return 0
}
