package loop

import (
	"context"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// FollowBranch makes the branch the lead's checkout is on the run's branch of s, the lead's run, when it
// holds the run's branch's tip (state.FollowBranch): logged run_branch_followed, announced to the human,
// and the lead's model told at its next hook. git is read before the run's lock. It is called where the
// run's branch is about to matter (a landing, a new agent's base, a record, the lead's done of a worker's
// phase), so a checkout that never moves costs nothing. seen is the checkout as git said it, its Run ""
// when git could not tell.
func FollowBranch(ctx context.Context, s *state.Store) (seen state.BranchSeen, followed bool) {
	st, err := s.Load()
	if err != nil || st.RunBranch == "" {
		return state.BranchSeen{}, false
	}
	seen, ok := state.SeeBranch(ctx, s.Checkout, st.RunBranch, st.BranchFrom)
	if !ok {
		return state.BranchSeen{}, false
	}
	if !seen.Followable() {
		return seen, false
	}
	if _, err := s.Update(func(st *state.State) error {
		if followed = state.FollowBranch(st, seen, decide.BranchFollowedNote(seen.Run, seen.On, seen.Holds)); followed {
			st.Run.Announce = append(st.Run.Announce, decide.BranchFollowed(seen.Run, seen.On, seen.Holds))
		}
		return nil
	}); err != nil {
		return seen, false
	}
	if followed {
		s.Event("run_branch_followed", map[string]any{"from": seen.Run, "to": seen.On, "holds": seen.Holds})
	}
	return seen, followed
}

// follow is FollowBranch for a step about to act on the run's branch of st (a new agent's base, a
// record), while no tool call of a lead runs in the checkout, which may be moving it from branch to branch
// for a moment. It returns st with the branch followed.
func (c *Coordinator) follow(now time.Time, s *state.Store, st state.State) state.State {
	if st.RunBranch == "" || c.Project == nil || c.busy(now, s) != "" {
		return st
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if seen, followed := FollowBranch(ctx, s); followed {
		st.RunBranch = seen.On
	}
	return st
}
