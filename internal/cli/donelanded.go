package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// doneLanded is `baton done` of id from the lead, id being a worker's phase (state.PhaseWorking), with the
// lead's notes. The run's branch first follows the lead's checkout to a branch that holds it
// (loop.FollowBranch). Once every commit of the worker's branch is on the run's branch already (the human
// or the lead merged it there, or the phase's work was done there), baton records its work landed
// (state.Landed, by the lead), as a decision without the human with its undo; the run's host then ends the
// worker's agent and retires it as its work landed (loop's watch and retireLanded): what it had not
// committed is saved as a snapshot first, and its branch is removed, with nothing on it left to land. Its
// commits and files are its branch's from its base to its tip as it finished (Worker.Tip), not what a
// merge of the run's branch brought it since. A phase the lead takes up so starts after a boundary its
// stop queues, as after any done. Otherwise done is refused, saying how to finish the phase. git is only
// read here: nothing is merged or moved.
func doneLanded(io IO, s *state.Store, pl plan.Plan, cur state.State, id, notes string, followed bool) int {
	isHosted := hosted(io) // read before the run's lock: it reads processes
	e := cur.WorkerOn(id)
	if e == nil {
		return refused(io, s, "done", fmt.Errorf("phase %s is a worker's: baton marks it done once its work lands", id))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	seen, moved := loop.FollowBranch(ctx, s)
	co, into := s.Checkout, cur.RunBranch
	if moved {
		into = seen.On
	}
	tip, onto := gitx.RevParse(co, "refs/heads/"+e.Branch), gitx.RevParse(co, "refs/heads/"+into)
	on := false
	if tip != "" && onto != "" {
		on, _ = gitx.IsAncestor(ctx, co, tip, onto)
	}
	if !on {
		ahead := -1
		if tip != "" && onto != "" {
			if n, err := gitx.Ahead(ctx, co, "refs/heads/"+into, "refs/heads/"+e.Branch); err == nil {
				ahead = n
			}
		}
		off := ""
		if seen.Off() && !seen.Followable() && seen.Run == into {
			off = seen.On
		}
		s.Event("refused", map[string]any{"command": "done", "phase": id, "why": "a worker's phase", "branch": e.Branch, "unlanded": ahead})
		return fail(io, "%s", decide.WorkerPhaseOpen(id, e.Branch, into, off, ahead))
	}
	own := tip
	if e.Tip != "" && e.Tip != tip {
		if ok, err := gitx.IsAncestor(ctx, co, e.Tip, tip); err == nil && ok {
			own = e.Tip
		}
	}
	commits, _ := gitx.Commits(ctx, co, e.Base, own)
	changes, _ := gitx.Changes(ctx, co, e.Base, own)
	var files []string
	for _, ch := range changes {
		files = append(files, ch.Path)
	}
	dirty := false
	if paths, err := gitx.Dirty(e.Worktree); err == nil {
		dirty = len(paths) > 0
	}
	what, undo := decide.DoneByLead(id, e.Branch, into, len(commits), dirty)
	most := config.EscalationFromEnv(io.Env).MaxAuto
	now := io.Now()
	var next string
	var note state.Decision
	var count int
	st, err := s.Update(func(st *state.State) error {
		if err := guard(isHosted, st, "not done", true); err != nil {
			return err
		}
		if st.ReviewDue != "" {
			return reviewRefusal(st)
		}
		if w := st.WorkerOn(id); w == nil || !w.Is(*e) {
			return fmt.Errorf("not done — %s's worker changed meanwhile: run baton done %s again", id, id)
		}
		if followed {
			st.PlanDrift = ""
			if added, _ := state.Extend(st, pl, now); len(added) > 0 {
				s.Event("plan_changed", map[string]any{"plan": pl.File, "adopted": true, "added": added})
			}
		}
		var err error
		if note, err = state.NoteOn(st, id, what, undo, now); err != nil {
			return err
		}
		if next, err = state.Landed(st, pl, state.Landing{Phase: id, Commits: commits, Files: files, Head: onto, At: now, Told: true, By: state.LandedByLead}, now); err != nil {
			return err
		}
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "note", Text: note.What, At: now})
		if count = st.Unattended(id); decide.CapReached(count, most) {
			state.DueReview(st, id, now)
		}
		return nil
	})
	if err != nil {
		return refused(io, s, "done", err)
	}
	s.AppendHandoff(id, notes)
	s.Event("note", map[string]any{"id": note.ID, "phase": id, "what": note.What, "undo": note.Undo})
	landed := map[string]any{"phase": id, "commits": commits, "files": files, "head": onto, "by": state.LandedByLead}
	if next != "" {
		landed["next"] = next
	}
	s.Event("landed", landed)
	line := decide.DoneLanded(note.ID, id, e.Branch, into, dirty)
	switch {
	case st.ReviewDue != "":
		s.Event("review_due", map[string]any{"phase": id, "decisions": count})
		line += " That makes as many decisions without the human as baton lets a phase make before they review them: end your turn now, and baton stops for the human to review them."
	case st.Mode == state.ModeComplete:
		line += " That was the last phase — the plan is complete. " + decide.PlanComplete(s.Decisions(st))
	case next != "":
		np := pl.Phases[pl.Index(next)]
		line += fmt.Sprintf(" Next phase: %s (%s). End your turn now: baton compacts the context, then starts %s with a fresh brief.", np.ID, np.Title, np.ID)
	case st.Current != "":
		line += " Carry on with " + st.Current + "."
	default:
		line += " No other phase can start yet: every phase left waits for a worker or a job. End your turn now; baton wakes you when there is something to do."
	}
	fmt.Fprintln(io.Out, line)
	return 0
}
